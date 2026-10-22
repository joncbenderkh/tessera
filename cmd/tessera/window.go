// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"image"
	"strings"
	"sync"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// reload is the config file as Watch read it after a change, or why it
// could not be used.
type reload struct {
	cfg config.Config
	err error
}

// run draws the window until it is closed: the header, the grid of
// cfg's panels below it, and the toast and dialog over both. Each
// reload replaces cfg; one that failed keeps the last good one.
func run(window *app.Window, plugins *pluginhost.Manager, cfg config.Config, reloads <-chan reload, saver *themeSaver) error {
	go func() {
		for range plugins.Changed() {
			window.Invalidate()
		}
	}()
	shaper := theme.NewShaper(true)
	dark := cfg.Theme != config.ThemeLight
	th := theme.New(dark, shaper)

	var (
		ops    op.Ops
		header ui.Header
		grid   ui.Grid
		toast  ui.Toaster
		dialog ui.Dialog
	)
	for {
		switch e := window.Event().(type) {
		case app.DestroyEvent:
			saver.wait()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			select {
			case err := <-saver.done:
				toast.Show("Theme not saved; it applies until tessera restarts: " + err.Error())
			default:
			}
			select {
			case r := <-reloads:
				if r.err != nil {
					toast.Show("config.yaml not applied: " + r.err.Error())
					break
				}
				cfg = r.cfg
				if d := cfg.Theme != config.ThemeLight; d != dark {
					dark = d
					th = theme.New(dark, shaper)
				}
			default:
			}
			behind := gtx
			if dialog.IsOpen() {
				behind = gtx.Disabled()
			}
			if header.Theme.Clicked(behind) {
				dark = !dark
				th = theme.New(dark, shaper)
				saver.save(dark)
			}
			// Edit layout stays unpressed until edit mode exists (#15).
			if header.Edit.Clicked(behind) {
				toast.Show("Layout editing is not available yet")
			}
			paint.Fill(gtx.Ops, th.Bg)
			states := plugins.States()
			head := header.Layout(behind, th, ui.StatusOf(states))
			content := behind
			content.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, max(gtx.Constraints.Max.Y-head.Size.Y, 0)))
			st := op.Offset(image.Pt(0, head.Size.Y)).Push(gtx.Ops)
			_, actions := grid.Layout(content, th, cfg, states)
			st.Pop()
			for _, a := range actions {
				act(a, plugins, states, &toast, &dialog)
			}
			toast.Layout(gtx, th)
			dialog.Layout(gtx, th)
			e.Frame(gtx.Ops)
		}
	}
}

// act carries out what the user asked for from a tile.
func act(a ui.Action, plugins *pluginhost.Manager, states []pluginhost.PanelState, toast *ui.Toaster, dialog *ui.Dialog) {
	switch a.Kind {
	case ui.ActionConfigure:
		toast.Show("Configuring panels is not available yet")
	case ui.ActionDisable:
		toast.Show("Disabling panels is not available yet")
	case ui.ActionRestart:
		switch err := plugins.Restart(a.Panel); {
		case errors.Is(err, pluginhost.ErrStarting):
			toast.Show("Already restarting")
		case err != nil:
			toast.Show("Not restarted: " + err.Error())
		}
	case ui.ActionLogs:
		plugin := a.Panel
		for _, s := range states {
			if s.ID == a.Panel {
				plugin = s.Plugin
			}
		}
		dialog.Open("Logs: "+a.Panel, "Plugin "+plugin+", newest last", strings.Join(plugins.Logs(a.Panel), "\n"))
	}
}

// themeSaver writes the theme to the config file in the background. It
// rereads the file first, so edits made since tessera started are kept,
// and it never writes over a file it cannot parse or one that changes
// while it saves. Saves run one at a time, each writing the latest
// theme asked for; a failure is reported only if no later save was asked
// for, since that one replaces it.
type themeSaver struct {
	path       string
	done       chan error // a failed save's error
	invalidate func()
	write      func(path string, dark bool) error // saveTheme if nil

	mu      sync.Mutex // held while saving
	want    sync.Mutex // guards dark, asked, handled and closed
	dark    bool
	asked   int // saves asked for so far
	handled int // saves asked for when the latest write began
	closed  bool
	running sync.WaitGroup
}

// wait returns once every save asked for has finished. Saves asked for
// later are ignored, since the host is exiting.
func (s *themeSaver) wait() {
	s.want.Lock()
	s.closed = true
	s.want.Unlock()
	s.running.Wait()
}

func (s *themeSaver) save(dark bool) {
	s.want.Lock()
	defer s.want.Unlock()
	if s.closed {
		return
	}
	s.dark = dark
	s.asked++
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		s.want.Lock()
		dark, asked := s.dark, s.asked
		if asked == s.handled {
			// An earlier goroutine already wrote this theme.
			s.want.Unlock()
			return
		}
		s.handled = asked
		s.want.Unlock()
		write := s.write
		if write == nil {
			write = saveTheme
		}
		err := write(s.path, dark)
		s.want.Lock()
		superseded := s.asked != asked
		s.want.Unlock()
		if err != nil && !superseded {
			select {
			case s.done <- err:
			default: // one error is enough to show
			}
			s.invalidate()
		}
	}()
}

func saveTheme(path string, dark bool) error {
	return config.Update(path, func(c *config.Config) {
		c.Theme = config.ThemeLight
		if dark {
			c.Theme = config.ThemeDark
		}
	})
}
