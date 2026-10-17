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
	Stale                   // running, but its view is old; set by view updates (#14)
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

	// For Restarting and Crashed: why the plugin failed, how its
	// process ended if it exited by itself, and how many failures in a
	// row there have been. Stopping carries only the new failure's Err;
	// Failures there still counts the earlier ones. A failure is a
	// launch that failed, or an exit or failed health check once live.
	// Failures is kept while Starting (the launch under way is attempt
	// Failures+1 of MaxAttempts) and while Live, until the panel has
	// been live for ResetAfter. RestartAt is the Backoff for that
	// count: 1 s after the first failure, 2 s after the second, and so
	// on. A permanent failure (see Retryable) is Crashed at once,
	// whatever MaxAttempts says.
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
	MaxAttempts  int           // failed launches in a row before giving up (5)
	ResetAfter   time.Duration // time live after which the count restarts (60 s)
	CallTimeout  time.Duration // deadline on each call to a plugin (5 s)
	PingInterval time.Duration // how often a live plugin's health is checked (10 s)
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

	mu     sync.Mutex
	panels []*panel
}

type panel struct {
	PanelState
	cfg     config.Panel
	inst    *Instance // the live one, if any; under mu; read by tests
	logs    *ring
	restart chan struct{} // a manual Restart; buffered, coalesced
	told    bool          // Logs already name this failure; supervisor only

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
	now := time.Now()
	for _, p := range cfg.Panels {
		if !p.IsEnabled() {
			continue
		}
		pn := &panel{
			PanelState: PanelState{ID: p.ID, Plugin: p.Plugin, State: Starting, Since: now, MaxAttempts: m.opts.MaxAttempts},
			cfg:        p,
			logs:       newRing(logLines),
			restart:    make(chan struct{}, 1),
		}
		m.panels = append(m.panels, pn)
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.supervise(pn)
		}()
	}
	return m
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
			s.State, s.Info, s.Err, s.ExitStatus = Starting, nil, nil, ""
			s.Failures, s.RestartAt = failures, time.Time{}
		})
		m.event(pn, "starting plugin %s", pn.Plugin)
		pn.told = false
		inst, exit, err := m.launch(pn)
		if m.ctx.Err() != nil {
			if inst != nil {
				inst.Close()
				pn.fence()
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
			} else if m.ctx.Err() == nil {
				m.stopping(pn, err)
			}
			m.update(pn, func(*PanelState) { pn.inst = nil })
			if exited {
				exit = orUnknown(awaitStatus(m.ctx, inst))
			}
			m.shutDown(pn, inst)
			pn.fence()
			switch {
			case m.ctx.Err() != nil:
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

		failures++
		if !Retryable(err) || failures >= m.opts.MaxAttempts {
			if Retryable(err) {
				m.event(pn, "giving up after failure %d/%d%s", failures, m.opts.MaxAttempts, pn.untold(err))
			} else {
				m.event(pn, "not restarting%s", pn.untold(err))
			}
			m.update(pn, func(s *PanelState) { s.State, s.Err, s.ExitStatus, s.Failures = Crashed, err, exit, failures })
			select {
			case <-m.ctx.Done():
				return
			case <-pn.restart:
				m.event(pn, "restart requested")
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
		case <-m.ctx.Done():
			return
		case <-pn.restart:
			m.event(pn, "restart requested")
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
// outlived Close, so the next launch can run beside it.
func (m *Manager) shutDown(pn *panel, inst *Instance) {
	inst.Close()
	if !inst.Exited() {
		m.event(pn, "plugin %s did not exit when shut down", pn.Plugin)
	}
}

// watchLive waits while inst is live: until it exits, fails a health
// check, Restart is asked for, or Close; a Restart or Close also ends a
// health check under way. It returns whether the process exited, how
// long it was live, whether a Restart ended it, and the failure, if
// any.
func (m *Manager) watchLive(pn *panel, inst *Instance) (exited bool, lived time.Duration, restart bool, err error) {
	start := time.Now()
	tick := time.NewTicker(m.opts.PingInterval)
	defer tick.Stop()
	settled := time.NewTimer(m.opts.ResetAfter)
	defer settled.Stop()
	exit := func() (bool, time.Duration, bool, error) {
		return true, time.Since(start), false, fmt.Errorf("plugin %q exited unexpectedly", inst.Info.GetName())
	}
	for {
		select {
		case <-m.ctx.Done():
			return false, time.Since(start), false, nil
		case <-pn.restart:
			return false, time.Since(start), true, nil
		case <-inst.Done():
			return exit()
		case <-settled.C:
			m.update(pn, func(s *PanelState) { s.Failures = 0 })
			continue
		case <-tick.C:
		}
		ctx, cancel := context.WithTimeout(m.ctx, m.opts.CallTimeout)
		answer := make(chan error, 1)
		go func() { answer <- inst.Ping(ctx) }()
		var perr error
		select {
		case perr = <-answer:
		case <-m.ctx.Done():
			cancel()
			return false, time.Since(start), false, nil
		case <-pn.restart:
			cancel()
			return false, time.Since(start), true, nil
		case <-inst.Done():
			cancel()
			return exit()
		}
		hang := perr != nil && hung(ctx, perr, m.opts.CallTimeout)
		cancel()
		if perr == nil {
			continue
		}
		// A plugin that died makes the check fail before its exit is
		// seen; give the exit a moment to show.
		select {
		case <-m.ctx.Done():
			return false, time.Since(start), false, nil
		case <-pn.restart:
			return false, time.Since(start), true, nil
		case <-inst.Done():
			return exit()
		case <-time.After(500 * time.Millisecond):
		}
		if !hang {
			return false, time.Since(start), false, fmt.Errorf("plugin %q failed its health check: %w", inst.Info.GetName(), perr)
		}
		return false, time.Since(start), false, hangError{fmt.Errorf("plugin %q hung: health check did not answer within %v", inst.Info.GetName(), m.opts.CallTimeout)}
	}
}

// launch starts and configures pn's plugin. A Configure that misses its
// deadline counts as a hang: the plugin is shut down before anything
// else is called on it.
func (m *Manager) launch(pn *panel) (*Instance, string, error) {
	inst, exit, err := launch(m.ctx, m.dir, pn.Plugin, m.output(pn), m.logger, m.opts.CallTimeout, func(err error) { m.stopping(pn, err) })
	if err != nil {
		pn.fence()
		return nil, exit, err
	}
	if err := configure(m.ctx, inst, pn.cfg, m.opts.CallTimeout); err != nil {
		if m.ctx.Err() == nil {
			m.stopping(pn, err)
		}
		// A plugin that died while configuring says how. Its lost
		// connection can fail the call before the exit is seen, so
		// wait briefly for it. One that hung, or answered with an
		// error, is still running and has nothing to say, and Close
		// does not wait.
		var exit string
		var hung hangError
		if Retryable(err) && !errors.As(err, &hung) && !answered(err) && m.ctx.Err() == nil {
			select {
			case <-inst.Done():
				exit = orUnknown(awaitStatus(m.ctx, inst))
			case <-m.ctx.Done():
			case <-time.After(time.Second):
			}
		}
		m.shutDown(pn, inst)
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

// ErrClosed is Restart's answer once Close has begun.
var ErrClosed = errors.New("the plugin host is closed")

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
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

// event records a lifecycle event in pn's Logs and the host log, unless
// Close is under way.
func (m *Manager) event(pn *panel, format string, args ...any) {
	if m.ctx.Err() != nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	for _, line := range strings.Split(msg, "\n") { // Logs holds lines
		pn.logs.add("HOST", line)
	}
	m.logger.Printf("panel %q: %s", pn.ID, msg)
}

// States returns a snapshot of every enabled panel, in config order.
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
// can take up to startTimeout and then an Instance.Close; also when
// called again while a first call is still shutting them down.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.cancel() // each supervisor closes its own instance
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
