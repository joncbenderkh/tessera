// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/ui"
)

func layoutConfig() config.Config {
	c := config.Default()
	c.Panels = []config.Panel{
		{ID: "a", Plugin: "clock", X: 0, Y: 0, W: 1, H: 1},
		{ID: "b", Plugin: "clock", X: 0, Y: 1, W: 1, H: 1},
	}
	return c
}

func TestChangeForStepMovesPastTheNextPanel(t *testing.T) {
	cfg, err := edited(layoutConfig(), changeFor(ui.Action{Kind: ui.ActionStep, Panel: "a", Step: image.Pt(0, 1)}, layoutConfig()))
	if err != nil {
		t.Fatal(err)
	}
	if a, b := cfg.Panels[0], cfg.Panels[1]; a.Y != 1 || b.Y != 0 {
		t.Errorf("a at row %d, b at %d, want 1 and 0", a.Y, b.Y)
	}
}

func TestChangeForAddsANewID(t *testing.T) {
	cfg, err := edited(layoutConfig(), changeFor(ui.Action{Kind: ui.ActionAdd, Plugin: "clock"}, layoutConfig()))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Panels[len(cfg.Panels)-1]; got.ID != "clock" || got.Plugin != "clock" {
		t.Errorf("added %q of %q, want clock", got.ID, got.Plugin)
	}
}

func TestChangeForAddRefusesATakenID(t *testing.T) {
	change := changeFor(ui.Action{Kind: ui.ActionAdd, Plugin: "clock"}, layoutConfig())
	file := layoutConfig()
	file.Panels = append(file.Panels, config.Panel{ID: "clock", Plugin: "notes", X: 1, W: 1, H: 1})
	if _, err := edited(file, change); err == nil {
		t.Error("added a panel whose id the file has for another plugin")
	}
}

// Shift and arrow keys resize from where the panel is when the change
// applies, after a move in the same frame.
func TestChangeForResizeAfterStep(t *testing.T) {
	cfg := layoutConfig()
	cfg.Grid.Columns = 2
	for _, a := range []ui.Action{
		{Kind: ui.ActionStep, Panel: "a", Step: image.Pt(1, 0)},
		{Kind: ui.ActionResize, Panel: "a", Step: image.Pt(0, 1)},
	} {
		var err error
		if cfg, err = edited(cfg, changeFor(a, cfg)); err != nil {
			t.Fatal(err)
		}
	}
	if a := cfg.Panels[0]; a.X != 1 || a.H != 2 {
		t.Errorf("a at column %d, %d rows tall, want 1 and 2", a.X, a.H)
	}
}

func TestChangeForIgnoresOtherActions(t *testing.T) {
	for _, k := range []ui.ActionKind{ui.ActionConfigure, ui.ActionRestart, ui.ActionLogs, ui.ActionEvent} {
		if changeFor(ui.Action{Kind: k, Panel: "a"}, layoutConfig()) != nil {
			t.Errorf("kind %d is a layout change", k)
		}
	}
}

func TestLayoutSaverWritesInOrder(t *testing.T) {
	var mu sync.Mutex
	var order []int
	release := make(chan struct{})
	s := &layoutSaver{done: make(chan error, 1), invalidate: func() {},
		write: func(_ string, change layoutChange) error {
			<-release
			var c config.Config
			err := change(&c)
			mu.Lock()
			order = append(order, c.Grid.Columns)
			mu.Unlock()
			return err
		}}
	failed := errors.New("read-only")
	for i := range 3 {
		s.save(func(c *config.Config) error {
			c.Grid.Columns = i
			if i == 1 {
				return failed
			}
			return nil
		})
	}
	if n := len(s.since(0)); n != 3 {
		t.Errorf("%d changes, want 3", n)
	}
	close(release)
	s.wait()
	if !slices.Equal(order, []int{0, 1, 2}) {
		t.Errorf("written in order %v, want 0 1 2", order)
	}
	// The failed one is not applied to a config read again.
	if n := len(s.since(0)); n != 2 {
		t.Errorf("%d changes after the writes, want 2", n)
	}
	select {
	case err := <-s.done:
		if err != failed {
			t.Errorf("error %v, want %v", err, failed)
		}
	default:
		t.Error("the failed write was not reported")
	}
	s.save(func(*config.Config) error { t.Error("written after wait"); return nil })
	s.wait()
}

// With the file not watched, no reload replays changes, so none are
// kept for one.
func TestLayoutSaverUnwatched(t *testing.T) {
	s := &layoutSaver{done: make(chan error, 1), invalidate: func() {},
		write: func(string, layoutChange) error { return nil }}
	s.save(func(*config.Config) error { return nil })
	s.unwatch()
	s.save(func(*config.Config) error { return nil })
	s.wait()
	if n := len(s.since(0)); n != 0 {
		t.Errorf("%d changes kept, want none", n)
	}
}

func TestLayoutSaverKeepsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := layoutConfig()
	cfg.Grid.Columns = 6 // an edit made while tessera runs
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	s := &layoutSaver{path: path, done: make(chan error, 1), invalidate: func() {}}
	s.save(changeFor(ui.Action{Kind: ui.ActionDisable, Panel: "a"}, layoutConfig()))
	s.wait()
	select {
	case err := <-s.done:
		t.Fatal(err)
	default:
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Grid.Columns != 6 || got.Panels[0].IsEnabled() {
		t.Errorf("got %d columns, a enabled %t, want 6 and false", got.Grid.Columns, got.Panels[0].IsEnabled())
	}
}

// readConfig reads the config file as Watch does through s, with the
// number of the latest change it has.
func readConfig(t *testing.T, s *layoutSaver) (config.Config, int) {
	t.Helper()
	data, err := s.readFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return c, s.lastRead()
}

// A config read from the file comes with the number of the latest
// change it has, so the window applies only the later ones, even when
// a write lands between Watch's read and the window's.
func TestLayoutSaverReadsInStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, layoutConfig()); err != nil {
		t.Fatal(err)
	}
	// Each write waits for a release; the first is released already.
	release := make(chan struct{}, 1)
	release <- struct{}{}
	s := &layoutSaver{path: path, done: make(chan error, 1), invalidate: func() {}}
	s.write = func(path string, change layoutChange) error {
		<-release
		return config.Update(path, change)
	}
	disable := func(id string) layoutChange {
		return changeFor(ui.Action{Kind: ui.ActionDisable, Panel: id}, layoutConfig())
	}
	s.save(disable("a"))
	s.running.Wait()
	cfg, seen := readConfig(t, s)
	if seen != 1 || cfg.Panels[0].IsEnabled() {
		t.Errorf("read change %d, a enabled %t, want 1 and false", seen, cfg.Panels[0].IsEnabled())
	}
	s.save(disable("b"))
	if later := s.since(seen); len(later) != 1 {
		t.Fatalf("%d changes after %d, want 1", len(later), seen)
	}
	s.forget(seen)
	if n := len(s.since(0)); n != 1 {
		t.Errorf("%d changes kept after forget, want 1", n)
	}
	release <- struct{}{}
	s.wait()
	if _, seen := readConfig(t, s); seen != 2 || len(s.since(seen)) != 0 {
		t.Errorf("read change %d with %d after it, want 2 and none", seen, len(s.since(seen)))
	}
}

// A reload goes to the window with the number of the latest change the
// file had when Watch read it, and to the plugins with the changes
// saved since.
func TestReloadedPairsTheRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, layoutConfig()); err != nil {
		t.Fatal(err)
	}
	s := &layoutSaver{path: path, done: make(chan error, 1), invalidate: func() {}}
	stale, _ := readConfig(t, s) // read before the change is written
	s.save(changeFor(ui.Action{Kind: ui.ActionDisable, Panel: "a"}, layoutConfig()))
	s.wait()
	reloads := make(chan reload, 1)
	var applied config.Config
	reloaded(stale, nil, s, func(c config.Config) { applied = c }, reloads)
	if r := <-reloads; !r.valid || r.seen != 0 || !r.cfg.Panels[0].IsEnabled() {
		t.Errorf("reload of change %d, valid %t, a enabled %t, want 0, true and true", r.seen, r.valid, r.cfg.Panels[0].IsEnabled())
	}
	if applied.Panels[0].IsEnabled() {
		t.Error("the plugins run a enabled, without the change saved since")
	}

	// An error goes to the window alone.
	applied = config.Config{}
	reloaded(config.Config{}, errors.New("bad"), s, func(c config.Config) { applied = c }, reloads)
	if r := <-reloads; r.valid || r.err == nil {
		t.Errorf("reload valid %t, error %v, want an error alone", r.valid, r.err)
	}
	if applied.Panels != nil {
		t.Error("the plugins got a config")
	}
}

// Watch reads the file through the saver, so the change the host just
// saved comes back numbered and is not applied again.
func TestWatchReadsThroughTheSaver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, layoutConfig()); err != nil {
		t.Fatal(err)
	}
	from, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &layoutSaver{path: path, done: make(chan error, 1), invalidate: func() {}}
	reloads := make(chan reload, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- config.Watch(ctx, path, from, s.readFile, func(c config.Config, err error) {
			reloaded(c, err, s, func(config.Config) {}, reloads)
		})
	}()
	defer func() { cancel(); <-done }()
	s.save(changeFor(ui.Action{Kind: ui.ActionDisable, Panel: "a"}, layoutConfig()))
	select {
	case r := <-reloads:
		if !r.valid || r.seen != 1 || r.cfg.Panels[0].IsEnabled() {
			t.Errorf("reload of change %d, valid %t, a enabled %t, want 1, true and false", r.seen, r.valid, r.cfg.Panels[0].IsEnabled())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no reload")
	}
	s.wait()
}

// A reload forgets the changes the file had when read and replays the
// later ones on it.
func TestLayoutSaverReplay(t *testing.T) {
	s := &layoutSaver{done: make(chan error, 1), invalidate: func() {},
		write: func(string, layoutChange) error { return nil }}
	disable := func(id string) layoutChange {
		return changeFor(ui.Action{Kind: ui.ActionDisable, Panel: id}, layoutConfig())
	}
	s.save(disable("a"))
	s.save(disable("b"))
	s.wait()
	file := layoutConfig()
	off := false
	file.Panels[0].Enabled = &off // change 1 is in the file
	got := s.replay(reload{cfg: file, seen: 1, valid: true})
	if got.Panels[0].IsEnabled() || got.Panels[1].IsEnabled() {
		t.Errorf("a enabled %t, b enabled %t, want both disabled", got.Panels[0].IsEnabled(), got.Panels[1].IsEnabled())
	}
	if n := len(s.since(0)); n != 1 {
		t.Errorf("%d changes kept, want change 2 alone", n)
	}
}

func TestSameGrid(t *testing.T) {
	cfg := layoutConfig()
	cfg.Panels[1].Y = 5 // shown at row 1
	shown := layoutConfig()
	moved := layoutConfig()
	moved.Panels[0].X = 1
	for _, tc := range []struct {
		next config.Config
		want bool
	}{{cfg, true}, {shown, true}, {moved, false}} {
		if got := sameGrid(cfg, tc.next); got != tc.want {
			t.Errorf("sameGrid(%v) = %t, want %t", tc.next.Panels, got, tc.want)
		}
	}
}

// A change takes the reload waiting first, so Add picks an id the file
// has free, and the plugins and the file get the change on top of it.
func TestWindowConfigChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	file := layoutConfig()
	file.Panels = append(file.Panels, config.Panel{ID: "clock", Plugin: "clock", X: 1, Y: 0, W: 1, H: 1})
	file.Theme = config.ThemeLight
	if err := config.Save(path, file); err != nil {
		t.Fatal(err)
	}
	layouts := &layoutSaver{path: path, done: make(chan error, 1), invalidate: func() {}}
	reloads := make(chan reload, 1)
	reloads <- reload{cfg: file, valid: true}
	var applied []config.Config
	var added []string
	redraws := 0
	w := &windowConfig{
		cfg:      layoutConfig(),
		dark:     true,
		fileDark: true,
		reloads:  reloads,
		layouts:  layouts,
		themes:   &themeSaver{},
		toast:    &ui.Toaster{},
		apply:    func(c config.Config) { applied = append(applied, c) },
		redraw:   func() { redraws++ },
		added:    func(id string) { added = append(added, id) },
	}
	if !w.change(ui.Action{Kind: ui.ActionAdd, Plugin: "clock"}) {
		t.Fatal("Add changed nothing")
	}
	if !slices.Equal(added, []string{"clock-2"}) {
		t.Errorf("told of panels %v added, want [clock-2]: the id the reload left free", added)
	}
	ids := func(c config.Config) []string {
		var ids []string
		for _, p := range c.Panels {
			ids = append(ids, p.ID)
		}
		return ids
	}
	if got, want := strings.Join(ids(w.cfg), " "), "a b clock clock-2"; got != want {
		t.Errorf("panels %s, want %s", got, want)
	}
	if w.dark {
		t.Error("dark after a reload to the light theme")
	}
	if len(applied) != 2 || !reflect.DeepEqual(applied[1], w.cfg) {
		t.Errorf("applied %d configs, the last not the window's", len(applied))
	}
	if redraws == 0 {
		t.Error("no frame asked for")
	}

	// One that changes nothing shown is neither applied nor saved.
	applied = nil
	if w.change(ui.Action{Kind: ui.ActionEnable, Panel: "a"}) {
		t.Error("enabling a shown panel changed the grid")
	}
	if applied != nil {
		t.Errorf("applied %d configs", len(applied))
	}
	layouts.wait()
	saved, _ := readConfig(t, layouts)
	if got, want := strings.Join(ids(saved), " "), "a b clock clock-2"; got != want {
		t.Errorf("file has %s, want %s", got, want)
	}
	if layouts.last != 1 {
		t.Errorf("%d saves, want 1", layouts.last)
	}
}
