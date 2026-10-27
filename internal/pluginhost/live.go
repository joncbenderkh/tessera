// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	grpcproto "google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// maxViewBytes is the largest Watch or Render response read, gRPC's
// default receive limit. viewCodec enforces it, so the host knows the
// refusal is its own. go-plugin lifts gRPC's limit, so the host sets
// it to recvCap, to bound what it reads at all: gRPC's error for a
// response over that is like one the plugin sent.
const (
	maxViewBytes = 4 << 20
	recvCap      = 4 * maxViewBytes
)

// maxViewDepth is how deeply a view's widgets may nest: View.body is
// depth 1, and each StackItem.widget adds 1.
const maxViewDepth = 16

// maxViewWidgets is how many widgets a view may hold, View.body and
// every StackItem.widget below it: the host lays each one out every
// frame, even one that takes no space.
const maxViewWidgets = 1024

// maxTableColumns is how many columns a table may have: the host lays
// each one out every frame, even one too narrow to show.
const maxTableColumns = 64

// minPoll and maxPoll clamp the poll interval a plugin asks for.
const (
	minPoll = 250 * time.Millisecond
	maxPoll = time.Hour
)

// viewEvery is the shortest time between two Changed signals for new
// views, so a plugin that pushes views faster costs no extra redraws.
const viewEvery = 33 * time.Millisecond

// end says why watchLive stopped waiting: inst exited, Restart was
// asked for, or neither, which is Close. changed marks a restart for
// new settings or size.
type end struct{ exited, restart, changed bool }

// live is one live plugin's watch: its views, events, and health.
type live struct {
	m      *Manager
	pn     *panel
	inst   *Instance
	start  time.Time
	lastOK time.Time // the last sign of life; see alive
	polled bool      // on the Render fallback
	off    bool      // a view over a limit stopped views until a relaunch
	stale  bool      // polled, and Render failing
}

// watchLive waits while inst is live: it reads the plugin's views, from
// its Watch stream or by polling Render, sends the panel's queued
// events, and checks the plugin's health, until it exits, fails,
// Restart is asked for, or Close, any of which also ends a call under
// way. It returns whether the process exited, how long it was live,
// whether a Restart ended it, and the failure, if any. It counts as
// live only until its last sign of life (see alive), since it may have
// frozen right after, so the count of failures it resets agrees with
// PanelState.Failures.
func (m *Manager) watchLive(pn *panel, inst *Instance) (exited bool, lived time.Duration, restart string, err error) {
	w := &live{m: m, pn: pn, inst: inst, start: time.Now()}
	w.lastOK = w.start
	e, err := w.run()
	if e.exited {
		err = fmt.Errorf("plugin %q exited unexpectedly", w.name())
	}
	switch {
	case e.changed:
		restart = "settings or size changed; restarting"
	case e.restart:
		restart = "restart requested"
	}
	return e.exited, w.lastOK.Sub(w.start), restart, err
}

func (w *live) name() string { return w.inst.Info.GetName() }

// alive records a sign of life: a passing health check, a Watch
// response, or an answer to a call. The first once the panel has been
// live for ResetAfter clears its Failures.
func (w *live) alive() {
	settled := w.lastOK.Sub(w.start) >= w.m.opts.ResetAfter
	w.lastOK = time.Now()
	if !settled && w.lastOK.Sub(w.start) >= w.m.opts.ResetAfter {
		w.m.update(w.pn, func(s *PanelState) { s.Failures = 0 })
	}
}

func (w *live) run() (end, error) {
	m, pn, inst := w.m, w.pn, w.inst
	health := time.NewTicker(m.opts.PingInterval)
	defer health.Stop()
	ctx, stopWatch := context.WithCancel(pn.ctx)
	defer stopWatch()
	st := w.watch(ctx)
	ready := st.ready
	quiet := time.NewTicker(m.opts.Heartbeat / 2)
	defer quiet.Stop()
	silence := quiet.C
	poll := time.NewTimer(time.Hour)
	poll.Stop()
	defer poll.Stop()
	unwatch := func() {
		stopWatch()
		ready, silence = nil, nil
	}
	render := func() (*end, error) {
		next, stop, err := w.render()
		switch {
		case stop != nil || err != nil:
		case w.off:
			poll.Stop()
		default:
			poll.Reset(next)
		}
		return stop, err
	}
	for {
		// An idle wait of more than a tick or two means the clock
		// jumped, as across a sleep on some systems, rather than a call
		// kept the loop busy: give the plugin a fresh wait for its next
		// heartbeat. Checked by each case a wakeup may pick first.
		waiting := time.Now()
		resumed := func() {
			if now := time.Now(); now.Sub(waiting) > 2*m.opts.Heartbeat {
				st.resume(now)
			}
		}
		select {
		case <-pn.ctx.Done():
			return end{}, nil
		case <-pn.restart:
			return end{restart: true}, nil
		case <-inst.Done():
			return end{exited: true}, nil
		case <-health.C:
			resumed()
			stop, hang, err := w.call(inst.Ping)
			if stop != nil {
				return *stop, nil
			}
			if err == nil {
				w.alive()
				continue
			}
			if stop := w.settle(); stop != nil {
				return *stop, nil
			}
			if hang {
				return end{}, hangError{fmt.Errorf("plugin %q hung: health check did not answer within %v", w.name(), m.opts.CallTimeout)}
			}
			return end{}, fmt.Errorf("plugin %q failed its health check: %w", w.name(), err)
		case <-ready:
			view, got, err := st.take()
			if got {
				w.alive()
			}
			if view != nil {
				m.setView(pn, view)
			}
			switch {
			case err == nil:
			case status.Code(err) == codes.Unimplemented && !st.answered():
				unwatch()
				w.polled = true
				m.event(pn, "plugin does not stream its view; polling Render")
				m.update(pn, func(s *PanelState) { s.Polled = true })
				poll.Reset(0)
			case refused(w.name(), st.codec) != nil:
				unwatch()
				w.overLimit(refused(w.name(), st.codec))
			default:
				if stop := w.settle(); stop != nil {
					return *stop, nil
				}
				if errors.Is(err, io.EOF) {
					return end{}, fmt.Errorf("plugin %q ended its Watch stream", w.name())
				}
				return end{}, fmt.Errorf("plugin %q ended its Watch stream: %w", w.name(), err)
			}
		case <-silence:
			resumed()
			if quiet := time.Since(st.lastResponse()); quiet > 2*m.opts.Heartbeat {
				return end{}, hangError{fmt.Errorf("plugin %q hung: its Watch stream was silent for %v", w.name(), quiet.Round(time.Millisecond))}
			}
		case <-poll.C:
			if stop, err := render(); stop != nil || err != nil {
				return orClose(stop), err
			}
		case <-pn.changed:
			resumed()
			if w.off { // new settings may fit the limits; only a relaunch shows views again
				return end{restart: true, changed: true}, nil
			}
			cfg := m.config(pn)
			stop, _, err := w.call(func(ctx context.Context) error {
				return configure(ctx, inst, cfg, m.opts.CallTimeout)
			})
			if stop != nil {
				return *stop, nil
			}
			if err != nil {
				var hung hangError
				if Retryable(err) && !errors.As(err, &hung) && !answered(err) {
					if stop := w.settle(); stop != nil {
						return *stop, nil
					}
				}
				return end{}, err
			}
			w.alive()
			m.event(pn, "settings or size changed; plugin configured again")
			if w.polled && !w.off {
				if stop, err := render(); stop != nil || err != nil {
					return orClose(stop), err
				}
			}
		case req := <-pn.events:
			resumed()
			stop, err := w.handle(m.latest(pn, req))
			if stop != nil {
				return *stop, nil
			}
			if err != nil {
				return end{}, err
			}
			if w.polled && !w.off { // shows the event's effect before the next event
				if stop, err := render(); stop != nil || err != nil {
					return orClose(stop), err
				}
			}
		}
	}
}

// orClose is stop, or Close's end if there is none.
func orClose(stop *end) end {
	if stop == nil {
		return end{}
	}
	return *stop
}

// call runs f with the call deadline and waits for its error, and
// whether that is a hang. It returns stop instead, and cancels f, if
// inst exits, Restart is asked for, or Close comes first.
func (w *live) call(f func(context.Context) error) (stop *end, hang bool, err error) {
	ctx, cancel := context.WithTimeout(w.pn.ctx, w.m.opts.CallTimeout)
	defer cancel()
	answer := make(chan error, 1)
	go func() { answer <- f(ctx) }()
	select {
	case err = <-answer:
		return nil, err != nil && hung(ctx, err, w.m.opts.CallTimeout), err
	case <-w.pn.ctx.Done():
		return &end{}, false, nil
	case <-w.pn.restart:
		return &end{restart: true}, false, nil
	case <-w.inst.Done():
		return &end{exited: true}, false, nil
	}
}

// settle gives a plugin whose call failed a moment for its exit to
// show, since one that died fails calls before its exit is seen. It
// returns stop if something ended the wait.
func (w *live) settle() *end {
	select {
	case <-w.pn.ctx.Done():
		return &end{}
	case <-w.pn.restart:
		return &end{restart: true}
	case <-w.inst.Done():
		return &end{exited: true}
	case <-time.After(500 * time.Millisecond):
		return nil
	}
}

// render polls the plugin's view once and returns when to poll next.
// A Render the plugin answers with an error leaves its last view in
// place and the panel Stale until one succeeds.
func (w *live) render() (next time.Duration, stop *end, err error) {
	m, pn := w.m, w.pn
	var resp *tesserav1.RenderResponse
	codec := newViewCodec()
	stop, hang, err := w.call(func(ctx context.Context) error {
		r, err := w.inst.Client.Render(ctx, &tesserav1.RenderRequest{}, grpc.ForceCodecV2(codec), grpc.MaxCallRecvMsgSize(recvCap))
		resp = r
		return err
	})
	if stop != nil {
		return 0, stop, nil
	}
	next = m.opts.PollInterval
	switch {
	case err == nil:
		w.alive()
		if d := resp.GetPollInterval().AsDuration(); d > 0 {
			next = min(max(d, minPoll), maxPoll)
		}
		view := resp.GetView()
		if w.stale {
			w.stale = false
			m.event(pn, "Render answers again")
			m.update(pn, func(s *PanelState) { s.State, s.ViewErr = Live, nil })
		}
		if view != nil { // else a later view schema version's field: nothing new to show
			m.setView(pn, view)
		}
	case hang:
		return 0, nil, hangError{fmt.Errorf("plugin %q hung: Render did not answer within %v", w.name(), m.opts.CallTimeout)}
	case refused(w.name(), codec) != nil:
		w.overLimit(refused(w.name(), codec))
	case status.Code(err) == codes.Unimplemented:
		return 0, nil, permanent(fmt.Errorf("plugin %q implements neither Watch nor Render", w.name()))
	case codec.malformed() != nil: // a broken protocol, as on Watch
		return 0, nil, fmt.Errorf("plugin %q sent a Render response that does not decode: %w", w.name(), codec.malformed())
	default:
		stop, own := w.own(err)
		if stop != nil {
			return 0, stop, nil
		}
		if !own {
			return 0, nil, fmt.Errorf("plugin %q: Render: %w", w.name(), err)
		}
		w.alive()
		if !w.stale {
			w.stale = true
			m.event(pn, "Render failed; showing the last view: %v", err)
		}
		m.update(pn, func(s *PanelState) { s.State, s.ViewErr = Stale, err })
	}
	return next, nil, nil
}

// handle sends one input event. An event the plugin answers with an
// error is logged; the plugin keeps running.
func (w *live) handle(req *tesserav1.HandleEventRequest) (*end, error) {
	stop, hang, err := w.call(func(ctx context.Context) error {
		_, err := w.inst.Client.HandleEvent(ctx, req)
		return err
	})
	switch {
	case stop != nil:
		return stop, nil
	case err == nil:
		w.alive()
	case hang:
		return nil, hangError{fmt.Errorf("plugin %q hung: HandleEvent did not answer within %v", w.name(), w.m.opts.CallTimeout)}
	default:
		stop, own := w.own(err)
		if stop != nil {
			return stop, nil
		}
		if !own {
			return nil, fmt.Errorf("plugin %q: HandleEvent: %w", w.name(), err)
		}
		w.alive()
		w.m.event(w.pn, "plugin %q failed an event: %v", w.name(), err)
	}
	return nil, nil
}

// own reports whether the plugin sent err itself, rather than its
// connection failing: a status other than UNAVAILABLE or CANCELED, or
// one of those while the plugin still answers a health check. One that
// died fails calls before its exit is seen, so it gets a moment for
// that first. It returns stop instead if something ends the wait.
func (w *live) own(err error) (stop *end, own bool) {
	if answered(err) {
		return nil, true
	}
	if stop := w.settle(); stop != nil {
		return stop, false
	}
	stop, _, err = w.call(w.inst.Ping)
	return stop, stop == nil && err == nil
}

// overLimit stops views after one over a limit: the panel stays Live,
// showing err in place of a view, and the plugin keeps running.
func (w *live) overLimit(err error) {
	w.off, w.stale = true, false
	w.m.event(w.pn, "%v; no more views until the plugin is restarted", err)
	w.m.update(w.pn, func(s *PanelState) { s.State, s.View, s.ViewErr = Live, nil, err })
}

// refused reports a view over a limit, which c, the codec of the Watch
// or Render call that failed, refused.
func refused(name string, c *viewCodec) error {
	if e := c.refusal(); e != nil {
		return fmt.Errorf("plugin %q sent a %w", name, e)
	}
	return nil
}

// viewCodec is gRPC's proto codec for one Watch or Render call, which
// refuses a response over maxViewBytes, or a view nested deeper than
// maxViewDepth, holding more than maxViewWidgets, or with a table of
// more than maxTableColumns, and records that it did: gRPC's errors
// for these read like ones a plugin could send. It checks each response as it is
// decoded, so none escapes the check by being replaced unread.
type viewCodec struct {
	encoding.CodecV2

	mu      sync.Mutex
	refused error // a response over a limit
	broken  error // a response that did not decode for another reason
}

func newViewCodec() *viewCodec {
	return &viewCodec{CodecV2: encoding.GetCodecV2(grpcproto.Name)}
}

func (c *viewCodec) Unmarshal(data mem.BufferSlice, v any) error {
	if n := data.Len(); n > maxViewBytes {
		return c.fail(true, fmt.Errorf("view over the size limit: %d bytes, over %d", n, maxViewBytes))
	}
	err := c.CodecV2.Unmarshal(data, v)
	if err != nil && strings.Contains(err.Error(), "recursion depth") { // protobuf exports no error for it
		return c.fail(true, fmt.Errorf("view nested over the limit of %d: %w", maxViewDepth, err))
	}
	if err != nil {
		return c.fail(false, err)
	}
	if r, ok := v.(interface{ GetView() *tesserav1.View }); ok {
		d, n, cols := measure(r.GetView().GetBody())
		if d > maxViewDepth {
			return c.fail(true, fmt.Errorf("view nested %d deep, over the limit of %d", d, maxViewDepth))
		}
		if n > maxViewWidgets {
			return c.fail(true, fmt.Errorf("view of %d widgets, over the limit of %d", n, maxViewWidgets))
		}
		if cols > maxTableColumns {
			return c.fail(true, fmt.Errorf("view with a table of %d columns, over the limit of %d", cols, maxTableColumns))
		}
	}
	return nil
}

func (c *viewCodec) fail(refused bool, err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if refused {
		c.refused = err
	} else {
		c.broken = err
	}
	return err
}

// malformed returns why a response did not decode, if one did not.
func (c *viewCodec) malformed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.broken
}

func (c *viewCodec) refusal() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refused
}

// measure returns how deeply w's widgets nest, how many there are, and
// the most columns any of its tables has.
func measure(w *tesserav1.Widget) (depth, count, columns int) {
	if w == nil {
		return 0, 0, 0
	}
	columns = len(w.GetTable().GetColumns())
	for _, item := range w.GetStack().GetItems() {
		d, n, c := measure(item.GetWidget())
		depth, count, columns = max(depth, d), count+n, max(columns, c)
	}
	return 1 + depth, 1 + count, columns
}

// stream reads a Watch stream in the background and keeps only its
// latest view, so a host busy with a call never stalls the stream, and
// a plugin that pushes views fast costs one decode each.
type stream struct {
	ready chan struct{} // a response or the end; coalesced
	codec *viewCodec

	mu   sync.Mutex
	view *tesserav1.View // the latest view not yet taken
	got  bool            // a response came since the last take
	ever bool            // a response ever came
	last time.Time       // the latest response, or the stream's opening
	err  error           // why it ended, once it has
}

// watch opens the plugin's Watch stream; ending ctx closes it.
func (w *live) watch(ctx context.Context) *stream {
	st := &stream{ready: make(chan struct{}, 1), codec: newViewCodec(), last: time.Now()}
	go func() {
		err := func() error {
			c, err := w.inst.Client.Watch(ctx, &tesserav1.WatchRequest{HeartbeatInterval: durationpb.New(w.m.opts.Heartbeat)},
				grpc.ForceCodecV2(st.codec), grpc.MaxCallRecvMsgSize(recvCap))
			if err != nil {
				return err
			}
			for {
				resp, err := c.Recv()
				if err != nil {
					return err
				}
				st.mu.Lock()
				st.got, st.ever, st.last = true, true, time.Now()
				if v := resp.GetView(); v != nil {
					st.view = v
				}
				st.mu.Unlock()
				st.notify()
			}
		}()
		st.mu.Lock()
		st.err = err
		st.mu.Unlock()
		st.notify()
	}()
	return st
}

func (st *stream) notify() {
	select {
	case st.ready <- struct{}{}:
	default:
	}
}

// take returns the latest view not yet taken, whether any response
// came since the last take, and why the stream ended, if it has.
func (st *stream) take() (*tesserav1.View, bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	view, got := st.view, st.got
	st.view, st.got = nil, false
	return view, got, st.err
}

// answered reports whether any response ever came.
func (st *stream) answered() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.ever
}

// resume restarts the wait for the next response at now.
func (st *stream) resume(now time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.last = now
}

func (st *stream) lastResponse() time.Time {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.last
}
