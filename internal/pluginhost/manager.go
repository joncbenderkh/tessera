// SPDX-License-Identifier: GPL-3.0-or-later

// Package pluginhost finds, launches, and shuts down the plugins behind
// the config's panels: one process per panel, spoken to over go-plugin's
// gRPC transport (see docs/protocol.md).
package pluginhost

import (
	"context"
	"fmt"
	"log"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/config"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// State is where a panel's plugin is in its lifecycle.
type State int

const (
	Starting State = iota // launching, describing, or configuring
	Ready                 // configured and running
	Failed                // could not be brought up; Err says why
)

func (s State) String() string {
	switch s {
	case Starting:
		return "starting"
	case Ready:
		return "ready"
	case Failed:
		return "failed"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// PanelState is a snapshot of one enabled panel.
type PanelState struct {
	ID, Plugin string
	State      State
	Err        error
	Info       *tesserav1.DescribeResponse
}

// Manager runs one plugin process per enabled panel.
type Manager struct {
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	changed   chan struct{}
	closeOnce sync.Once

	mu     sync.Mutex
	panels []*panel
	closed bool
}

type panel struct {
	PanelState
	inst *Instance
}

// Start launches the plugin of every enabled panel in cfg from dir and
// returns at once; States reports their progress. Plugin stderr and
// launch errors go to logger.
func Start(cfg config.Config, dir string, logger *log.Logger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{ctx: ctx, cancel: cancel, changed: make(chan struct{}, 1)}
	for _, p := range cfg.Panels {
		if !p.IsEnabled() {
			continue
		}
		pn := &panel{PanelState: PanelState{ID: p.ID, Plugin: p.Plugin, State: Starting}}
		m.panels = append(m.panels, pn)
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.bringUp(pn, p, dir, logger)
		}()
	}
	return m
}

func (m *Manager) bringUp(pn *panel, p config.Panel, dir string, logger *log.Logger) {
	output := func(line string) { logger.Printf("[panel %s] %s", p.ID, line) }
	inst, err := Launch(m.ctx, dir, p.Plugin, output, logger)
	if err == nil {
		err = configure(m.ctx, inst, p)
		if err != nil {
			inst.Close()
			inst = nil
		}
	}
	if err != nil {
		logger.Printf("panel %q: %v", p.ID, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		if inst != nil {
			// Close is waiting on wg, so it waits for this too.
			m.mu.Unlock()
			inst.Close()
			m.mu.Lock()
		}
		return
	}
	pn.inst = inst
	if err != nil {
		pn.State, pn.Err = Failed, err
	} else {
		pn.State, pn.Info = Ready, inst.Info
	}
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

func configure(ctx context.Context, inst *Instance, p config.Panel) error {
	settings, err := toStruct(p.Settings)
	if err != nil {
		return fmt.Errorf("plugin %q: settings: %w", p.Plugin, err)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err = inst.Client.Configure(ctx, &tesserav1.ConfigureRequest{
		PanelId:  p.ID,
		Settings: settings,
		Size:     &tesserav1.GridSize{Columns: uint32(p.W), Rows: uint32(p.H)},
	})
	if s, ok := status.FromError(err); ok && s.Code() == codes.InvalidArgument {
		return fmt.Errorf("plugin %q rejected the settings: %s", p.Plugin, s.Message())
	}
	if err != nil {
		return fmt.Errorf("plugin %q: Configure: %w", p.Plugin, err)
	}
	return nil
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

// Close stops launching and shuts every plugin down in parallel. It
// returns once all plugin processes are gone, including when called
// again while a first call is still shutting them down.
func (m *Manager) Close() { m.closeOnce.Do(m.close) }

func (m *Manager) close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	var running []*Instance
	for _, pn := range m.panels {
		if pn.inst != nil {
			running = append(running, pn.inst)
			pn.inst = nil
		}
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, inst := range running {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inst.Close()
		}()
	}
	wg.Wait()
	m.wg.Wait()
}
