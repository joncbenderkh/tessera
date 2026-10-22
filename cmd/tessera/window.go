// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
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

// reload is the latest valid config Watch read since the window last
// looked, if any, and why a later change could not be used, if one
// could not, as the toast shows it.
type reload struct {
	cfg   config.Config
	valid bool
	err   error
}

// queue hands a change Watch reported to the window, folded into one
// the window has not read yet: it keeps the latest valid config, which
// Apply already runs, and the latest error, unless a valid config
// followed it. Only Watch's goroutine calls it, so the send never
// blocks.
func queue(reloads chan reload, c config.Config, err error) {
	var r reload
	select {
	case r = <-reloads:
	default:
	}
	if err == nil {
		r.cfg, r.valid, r.err = c, true, nil
	} else {
		r.err = err
	}
	reloads <- r
}

// reloadToast is the kind of toast that reports a problem with the
// config file.
const reloadToast = "reload"

// reloadError says what err from Watch means for the config file name,
// with detail giving its text: an edit was not applied, or changes may
// be missed.
func reloadError(name string, err error, detail func(error) string) error {
	if werr := (*config.WatchError)(nil); errors.As(err, &werr) {
		return fmt.Errorf("changes to %s may be missed: %s", name, detail(werr.Err))
	}
	return fmt.Errorf("%s not applied: %s", name, detail(err))
}

// brief is err's first problem, on one line, and how many more there
// are: a toast has no room for the list Validate gives, one per line,
// nor for a YAML error's excerpt of the file. The log has them in full.
func brief(err error) string {
	first, more := err, 0
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		if errs := joined.Unwrap(); len(errs) > 0 {
			first, more = errs[0], len(errs)-1
		}
	}
	line, _, cut := strings.Cut(first.Error(), "\n")
	switch {
	case more > 0:
		return fmt.Sprintf("%s (and %d more; see the log)", line, more)
	case cut:
		return line + " (see the log)"
	}
	return line
}

// themeOnReload is the theme to show, dark or not, after a reload
// changes the file's theme from was to now. Only a change in the file
// counts, so an edit elsewhere in it keeps a theme whose save failed,
// and a save of the host's own still pending means the file is about
// to hold the theme shown.
func themeOnReload(shown, was, now, saving bool) bool {
	if now == was || saving {
		return shown
	}
	return now
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
	fileDark := dark // the theme config.yaml held at the latest reload
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
				// Only the latest problem with the file shows, and none
				// once it is fixed.
				if r.err != nil {
					toast.Put(reloadToast, r.err.Error())
				} else {
					toast.Drop(reloadToast)
				}
				if !r.valid {
					break
				}
				cfg = r.cfg
				was := fileDark
				fileDark = cfg.Theme != config.ThemeLight
				if d := themeOnReload(dark, was, fileDark, saver.pending()); d != dark {
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
			// Deferred after the tiles' popovers, which are deferred too, so
			// the toast and the dialog stay on top of them.
			over := op.Record(gtx.Ops)
			toast.Layout(gtx, th)
			dialog.Layout(gtx, th)
			op.Defer(gtx.Ops, over.Stop())
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
	want    sync.Mutex // guards dark, asked, handled, written and closed
	dark    bool
	asked   int // saves asked for so far
	handled int // saves asked for when the latest write began
	written int // saves asked for when the latest write ended
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

// pending reports whether a save asked for has not been written yet.
func (s *themeSaver) pending() bool {
	s.want.Lock()
	defer s.want.Unlock()
	return s.asked != s.written
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
		s.written = asked
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
