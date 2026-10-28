// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"image"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// reload is the latest valid config Watch read since the window last
// looked, if any, and why a later change could not be used, if one
// could not, as the toast shows it.
type reload struct {
	cfg   config.Config
	seen  int // the number of the latest layout change cfg has
	valid bool
	err   error
}

// queue hands a change Watch reported to the window, folded into one
// the window has not read yet: it keeps the latest valid config, with
// seen, which Apply already runs, and the latest error, unless a valid
// config followed it. Only Watch's goroutine calls it, so the send
// never blocks.
func queue(reloads chan reload, c config.Config, seen int, err error) {
	var r reload
	select {
	case r = <-reloads:
	default:
	}
	if err == nil {
		r.cfg, r.seen, r.valid, r.err = c, seen, true, nil
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

// minWidth and minHeight are the smallest window, in dp; larger text
// needs a taller one.
const minWidth, minHeight unit.Dp = 360, 240

// minWindowHeight is the smallest window height, in dp, that leaves
// room for the header and a tile of the smallest size, px tall in all.
func minWindowHeight(gtx layout.Context, px int) unit.Dp {
	return max(minHeight, unit.Dp(math.Ceil(float64(px)/float64(gtx.Metric.PxPerDp))))
}

// run draws the window until it is closed: the header, the grid of
// cfg's panels below it, with the edit bar between them in edit mode,
// and the toast and dialog over them. Each reload replaces cfg; one that
// failed keeps the last good one. Edit mode lists the plugins in
// pluginDir, and layouts saves its changes.
func run(window *app.Window, plugins *pluginhost.Manager, pluginDir string, cfg config.Config, reloads <-chan reload, saver *themeSaver, layouts *layoutSaver) error {
	go func() {
		for range plugins.Changed() {
			window.Invalidate()
		}
	}()
	shaper := theme.NewShaper(true)
	var toast ui.Toaster
	w := &windowConfig{
		cfg:      cfg,
		dark:     cfg.Theme != config.ThemeLight,
		fileDark: cfg.Theme != config.ThemeLight,
		reloads:  reloads,
		layouts:  layouts,
		themes:   saver,
		toast:    &toast,
		apply:    plugins.Apply,
		redraw:   window.Invalidate,
	}
	dark := w.dark // the theme th has
	th := theme.New(dark, shaper)

	shortest := minHeight // the window's least height, which large text raises
	mode := app.Windowed
	var (
		ops       op.Ops
		header    ui.Header
		grid      ui.Grid
		bar       ui.EditBar
		dialog    ui.Dialog
		editing   bool
		installed []string // the plugins Add panel offers
	)
	// listPlugins lists the plugins installed, for Add panel….
	listPlugins := func() {
		var err error
		if installed, err = pluginhost.List(pluginDir); err != nil {
			toast.Show("Installed plugins not listed: " + err.Error())
		}
	}
	for {
		switch e := window.Event().(type) {
		case app.ConfigEvent:
			// Wayland drops a minimum size set while the window is not
			// windowed, so it is set again on the way back.
			if e.Config.Mode == app.Windowed && mode != app.Windowed {
				window.Option(app.MinSize(minWidth, shortest))
			}
			mode = e.Config.Mode
		case app.DestroyEvent:
			saver.wait()
			layouts.wait()
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			select {
			case err := <-saver.done:
				toast.Show("Theme not saved; it applies until tessera restarts: " + err.Error())
			default:
			}
			select {
			case err := <-layouts.done:
				toast.Show("Layout not saved; it applies until the config file changes or tessera restarts: " + err.Error())
			default:
			}
			w.takeReload()
			behind := gtx
			if dialog.IsOpen() {
				behind = gtx.Disabled()
			}
			if header.Theme.Clicked(behind) {
				w.dark = !w.dark
				saver.save(w.dark)
			}
			if header.Edit.Clicked(behind) {
				editing = !editing
				if editing {
					listPlugins()
				}
			}
			if id, ok := toast.Acted(behind); ok {
				w.change(ui.Action{Kind: ui.ActionEnable, Panel: id})
			}
			if !editing {
				bar.Close()
			}
			header.Edit.Toggle, header.Edit.Pressed = true, editing
			grid.Edit = editing
			if w.dark != dark {
				dark = w.dark
				th = theme.New(dark, shaper)
			}
			paint.Fill(gtx.Ops, th.Bg)
			states := plugins.States()
			head := header.Layout(behind, th, ui.StatusOf(states))
			if h := minWindowHeight(gtx, head.Size.Y+grid.MinHeight(gtx, th)); h != shortest {
				shortest = h
				window.Option(app.MinSize(minWidth, shortest))
			}
			top := head.Size.Y
			var actions []ui.Action
			if editing {
				bgtx := behind
				bgtx.Constraints = layout.Constraints{Max: image.Pt(gtx.Constraints.Max.X, max(gtx.Constraints.Max.Y-top, 0))}
				st := op.Offset(image.Pt(0, top)).Push(gtx.Ops)
				adding := bar.Adding()
				dims, chosen := bar.Layout(bgtx, th, w.cfg, installed, grid.EditHint())
				st.Pop()
				if bar.MenuOpened() {
					grid.CloseMenus() // one menu at a time
				}
				if bar.Adding() && !adding {
					// Plugins installed since edit mode began show next frame.
					listPlugins()
					gtx.Execute(op.InvalidateCmd{})
				}
				top += dims.Size.Y
				actions = append(actions, chosen...)
			}
			content := behind
			content.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, max(gtx.Constraints.Max.Y-top, 0)))
			st := op.Offset(image.Pt(0, top)).Push(gtx.Ops)
			_, chosen := grid.Layout(content, th, w.cfg, states)
			st.Pop()
			if grid.MenuOpened() && editing {
				bar.Close() // one menu at a time
				gtx.Execute(op.InvalidateCmd{})
			}
			actions = append(actions, chosen...)
			for _, a := range actions {
				if changeFor(a, w.cfg) != nil {
					switch changed := w.change(a); {
					case a.Kind == ui.ActionEnable:
						toast.DropAction(a.Panel) // nothing left to undo
					case changed && a.Kind == ui.ActionDisable:
						toast.ShowAction("Panel "+a.Panel+" disabled", "Undo", a.Panel)
					}
					continue
				}
				if act(a, plugins, states, &grid, &toast, &dialog) {
					// A scroll range the plugin's queue refused goes
					// again in a moment.
					gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(scrollRetry)})
				}
			}
			// Deferred after the tiles' popovers, which are deferred too, so
			// the toast and the dialog stay on top of them.
			over := op.Record(gtx.Ops)
			toast.Inert = dialog.IsOpen()
			toast.Layout(gtx, th)
			dialog.Layout(gtx, th)
			op.Defer(gtx.Ops, over.Stop())
			e.Frame(gtx.Ops)
		}
	}
}

// outcome is what the host does about an event its plugin did not get.
type outcome struct {
	resend   bool   // a scroll range to send again shortly
	unselect bool   // an activation not to show as done
	toast    string // what to tell the user
	logged   error  // what to log
}

// eventOutcome is the outcome of sending ev, which HandleEvent answered
// with err. A busy plugin gets a scroll range again soon; an activation
// it did not get loses its highlight, and the user hears so if the
// plugin was busy. A panel that is not live is about to get a new view,
// so only other errors are logged.
func eventOutcome(ev *tesserav1.HandleEventRequest, err error) outcome {
	if err == nil {
		return outcome{}
	}
	busy := errors.Is(err, pluginhost.ErrBusy)
	var o outcome
	switch {
	case ev.GetScroll() != nil:
		o.resend = busy
	case ev.GetActivate() != nil:
		o.unselect = true
		if busy {
			o.toast = "Panel busy; try again"
		}
	}
	if !busy && !errors.Is(err, pluginhost.ErrNotLive) {
		o.logged = err
	}
	return o
}

// scrollRetry is how soon a scroll range refused by a busy plugin's
// queue is sent again.
const scrollRetry = 100 * time.Millisecond

// act carries out what the user asked for from a tile, and reports
// whether a frame soon should send a refused scroll range again.
func act(a ui.Action, plugins *pluginhost.Manager, states []pluginhost.PanelState, grid *ui.Grid, toast *ui.Toaster, dialog *ui.Dialog) bool {
	switch a.Kind {
	case ui.ActionConfigure:
		toast.Show("Configuring panels is not available yet")
	case ui.ActionRestart:
		switch err := plugins.Restart(a.Panel); {
		case errors.Is(err, pluginhost.ErrStarting):
			toast.Show("Already restarting")
		case err != nil:
			toast.Show("Not restarted: " + err.Error())
		}
	case ui.ActionEvent:
		o := eventOutcome(a.Event, plugins.HandleEvent(a.Panel, a.Event))
		if o.resend {
			grid.Unsent(a.Panel, a.Event.GetScroll().GetWidgetId())
		}
		if o.unselect {
			grid.Unselect(a.Panel, a.Event)
		}
		if o.toast != "" {
			toast.Show(o.toast)
		}
		if o.logged != nil {
			log.Printf("panel %s: event not sent: %v", a.Panel, o.logged)
		}
		return o.resend
	case ui.ActionLogs:
		plugin := a.Panel
		for _, s := range states {
			if s.ID == a.Panel {
				plugin = s.Plugin
			}
		}
		dialog.Open("Logs: "+a.Panel, "Plugin "+plugin+", newest last", strings.Join(plugins.Logs(a.Panel), "\n"))
	}
	return false
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
	return update(path, func(c *config.Config) error {
		c.Theme = config.ThemeLight
		if dark {
			c.Theme = config.ThemeDark
		}
		return nil
	})
}
