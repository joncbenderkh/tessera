// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"sync"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/op/paint"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// run draws the window until it is closed: the header over an empty
// content area, which the grid (#10) will fill, and the toast and
// dialog over both.
func run(window *app.Window, plugins *pluginhost.Manager, initial config.Theme, saver *themeSaver) error {
	go func() {
		for range plugins.Changed() {
			window.Invalidate()
		}
	}()
	shaper := theme.NewShaper(true)
	dark := initial != config.ThemeLight
	th := theme.New(dark, shaper)

	var (
		ops    op.Ops
		header ui.Header
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
			header.Layout(behind, th, ui.StatusOf(plugins.States()))
			toast.Layout(gtx, th)
			dialog.Layout(gtx, th)
			e.Frame(gtx.Ops)
		}
	}
}

// themeSaver writes the theme to the config file in the background. It
// rereads the file first, so edits made since tessera started are kept,
// and it never writes over a file it cannot parse or one that changes
// while it saves. Saves run one at a time, each writing the latest
// theme asked for.
type themeSaver struct {
	path       string
	done       chan error // a failed save's error
	invalidate func()

	mu      sync.Mutex // held while saving
	want    sync.Mutex // guards dark and closed
	dark    bool
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
	s.running.Add(1)
	go func() {
		defer s.running.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		s.want.Lock()
		dark := s.dark
		s.want.Unlock()
		if err := saveTheme(s.path, dark); err != nil {
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
