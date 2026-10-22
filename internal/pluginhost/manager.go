// SPDX-License-Identifier: GPL-3.0-or-later

// Package pluginhost finds, launches, supervises, and shuts down the
// plugins behind the config's panels: one process per panel, spoken to
// over go-plugin's gRPC transport (see docs/protocol.md).
package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/config"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// State is where a panel's plugin is in its lifecycle.
type State int

const (
	Starting   State = iota // launching, describing, or configuring
	Live                    // configured and running
	Stale                   // polled, and Render failing: its view is old
	Stopping                // failed or restarted; being shut down
	Restarting              // failed; a restart is scheduled for RestartAt
	Crashed                 // failed for good, or too often; waits for Restart
)

func (s State) String() string {
	switch s {
	case Starting:
		return "starting"
	case Live:
		return "live"
	case Stale:
		return "stale"
	case Stopping:
		return "stopping"
	case Restarting:
		return "restarting"
	case Crashed:
		return "crashed"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// PanelState is a snapshot of one enabled panel.
type PanelState struct {
	ID, Plugin string
	State      State
	Since      time.Time // when State last changed
	Info       *tesserav1.DescribeResponse

	// The panel's latest view and when it came: nil until the first of
	// each launch, kept after the plugin fails, and never modified once
	// received, so a snapshot may share it. Polled is
	// set for a plugin that does not stream its view, which the host
	// polls with Render. ViewErr says why the view is old (Stale: the
	// Render error) or missing (Live: the plugin sent one over a limit,
	// and no more are read until the plugin is restarted).
	View    *tesserav1.View
	Updated time.Time
	Polled  bool
	ViewErr error

	// For Restarting and Crashed: why the plugin failed, how its
	// process ended if it exited by itself, and how many failures in a
	// row there have been. Stopping carries only the new failure's Err;
	// Failures there still counts the earlier ones. A failure is a
	// launch that failed, or once live an exit, a hang (a call or the
	// health check past its deadline, or a silent Watch stream), a
	// failed health check, or a broken protocol.
	// Failures is kept while Starting (the launch under way is attempt
	// Failures+1 of MaxAttempts) and while Live, until the plugin
	// shows a sign of life (a passing health check, a Watch response,
	// or an answered call) once the panel has been live for
	// ResetAfter. RestartAt is
	// the Backoff for that count: 1 s after the first failure, 2 s
	// after the second, and so on. A permanent failure (see Retryable)
	// is Crashed at once, whatever MaxAttempts says.
	Err         error
	ExitStatus  string
	Failures    int
	MaxAttempts int
	RestartAt   time.Time // Restarting only
}

// Options tunes supervision. The zero value of a field means its
// default.
type Options struct {
	Backoff      time.Duration // delay after the first failure, doubled after each (1 s), capped at 5 min
	MaxAttempts  int           // failures in a row before giving up (5); see PanelState.Failures
	ResetAfter   time.Duration // time live after which the count restarts (60 s); see PanelState.Failures
	CallTimeout  time.Duration // deadline on each call to a plugin (5 s)
	PingInterval time.Duration // how often a live plugin's health is checked (10 s)
	Heartbeat    time.Duration // the Watch heartbeat interval asked for, at least 1 s (5 s); silence for twice it is a hang
	PollInterval time.Duration // time between Renders of a polled plugin that names none (5 s)
}

func (o Options) withDefaults() Options {
	if o.Backoff <= 0 {
		o.Backoff = time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if o.ResetAfter <= 0 {
		o.ResetAfter = time.Minute
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = callTimeout
	}
	if o.PingInterval <= 0 {
		o.PingInterval = 10 * time.Second
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = 5 * time.Second
	}
	o.Heartbeat = max(o.Heartbeat, time.Second) // the protocol's floor
	if o.PollInterval <= 0 {
		o.PollInterval = 5 * time.Second
	}
	return o
}

// maxBackoff caps the delay before a restart, however many attempts.
const maxBackoff = 5 * time.Minute

// backoff is the delay after the attempt-th failure in a row.
func (o Options) backoff(attempt int) time.Duration {
	d := o.Backoff
	for range attempt - 1 {
		if d >= maxBackoff/2 {
			return maxBackoff
		}
		d *= 2
	}
	return min(d, maxBackoff)
}

// Manager runs and supervises one plugin process per enabled panel.
type Manager struct {
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	changed   chan struct{}
	closeOnce sync.Once
	opts      Options
	dir       string
	logger    *log.Logger

	mu          sync.Mutex
	panels      []*panel
	viewSignal  time.Time // the last Changed signal for a view
	viewPending bool      // one is scheduled
}

type panel struct {
	PanelState
	ctx     context.Context // done once Close begins or Apply drops the panel
	stop    context.CancelFunc
	cfg     config.Panel // under mu
	changed chan struct{} // Apply changed cfg's settings or size; buffered, coalesced
	inst    *Instance // the live one, if any; under mu; read by tests
	logs    *ring
	restart chan struct{}                      // a manual Restart; buffered, coalesced
	events  chan *tesserav1.HandleEventRequest // queued by HandleEvent while Live or Stale
	told    bool                               // Logs already name this failure; supervisor only

	outMu  sync.Mutex
	outGen int // bumped by fence as each launch ends
}

// Start launches the plugin of every enabled panel in cfg from dir,
// with default Options, and returns at once; States reports their
// progress. Plugin output and lifecycle events go to logger and to
// each panel's Logs.
func Start(cfg config.Config, dir string, logger *log.Logger) *Manager {
	return StartWith(cfg, dir, logger, Options{})
}

// StartWith is Start with Options.
func StartWith(cfg config.Config, dir string, logger *log.Logger, opts Options) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		ctx: ctx, cancel: cancel, changed: make(chan struct{}, 1),
		opts: opts.withDefaults(), dir: dir, logger: logger,
	}
	m.Apply(cfg)
	return m
}

// Apply makes the panels supervised those enabled in cfg, as when the
// config file is reloaded. It matches panels by id: a panel that is
// gone or disabled has its plugin shut down in the background, a new or
// re-enabled one is launched, and one whose plugin changed is both. A
// live panel whose settings or size changed is configured again, and a
// failed Configure is a failure like any other; a crashed panel, or
// one waiting to restart, whose settings or size changed restarts now. States then lists the
// panels in cfg's order. Once Close has begun, Apply does nothing.
func (m *Manager) Apply(cfg config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return
	}
	old := make(map[string]*panel, len(m.panels))
	for _, pn := range m.panels {
		old[pn.ID] = pn
	}
	var next []*panel
	for _, p := range cfg.Panels {
		if !p.IsEnabled() {
			continue
		}
		if pn := old[p.ID]; pn != nil && pn.Plugin == p.Plugin {
			delete(old, p.ID)
			if !reflect.DeepEqual(pn.cfg.Settings, p.Settings) || pn.cfg.W != p.W || pn.cfg.H != p.H {
				select {
				case pn.changed <- struct{}{}:
				default: // one is already pending; it reads the latest cfg
				}
			}
			pn.cfg = p
			next = append(next, pn)
			continue
		}
		pn := m.newPanel(p)
		next = append(next, pn)
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.supervise(pn)
		}()
	}
	for _, pn := range old {
		pn.stop() // its supervisor shuts its plugin down
	}
	m.panels = next
	m.signal()
}

func (m *Manager) newPanel(p config.Panel) *panel {
	ctx, stop := context.WithCancel(m.ctx)
	return &panel{
		PanelState: PanelState{ID: p.ID, Plugin: p.Plugin, State: Starting, Since: time.Now(), MaxAttempts: m.opts.MaxAttempts},
		ctx:        ctx,
		stop:       stop,
		cfg:        p,
		changed:    make(chan struct{}, 1),
		logs:       newRing(logLines),
		restart:    make(chan struct{}, 1),
		events:     make(chan *tesserav1.HandleEventRequest, maxEvents),
	}
}

// config is pn's latest panel config.
func (m *Manager) config(pn *panel) config.Panel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return pn.cfg
}

// supervise runs one panel's plugin until Close: it launches and
// configures it, waits for it to exit, and restarts it after a failure
// with exponential backoff, up to MaxAttempts launches in a row. A
// permanent failure (see Retryable) and the last failed attempt leave
// the panel Crashed until Restart.
func (m *Manager) supervise(pn *panel) {
	failures := 0 // failed launches in a row
	for {
		beforeLaunchHook(pn)
		m.update(pn, func(s *PanelState) {
			// A Restart asked for before this launch is this launch, and
			// resets the count like any Restart; one asked for while it
			// is Starting, Restart refuses. Both hold mu, so none slips
			// in between.
			select {
			case <-pn.restart:
				failures = 0
			default:
			}
			for len(pn.events) > 0 { // meant for the last launch's view
				<-pn.events
			}
			select {
			case <-pn.changed: // this launch configures the latest
			default:
			}
			s.State, s.Info, s.Err, s.ExitStatus = Starting, nil, nil, ""
			s.Failures, s.RestartAt = failures, time.Time{}
			s.View, s.Updated, s.Polled, s.ViewErr = nil, time.Time{}, false, nil
		})
		m.event(pn, "starting plugin %s", pn.Plugin)
		pn.told = false
		inst, exit, err := m.launch(pn)
		if pn.ctx.Err() != nil {
			if inst != nil {
				m.shutDown(pn, inst, false)
				pn.fence()
			}
			if exit != "" {
				m.note(pn, "plugin exited: %s", exit)
			}
			return
		}
		// Each event is logged before the state it explains, so a
		// panel's Logs already say why when States shows the change.
		if err == nil {
			m.event(pn, "plugin %s %s live", pn.Plugin, inst.Info.GetVersion())
			m.update(pn, func(s *PanelState) {
				s.State, s.Info = Live, inst.Info
				pn.inst = inst
			})
			var exited, restart bool
			var lived time.Duration
			exited, lived, restart, err = m.watchLive(pn, inst)
			if restart {
				m.event(pn, "restart requested")
				m.update(pn, func(s *PanelState) { s.State = Stopping })
			} else if pn.ctx.Err() == nil {
				m.stopping(pn, err)
			}
			m.update(pn, func(*PanelState) { pn.inst = nil })
			if exited {
				exit = orUnknown(awaitStatus(pn.ctx, inst))
			}
			m.shutDown(pn, inst, exited)
			pn.fence()
			switch {
			case pn.ctx.Err() != nil:
				if exited { // its status, cut short by Close, is in by now
					m.note(pn, "plugin exited: %s", orUnknown(inst.ExitStatus()))
				}
				return
			case restart:
				failures = 0
				continue
			}
			if lived >= m.opts.ResetAfter {
				failures = 0
			}
		}
		if exit != "" {
			m.event(pn, "plugin exited: %s", exit)
		}
		// A Restart asked for while the failed plugin was Stopping
		// relaunches now, without a backoff or give-up shown first.
		select {
		case <-pn.restart:
			m.event(pn, "restart requested")
			failures = 0
			continue
		default:
		}

		failures++
		if !Retryable(err) || failures >= m.opts.MaxAttempts {
			if Retryable(err) {
				m.event(pn, "giving up after failure %d/%d%s", failures, m.opts.MaxAttempts, pn.untold(err))
			} else {
				m.event(pn, "not restarting%s", pn.untold(err))
			}
			m.update(pn, func(s *PanelState) { s.State, s.Err, s.ExitStatus, s.Failures = Crashed, err, exit, failures })
			select {
			case <-pn.ctx.Done():
				return
			case <-pn.restart:
				m.event(pn, "restart requested")
				failures = 0
				continue
			case <-pn.changed:
				m.event(pn, "settings or size changed; restarting")
				failures = 0
				continue
			}
		}
		delay := m.opts.backoff(failures)
		m.event(pn, "restarting in %v (%d/%d failures)%s", delay, failures, m.opts.MaxAttempts, pn.untold(err))
		m.update(pn, func(s *PanelState) {
			s.State, s.Err, s.ExitStatus = Restarting, err, exit
			s.Failures, s.RestartAt = failures, time.Now().Add(delay)
		})
		select {
		case <-pn.ctx.Done():
			return
		case <-pn.restart:
			m.event(pn, "restart requested")
			failures = 0
		case <-pn.changed:
			m.event(pn, "settings or size changed; restarting")
			failures = 0
		case <-time.After(delay):
		}
	}
}

// stopping logs err and shows pn as Stopping, before its failed plugin
// is shut down, which can take seconds; the next one starts only after.
func (m *Manager) stopping(pn *panel, err error) {
	m.event(pn, "stopping plugin: %v", err)
	pn.told = true
	m.update(pn, func(s *PanelState) { s.State, s.Err = Stopping, err })
}

// untold is ": err" for a log line, unless the Logs already name err.
func (pn *panel) untold(err error) string {
	if pn.told {
		return ""
	}
	return fmt.Sprintf(": %v", err)
}

// shutDown closes inst, and says so in pn's Logs if the process
// outlived Close, so the next launch can run beside it, or if it ended
// by itself with an error, unless reported: the caller logs that exit.
// One that exits just before shutDown, as a Restart or Close comes in,
// is the caller's only if it saw the exit.
func (m *Manager) shutDown(pn *panel, inst *Instance, reported bool) {
	inst.Close()
	if !inst.runner.started() {
		return // no process: nothing to outlive Close or exit
	}
	if !inst.Exited() {
		m.note(pn, "plugin %s did not exit when shut down", pn.Plugin)
		return
	}
	if s := inst.ExitStatus(); !reported && s != "" && s != "exit status 0" && inst.runner.ownExit(s) {
		m.note(pn, "plugin exited during shutdown: %s", s)
	}
}

// note is event, but during Close, which event skips, it still logs to
// the host log: go-plugin's own exit line is hidden for supervised
// launches, so a plugin's exit has no other record.
func (m *Manager) note(pn *panel, format string, args ...any) {
	if pn.ctx.Err() != nil {
		m.logger.Printf("panel %q: %s", pn.ID, fmt.Sprintf(format, args...))
		return
	}
	m.event(pn, format, args...)
}

// launch starts and configures pn's plugin. A Configure that misses its
// deadline counts as a hang: the plugin is shut down before anything
// else is called on it.
func (m *Manager) launch(pn *panel) (*Instance, string, error) {
	inst, exit, err := launch(pn.ctx, m.dir, pn.Plugin, m.output(pn), m.logger, m.opts.CallTimeout,
		func(err error) { m.stopping(pn, err) }, func(inst *Instance, reported bool) { m.shutDown(pn, inst, reported) })
	if err != nil {
		pn.fence()
		return nil, exit, err
	}
	if err := configure(pn.ctx, inst, m.config(pn), m.opts.CallTimeout); err != nil {
		if pn.ctx.Err() == nil {
			m.stopping(pn, err)
		}
		// A plugin that died while configuring says how. Its lost
		// connection can fail the call before the exit is seen, so
		// wait briefly for it. One that hung, or answered with an
		// error, is still running and has nothing to say, and Close
		// does not wait.
		var exit string
		var hung hangError
		if Retryable(err) && !errors.As(err, &hung) && !answered(err) && pn.ctx.Err() == nil {
			select {
			case <-inst.Done():
				exit = orUnknown(awaitStatus(pn.ctx, inst))
			case <-pn.ctx.Done():
			case <-time.After(time.Second):
			}
		}
		m.shutDown(pn, inst, exit != "")
		pn.fence()
		return nil, exit, err
	}
	return inst, "", nil
}

// output returns the output callback for a launch of pn's plugin. It
// passes every line to the host log, but to pn's Logs only until fence
// ends the launch: Close stops waiting for a stalled callback, which
// must not add its line among the next launch's.
func (m *Manager) output(pn *panel) func(line string) {
	pn.outMu.Lock()
	gen := pn.outGen
	pn.outMu.Unlock()
	return func(line string) {
		m.logger.Printf("[panel %s] %s", pn.ID, line)
		pn.outMu.Lock()
		defer pn.outMu.Unlock()
		if pn.outGen == gen {
			pn.logs.add("PLUGIN", line)
		}
	}
}

// fence ends the output of pn's last launch, once its plugin is shut
// down.
func (pn *panel) fence() {
	pn.outMu.Lock()
	pn.outGen++
	pn.outMu.Unlock()
}

// ErrStarting is Restart's answer for a panel whose plugin is already
// being launched.
var ErrStarting = errors.New("the plugin is already starting")

// ErrClosed is Restart's and HandleEvent's answer once Close has
// begun.
var ErrClosed = errors.New("the plugin host is closed")

// ErrNotLive is HandleEvent's answer for a panel that is not Live or
// Stale.
var ErrNotLive = errors.New("the panel is not live")

// ErrBusy is HandleEvent's answer when maxEvents events are already
// waiting for the panel's plugin.
var ErrBusy = errors.New("too many events are waiting for the plugin")

// maxEvents is how many events may wait for a panel's plugin.
const maxEvents = 32

// beforeLaunchHook runs at the top of each launch; tests use it to
// place a Restart exactly there.
var beforeLaunchHook = func(*panel) {}

// awaitStatus returns how inst's process ended, waiting briefly, or
// until ctx ends, for the reap that tells.
func awaitStatus(ctx context.Context, inst *Instance) string {
	select {
	case <-inst.runner.reaped:
	case <-ctx.Done():
	case <-time.After(pipeGrace + 500*time.Millisecond): // watch may reap after pipeGrace
	}
	return inst.ExitStatus()
}

func orUnknown(s string) string {
	if s == "" {
		return "status unknown"
	}
	return s
}

// hung reports whether err, from a failed call bounded by ctx with
// timeout, means the plugin did not answer in time. The plugin sees the
// deadline too, and may report it first; a DeadlineExceeded well before
// the deadline is the plugin's own. One the plugin raises itself just
// before the deadline is taken for a hang too: still retried, but
// without its message or an exit status.
func hung(ctx context.Context, err error, timeout time.Duration) bool {
	deadline, _ := ctx.Deadline()
	return errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		status.Code(err) == codes.DeadlineExceeded && time.Until(deadline) < min(timeout/10, 250*time.Millisecond)
}

// answered reports whether err is a status the plugin sent, not a lost
// connection, so the plugin is still running.
func answered(err error) bool {
	s, ok := status.FromError(err)
	return ok && s.Code() != codes.Unavailable && s.Code() != codes.Canceled
}

func configure(ctx context.Context, inst *Instance, p config.Panel, timeout time.Duration) error {
	settings, err := toStruct(p.Settings)
	if err != nil {
		return permanent(fmt.Errorf("plugin %q: settings: %w", p.Plugin, err))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err = inst.Client.Configure(ctx, &tesserav1.ConfigureRequest{
		PanelId:  p.ID,
		Settings: settings,
		Size:     &tesserav1.GridSize{Columns: uint32(p.W), Rows: uint32(p.H)},
	})
	if err == nil {
		return nil // even if the deadline passes right after the answer
	}
	if s, ok := status.FromError(err); ok && s.Code() == codes.InvalidArgument {
		return permanent(fmt.Errorf("plugin %q rejected the settings: %s", p.Plugin, s.Message()))
	}
	if s, ok := status.FromError(err); ok && s.Code() == codes.Unimplemented {
		return permanent(fmt.Errorf("plugin %q does not implement Configure: %w", p.Plugin, err))
	}
	if hung(ctx, err, timeout) {
		return hangError{fmt.Errorf("plugin %q hung: Configure did not answer within %v", p.Plugin, timeout)}
	}
	return fmt.Errorf("plugin %q: Configure: %w", p.Plugin, err)
}

// update applies f to pn's state under the lock, stamps Since when the
// State changes, and signals Changed.
func (m *Manager) update(pn *panel, f func(*PanelState)) {
	m.mu.Lock()
	was := pn.State
	f(&pn.PanelState)
	if pn.State != was {
		pn.Since = time.Now()
	}
	m.mu.Unlock()
	m.signal()
}

func (m *Manager) signal() {
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

// setView makes v pn's view. Changed is signalled at most every
// viewEvery for views, however fast plugins send them; States then has
// each panel's latest.
func (m *Manager) setView(pn *panel, v *tesserav1.View) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pn.View, pn.Updated = v, time.Now()
	if m.viewPending {
		return
	}
	if wait := viewEvery - time.Since(m.viewSignal); wait > 0 {
		m.viewPending = true
		time.AfterFunc(wait, func() {
			m.mu.Lock()
			m.viewPending, m.viewSignal = false, time.Now()
			m.mu.Unlock()
			m.signal()
		})
		return
	}
	m.viewSignal = time.Now()
	m.signal()
}

// event records a lifecycle event in pn's Logs and the host log, unless
// Close is under way. Logs holds lines: those after an event's first
// start with "| ", so text a plugin put in an error cannot pass for an
// event of its own.
func (m *Manager) event(pn *panel, format string, args ...any) {
	if pn.ctx.Err() != nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	for i, line := range strings.Split(msg, "\n") {
		if i > 0 {
			line = "| " + line
		}
		pn.logs.add("HOST", line)
	}
	m.logger.Printf("panel %q: %s", pn.ID, msg)
}

// States returns a snapshot of every enabled panel, in config order.
// Once Close begins, the snapshot no longer changes.
func (m *Manager) States() []PanelState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PanelState, len(m.panels))
	for i, pn := range m.panels {
		out[i] = pn.PanelState
	}
	return out
}

// Changed receives a value after a panel's state changes.
func (m *Manager) Changed() <-chan struct{} { return m.changed }

// Restart relaunches panel id's plugin now, its failure count reset:
// one that is live is shut down first, and one waiting to restart or
// crashed starts at once. It is the error tile's Restart button.
//
// A panel already Starting is left alone, and Restart returns
// ErrStarting: the launch under way is the restart. Once Close has
// begun, Restart returns ErrClosed.
func (m *Manager) Restart(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return ErrClosed
	}
	for _, pn := range m.panels {
		if pn.ID != id {
			continue
		}
		if pn.State == Starting {
			return ErrStarting
		}
		select {
		case pn.restart <- struct{}{}:
		default: // one is already pending
		}
		return nil
	}
	return fmt.Errorf("no enabled panel %q", id)
}

// HandleEvent queues req, a user input event, for panel id's plugin,
// which gets the panel's events one at a time and in order. Events
// still waiting when the plugin fails or restarts are dropped: they
// were meant for its last view.
func (m *Manager) HandleEvent(id string, req *tesserav1.HandleEventRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return ErrClosed
	}
	for _, pn := range m.panels {
		if pn.ID != id {
			continue
		}
		if pn.State != Live && pn.State != Stale {
			return ErrNotLive
		}
		select {
		case pn.events <- req:
			return nil
		default:
			return ErrBusy
		}
	}
	return fmt.Errorf("no enabled panel %q", id)
}

// Logs returns panel id's recent output and lifecycle events, oldest
// first, or nil for an unknown panel.
func (m *Manager) Logs(id string) []string {
	if pn := m.find(id); pn != nil {
		return pn.logs.lines()
	}
	return nil
}

func (m *Manager) find(id string) *panel {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pn := range m.panels {
		if pn.ID == id {
			return pn
		}
	}
	return nil
}

// Close stops supervision and shuts every plugin down in parallel. It
// returns once every plugin has exited or been killed, or Instance.Close
// has given up on it, and every launch still starting has stopped, which
// takes an Instance.Close; also when called again while a first call is
// still shutting them down. Panel states are not updated once Close
// begins: States keeps showing each panel as it was, and Changed stays
// open.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock() // so no Apply adds a supervisor once Wait begins
		m.cancel()  // each supervisor closes its own instance
		m.mu.Unlock()
		m.wg.Wait()
	})
}

// logLines is how many lines each panel's Logs keeps, and logLine the
// bytes of each line it keeps.
const (
	logLines = 500
	logLine  = 4 << 10
)

// ring keeps the last lines added to it, each stamped with the time and
// a source tag.
type ring struct {
	mu   sync.Mutex
	buf  []string
	next int
	full bool
}

func newRing(n int) *ring { return &ring{buf: make([]string, n)} }

func (r *ring) add(source, line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(line) > logLine {
		cut := logLine - len("…")
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut] + "…"
	}
	r.buf[r.next] = time.Now().Format("15:04:05") + "  " + source + "  " + line
	r.next = (r.next + 1) % len(r.buf)
	r.full = r.full || r.next == 0
}

func (r *ring) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]string(nil), r.buf[:r.next]...)
	}
	return append(append([]string(nil), r.buf[r.next:]...), r.buf[:r.next]...)
}
