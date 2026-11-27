// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"

	"gioui.org/text"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// saveMu keeps the host's writes to the config file apart: Update gives
// up with ErrChanged if another write lands while it runs. Every call of
// config.Update holds it, as update and the layout saver do.
var saveMu sync.Mutex

// applyMu keeps a layout change's save and Apply together with the
// window taking any reload waiting, and a reload's queueing and Apply
// with the changes saved by then, so the plugins never run a config
// that lacks a change saved before it, or one older than they run.
var applyMu sync.Mutex

// update is config.Update, one at a time.
func update(path string, change func(*config.Config) error) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	return config.Update(path, change)
}

// layoutChange is a change to the layout from edit mode.
type layoutChange func(*config.Config) error

// edited is cfg with change applied, leaving cfg as it was.
func edited(cfg config.Config, change layoutChange) (config.Config, error) {
	cfg.Panels = slices.Clone(cfg.Panels)
	err := change(&cfg)
	return cfg, err
}

// changeFor is the layout change that a, from the grid or the edit bar,
// asks for of cfg, or nil if a is not one.
func changeFor(a ui.Action, cfg config.Config) layoutChange {
	switch a.Kind {
	case ui.ActionPlace:
		return func(c *config.Config) error { return c.Place(a.Panel, a.Cell.X, a.Cell.Y, a.Cell.W, a.Cell.H) }
	case ui.ActionStep:
		return func(c *config.Config) error { return c.Step(a.Panel, a.Step.X, a.Step.Y) }
	case ui.ActionResize:
		return func(c *config.Config) error { return c.Resize(a.Panel, a.Step.X, a.Step.Y) }
	case ui.ActionDisable, ui.ActionEnable:
		on := a.Kind == ui.ActionEnable
		return func(c *config.Config) error { return c.SetEnabled(a.Panel, on) }
	case ui.ActionAdd:
		// The id the window shows, which a panel added to the file
		// meanwhile may have taken: Add refuses it then. Panel, if
		// set, is the one picked already.
		id := a.Panel
		if id == "" {
			id = cfg.NewID(a.Plugin)
		}
		return func(c *config.Config) error { return c.Add(id, a.Plugin) }
	}
	return nil
}

// layoutSaver writes layout changes to the config file in the
// background, one at a time and in the order asked for, each through
// Update, so edits made to the file meanwhile are kept and a file that
// does not parse is left alone. Changes are numbered in that order; a
// config read from the file with read lacks those after the number it
// comes with, which the window applies to it again.
type layoutSaver struct {
	path       string
	done       chan error // a failed save's error
	invalidate func()
	write      func(path string, change layoutChange) error // config.Update if nil

	mu      sync.Mutex
	queue   []numbered // not written yet, oldest first; the first is being written
	log     []numbered // asked for and not failed, after the last config read had
	last    int        // the number of the latest change asked for
	written int        // the number of the latest change written or failed
	read    int        // the number of the latest change the file had when readFile last read it
	closed  bool
	// unwatched is set when the file is not watched: no reload comes
	// to replay the log on, so it is not kept.
	unwatched bool
	running   sync.WaitGroup
}

type numbered struct {
	n      int
	change layoutChange
}

func (s *layoutSaver) save(change layoutChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.last++
	c := numbered{s.last, change}
	if !s.unwatched {
		s.log = append(s.log, c)
	}
	s.queue = append(s.queue, c)
	if len(s.queue) > 1 {
		return // the goroutine writing takes it next
	}
	s.running.Add(1)
	go s.run()
}

func (s *layoutSaver) run() {
	defer s.running.Done()
	write := s.write
	if write == nil {
		write = func(path string, change layoutChange) error { return config.Update(path, change) }
	}
	s.mu.Lock()
	for len(s.queue) > 0 {
		c := s.queue[0]
		s.mu.Unlock()
		// Held until the change is counted as written, so read finds
		// the file and the count in step.
		saveMu.Lock()
		err := write(s.path, c.change)
		s.mu.Lock()
		saveMu.Unlock()
		s.queue = s.queue[1:]
		s.written = c.n
		if err != nil {
			// The window keeps it until the file is read again.
			s.log = slices.DeleteFunc(s.log, func(l numbered) bool { return l.n == c.n })
			select {
			case s.done <- err:
			default: // one error is enough to show
			}
			s.invalidate()
		}
	}
	s.mu.Unlock()
}

// readFile reads the config file at path, for Watch, between writes,
// and keeps the number of the latest change it has, which lastRead
// returns, so the config Watch reports from it comes with that number.
func (s *layoutSaver) readFile(path string) ([]byte, error) {
	saveMu.Lock()
	defer saveMu.Unlock()
	data, err := os.ReadFile(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read = s.written
	return data, err
}

// lastRead is the number of the latest change the file had when
// readFile last read it: a config read then lacks those after it.
func (s *layoutSaver) lastRead() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read
}

// since is the changes asked for after change number n, oldest first,
// which a config read with that number lacks.
func (s *layoutSaver) since(n int) []layoutChange {
	s.mu.Lock()
	defer s.mu.Unlock()
	var changes []layoutChange
	for _, c := range s.log {
		if c.n > n {
			changes = append(changes, c.change)
		}
	}
	return changes
}

// forget drops the changes up to number n, which the window's config
// now has from the file.
func (s *layoutSaver) forget(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = slices.DeleteFunc(s.log, func(c numbered) bool { return c.n <= n })
}

// watched reports whether the file is watched, as until unwatch.
func (s *layoutSaver) watched() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.unwatched
}

// unwatch stops keeping changes for reloads, which no longer come.
func (s *layoutSaver) unwatch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unwatched, s.log = true, nil
}

// wait returns once every change asked for is written. Changes asked
// for later are ignored, since the host is exiting.
func (s *layoutSaver) wait() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.running.Wait()
}

// replay is the config of reload r with the changes saved after the
// file was read, which are not in it; those before are forgotten.
func (s *layoutSaver) replay(r reload) config.Config {
	s.forget(r.seen)
	return withChanges(r.cfg, s.since(r.seen))
}

// sameGrid reports whether next shows the panels as cfg does, before or
// after compacting: a change to it changes nothing in the grid.
func sameGrid(cfg, next config.Config) bool {
	shown, _ := edited(cfg, func(c *config.Config) error { config.Compact(c.Panels); return nil })
	return reflect.DeepEqual(next.Panels, cfg.Panels) || reflect.DeepEqual(next.Panels, shown.Panels)
}

// withChanges is cfg with the changes in turn, skipping those that no
// longer apply, as to a panel removed from the file.
func withChanges(cfg config.Config, changes []layoutChange) config.Config {
	for _, c := range changes {
		if next, err := edited(cfg, c); err == nil {
			cfg = next
		}
	}
	return cfg
}

// reloaded hands a change Watch reported in the config file to the
// window, through reloads, and to the plugins, through apply. Watch
// reads the file with layouts.readFile, so a valid config comes with the
// number of the latest layout change it has, and the window applies the
// later ones; the plugins run it with them at once.
func reloaded(c config.Config, err error, layouts *layoutSaver, apply func(config.Config), reloads chan reload) {
	if err != nil {
		log.Print(reloadError(layouts.path, err, error.Error))
		queue(reloads, c, 0, reloadError(filepath.Base(layouts.path), err, brief))
		return
	}
	seen := layouts.lastRead()
	applyMu.Lock()
	defer applyMu.Unlock()
	// Queued first, so a frame that shows the new plugins' states lays
	// out the new config.
	queue(reloads, c, seen, nil)
	apply(withChanges(c, layouts.since(seen)))
}

// windowConfig is the config the window shows and what keeps it
// current: the reloads of the config file and the layout changes made
// in the window, which go to the plugins and the file.
type windowConfig struct {
	cfg       config.Config
	theme     config.Theme                     // the theme showing
	fileTheme config.Theme                     // the theme the config file held at the latest reload
	look      int                              // counts changes to the theme showing or its colours
	warned    []string                         // the contrast warnings reported for cfg's themes
	logf      func(format string, args ...any) // log.Printf if nil
	reloads   <-chan reload
	layouts   *layoutSaver
	themes    *themeSaver
	toast     *ui.Toaster
	apply     func(config.Config) // gives the plugins a config
	// reconfigure has the panel's plugin configured again from the
	// config, as after a trial whose settings were not saved.
	reconfigure func(id string)
	redraw      func()          // asks for a frame
	added       func(id string) // is told the id of each panel Add panel makes, if set
}

// takeReload takes a reload of the config file, if one is waiting.
func (w *windowConfig) takeReload() {
	applyMu.Lock()
	defer applyMu.Unlock()
	w.takeReloadLocked()
}

// takeReloadLocked is takeReload for a caller holding applyMu, so no
// reload is applied to the plugins meanwhile.
func (w *windowConfig) takeReloadLocked() {
	var r reload
	select {
	case r = <-w.reloads:
	default:
		return
	}
	// The frame under way, if any, drew the config before it.
	w.redraw()
	// Only the latest problem with the file shows, and none once it is
	// fixed.
	if r.err != nil {
		w.toast.Put(reloadToast, r.err.Error())
	} else {
		w.toast.Drop(reloadToast)
	}
	if !r.valid {
		return
	}
	w.cfg = w.layouts.replay(r)
	// Applied again, since a change the plugins got with the reload may
	// have failed to save since.
	w.apply(w.cfg)
	was := w.fileTheme
	w.fileTheme = w.cfg.Theme.OrDefault()
	w.setTheme(themeOnReload(w.theme, was, w.fileTheme, w.themes.pending(), w.cfg.Offers))
	w.warnContrast()
}

// setTheme shows theme t, with the colours cfg gives it; a reload
// that changes them is a new look even when t stays.
func (w *windowConfig) setTheme(t config.Theme) {
	w.theme = t
	w.look++
}

// themeLook is the theme to draw with, the theme showing in cfg's
// colours, and the name of the theme the header's toggle switches to
// and whether that one is dark.
func (w *windowConfig) themeLook(shaper *text.Shaper) (*theme.Theme, string, bool) {
	p, dark := w.cfg.Palette(w.theme)
	next := w.cfg.NextTheme(w.theme)
	_, nextDark := w.cfg.Palette(next)
	return &theme.Theme{Palette: p, Shaper: shaper, Dark: dark}, string(next), nextDark
}

// warnContrast reports the contrast warnings for cfg's themes, in the
// log and a toast, when they differ from those reported last.
func (w *windowConfig) warnContrast() {
	warnings := w.cfg.ContrastWarnings()
	if slices.Equal(warnings, w.warned) {
		return
	}
	logf := w.logf
	if logf == nil {
		logf = log.Printf
	}
	// The log gets what changed: new warnings, how many are left when
	// only some went, and the end of them all.
	added := false
	for _, line := range warnings {
		if !slices.Contains(w.warned, line) {
			logf("hard to read: %s", line)
			added = true
		}
	}
	if !added && len(warnings) > 0 {
		logf("%d theme colour warnings remain, as logged before", len(warnings))
	}
	w.warned = warnings
	if len(warnings) == 0 {
		logf("theme colours are readable again")
		w.toast.Drop(contrastToast)
		return
	}
	text := "Hard to read: " + warnings[0]
	if more := len(warnings) - 1; more > 0 {
		text += fmt.Sprintf(" (and %d more; see the log)", more)
	}
	w.toast.Put(contrastToast, text)
}

// contrastToast is the kind of toast that warns of theme colours hard
// to read.
const contrastToast = "contrast"

// change applies the layout change a asks for at once, and saves it,
// and reports whether it changed the grid shown. One that does not is
// not saved, since a save drops the file's comments. A reload waiting
// is taken first, and none applied until this change is, so the
// plugins are not given a config older than the one they run.
func (w *windowConfig) change(a ui.Action) bool {
	applyMu.Lock()
	defer applyMu.Unlock()
	w.takeReloadLocked()
	if a.Kind == ui.ActionAdd {
		a.Panel = w.cfg.NewID(a.Plugin) // after the reload, so it is free
	}
	c := changeFor(a, w.cfg)
	next, err := edited(w.cfg, c)
	if err != nil {
		w.toast.Show("Layout not changed: " + err.Error())
		return false
	}
	if sameGrid(w.cfg, next) {
		return false
	}
	w.cfg = next
	w.layouts.save(c)
	w.apply(w.cfg)
	if a.Kind == ui.ActionAdd && w.added != nil {
		w.added(a.Panel)
	}
	// The frame under way drew the old layout, and a change that starts
	// or stops no plugin brings no other frame.
	w.redraw()
	return true
}
