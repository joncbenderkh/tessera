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
	"sync"
	"time"

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

	// For Restarting and Crashed: why the plugin failed, how its process
	// ended if it ran, and how many launches in a row have failed.
	Err         error
	ExitStatus  string
	Attempt     int
	MaxAttempts int
	RestartAt   time.Time // Restarting only
}

// Options tunes supervision. The zero value of a field means its
// default.
type Options struct {
	Backoff     time.Duration // delay after the first failure, doubled after each (1 s)
	MaxAttempts int           // failed launches in a row before giving up (5)
	ResetAfter  time.Duration // time live after which the count restarts (60 s)
	CallTimeout time.Duration // deadline on each call to a plugin (5 s)
}

func (o Options) withDefaults() Options {
	if o.Backoff == 0 {
		o.Backoff = time.Second
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = 5
	}
	if o.ResetAfter == 0 {
		o.ResetAfter = time.Minute
	}
	if o.CallTimeout == 0 {
		o.CallTimeout = callTimeout
	}
	return o
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
	inst    *Instance
	logs    *ring
	restart chan struct{} // a manual Restart; buffered, coalesced
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
	attempt := 0 // failed launches in a row
	for {
		m.update(pn, func(s *PanelState) {
			s.State, s.Info, s.RestartAt = Starting, nil, time.Time{}
		})
		m.event(pn, "starting plugin %s", pn.Plugin)
		inst, err := m.launch(pn)
		if m.ctx.Err() != nil {
			if inst != nil {
				inst.Close()
			}
			return
		}
		if err == nil {
			m.update(pn, func(s *PanelState) {
				s.State, s.Info, s.Err, s.ExitStatus = Live, inst.Info, nil, ""
				pn.inst = inst
			})
			m.event(pn, "plugin %s %s live", pn.Plugin, inst.Info.GetVersion())
			live := time.Now()
			var exit string
			select {
			case <-m.ctx.Done():
				inst.Close()
				return
			case <-pn.restart:
				m.event(pn, "restart requested")
				inst.Close()
				m.update(pn, func(*PanelState) { pn.inst = nil })
				attempt = 0
				continue
			case <-inst.Done():
				exit = awaitStatus(inst)
				inst.Close()
				m.update(pn, func(*PanelState) { pn.inst = nil })
			}
			if time.Since(live) >= m.opts.ResetAfter {
				attempt = 0
			}
			err = fmt.Errorf("plugin %q exited unexpectedly", pn.Plugin)
			m.update(pn, func(s *PanelState) { s.ExitStatus = exit })
			m.event(pn, "plugin exited: %s", orUnknown(exit))
		}

		attempt++
		if !Retryable(err) || attempt >= m.opts.MaxAttempts {
			m.update(pn, func(s *PanelState) { s.State, s.Err, s.Attempt = Crashed, err, attempt })
			if Retryable(err) {
				m.event(pn, "giving up after %d failed attempts: %v", attempt, err)
			} else {
				m.event(pn, "not restarting: %v", err)
			}
			select {
			case <-m.ctx.Done():
				return
			case <-pn.restart:
				m.event(pn, "restart requested")
				attempt = 0
				continue
			}
		}
		delay := m.opts.Backoff << (attempt - 1)
		m.update(pn, func(s *PanelState) {
			s.State, s.Err, s.Attempt, s.RestartAt = Restarting, err, attempt, time.Now().Add(delay)
		})
		m.event(pn, "%v; restarting in %v (attempt %d/%d failed)", err, delay, attempt, m.opts.MaxAttempts)
		select {
		case <-m.ctx.Done():
			return
		case <-pn.restart:
			m.event(pn, "restart requested")
			attempt = 0
		case <-time.After(delay):
		}
	}
}

// launch starts and configures pn's plugin. A Configure that misses its
// deadline counts as a hang: the plugin is shut down before anything
// else is called on it.
func (m *Manager) launch(pn *panel) (*Instance, error) {
	output := func(line string) {
		m.logger.Printf("[panel %s] %s", pn.ID, line)
		pn.logs.add("PLUGIN", line)
	}
	inst, err := Launch(m.ctx, m.dir, pn.Plugin, output, m.logger)
	if err != nil {
		return nil, err
	}
	if err := configure(m.ctx, inst, pn.cfg, m.opts.CallTimeout); err != nil {
		inst.Close()
		return nil, err
	}
	return inst, nil
}

// awaitStatus returns how inst's process ended, waiting briefly for the
// reap that tells.
func awaitStatus(inst *Instance) string {
	select {
	case <-inst.runner.reaped:
	case <-time.After(time.Second):
	}
	return inst.ExitStatus()
}

func orUnknown(s string) string {
	if s == "" {
		return "status unknown"
	}
	return s
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
	if s, ok := status.FromError(err); ok && s.Code() == codes.InvalidArgument {
		return permanent(fmt.Errorf("plugin %q rejected the settings: %s", p.Plugin, s.Message()))
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("plugin %q hung: Configure did not answer within %v", p.Plugin, timeout)
	}
	if err != nil {
		return fmt.Errorf("plugin %q: Configure: %w", p.Plugin, err)
	}
	return nil
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
	pn.logs.add("HOST", msg)
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

// Restart relaunches panel id's plugin now, at attempt 1: one that is
// live is shut down first, and one waiting to restart or crashed starts
// at once. It is the error tile's Restart button.
func (m *Manager) Restart(id string) error {
	pn := m.find(id)
	if pn == nil {
		return fmt.Errorf("no enabled panel %q", id)
	}
	select {
	case pn.restart <- struct{}{}:
	default: // one is already pending
	}
	return nil
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
// can take up to startTimeout; also when called again while a first call
// is still shutting them down.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.cancel() // each supervisor closes its own instance
		m.wg.Wait()
	})
}

// logLines is how many lines each panel's Logs keeps.
const logLines = 500

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
