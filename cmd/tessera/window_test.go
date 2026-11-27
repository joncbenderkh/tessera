// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

func TestSaveThemeKeepsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	edited := config.Default()
	edited.Grid.Columns = 6 // an edit made while tessera runs
	if err := config.Save(path, edited); err != nil {
		t.Fatal(err)
	}
	if err := saveTheme(path, config.ThemeLight); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != config.ThemeLight || got.Grid.Columns != 6 {
		t.Fatalf("got theme %q and %d columns, want light and 6", got.Theme, got.Grid.Columns)
	}
}

// A custom theme is saved by name, and one the file has dropped since
// is refused rather than written as a theme the file does not define.
func TestSaveThemeCustom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	c := config.Default()
	c.Themes = map[config.Theme]config.ThemeColours{"dusk": {Base: config.ThemeLight, Bg: "#fdf6e3"}}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	if err := saveTheme(path, "dusk"); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != "dusk" || got.Themes["dusk"].Bg != "#fdf6e3" {
		t.Fatalf("got theme %q and themes %v, want dusk kept", got.Theme, got.Themes)
	}
	if err := saveTheme(path, "gone"); !errors.Is(err, errThemeDropped) {
		t.Fatalf("a theme the file has dropped: %v, want errThemeDropped", err)
	}
	if again, err := config.Load(path); err != nil || again.Theme != "dusk" {
		t.Fatalf("after the refused save: theme %q, %v; want dusk", again.Theme, err)
	}
}

func TestSaveThemeLeavesABrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	broken := []byte("version: 1\ngrid: [\n")
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveTheme(path, config.ThemeDark); err == nil {
		t.Fatal("saving over a file that does not parse succeeded")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(broken) {
		t.Fatalf("the file was rewritten: %q", data)
	}
}

func TestThemeSaverWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatal(err)
	}
	s := themeSaver{path: path, done: make(chan error, 1), invalidate: func() {}}
	s.save(config.ThemeLight)
	s.wait()
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != config.ThemeLight {
		t.Fatalf("theme %q after wait, want light", got.Theme)
	}
}

func TestThemeSaverIgnoresSavesAfterWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatal(err)
	}
	s := themeSaver{path: path, done: make(chan error, 1), invalidate: func() {}}
	s.wait()
	s.save(config.ThemeLight)
	s.running.Wait()
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != "" {
		t.Fatalf("a save after wait wrote theme %q", got.Theme)
	}
}

func TestThemeSaverDropsASupersededError(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	s := themeSaver{done: make(chan error, 1), invalidate: func() {}}
	s.write = func(string, config.Theme) error {
		calls++
		if calls == 1 {
			close(started)
			<-release
			return config.ErrChanged
		}
		return nil
	}
	s.save(config.ThemeLight)
	<-started
	s.save(config.ThemeDark)
	close(release)
	s.wait()
	select {
	case err := <-s.done:
		t.Fatalf("reported %v, which a later save replaced", err)
	default:
	}
}

func TestThemeSaverWritesQueuedSavesOnce(t *testing.T) {
	writes := 0
	s := themeSaver{done: make(chan error, 1), invalidate: func() {}}
	s.write = func(string, config.Theme) error {
		writes++
		return nil
	}
	// Both saves are asked for before either goroutine starts writing.
	s.mu.Lock()
	s.save(config.ThemeLight)
	s.save(config.ThemeDark)
	s.mu.Unlock()
	s.wait()
	if writes != 1 {
		t.Fatalf("%d writes, want 1", writes)
	}
}

// An invalid edit queued behind a valid one must not hide it: Apply
// already runs the valid config, so the grid has to show it too.
func TestQueueKeepsValidConfig(t *testing.T) {
	reloads := make(chan reload, 1)
	valid := config.Config{Version: 1, Theme: config.ThemeLight}
	queue(reloads, valid, 0, nil)
	queue(reloads, config.Config{}, 0, errors.New("line 3: bad"))
	r := <-reloads
	if !r.valid || r.cfg.Theme != config.ThemeLight || r.err == nil {
		t.Errorf("after a valid then an invalid change: %+v, want the valid config and the error", r)
	}

	queue(reloads, config.Config{}, 0, errors.New("line 3: bad"))
	queue(reloads, valid, 0, nil)
	if r := <-reloads; !r.valid || r.err != nil {
		t.Errorf("after an invalid then a valid change: %+v, want only the valid config", r)
	}
}

func TestThemeOnReload(t *testing.T) {
	const dark, light, dusk = config.ThemeDark, config.ThemeLight, config.Theme("dusk")
	all := func(config.Theme) bool { return true }
	noDusk := func(t config.Theme) bool { return t != dusk }
	for _, tt := range []struct {
		name            string
		shown, was, now config.Theme
		saving          bool
		offers          func(config.Theme) bool
		want            config.Theme
	}{
		{"the file's theme changed", light, light, dark, false, all, dark},
		{"an edit elsewhere keeps a toggle whose save failed", dark, light, light, false, all, dark},
		{"a pending save wins over the file", dusk, dark, light, true, all, dusk},
		{"the host's own save arrives", dark, light, dark, false, all, dark},
		{"a custom theme removed gives way to the file's", dusk, light, light, false, noDusk, light},
		{"even with a save pending", dusk, light, light, true, noDusk, light},
	} {
		if got := themeOnReload(tt.shown, tt.was, tt.now, tt.saving, tt.offers); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

// A theme the file has dropped is reported as not saved only when the
// file is not watched; when it is, the reload that dropped it shows the
// file's theme.
func TestThemeSaverDroppedTheme(t *testing.T) {
	for _, watched := range []bool{true, false} {
		s := themeSaver{done: make(chan error, 1), invalidate: func() {},
			write:   func(string, config.Theme) error { return errThemeDropped },
			watched: func() bool { return watched }}
		s.save("dusk")
		s.wait()
		select {
		case err := <-s.done:
			if watched {
				t.Errorf("watched: reported %v", err)
			}
			var failed *themeSaveError
			if !errors.As(err, &failed) || failed.theme != "dusk" || !errors.Is(err, errThemeDropped) {
				t.Errorf("reported %#v, want dusk's errThemeDropped", err)
			}
		default:
			if !watched {
				t.Error("not watched: the theme not saved was not reported")
			}
		}
	}
}

func TestNewSaversWatched(t *testing.T) {
	saver, layouts := newSavers(filepath.Join(t.TempDir(), "config.yaml"), func() {})
	if !saver.watched() {
		t.Fatal("not watched before unwatch")
	}
	layouts.unwatch()
	if saver.watched() {
		t.Error("the theme saver still reads the file as watched after unwatch")
	}
}

func TestThemeSaverPending(t *testing.T) {
	release := make(chan struct{})
	s := &themeSaver{done: make(chan error, 1), invalidate: func() {},
		write: func(string, config.Theme) error { <-release; return nil }}
	if s.pending() {
		t.Fatal("pending before any save")
	}
	s.save(config.ThemeDark)
	if !s.pending() {
		t.Error("not pending while writing")
	}
	close(release)
	s.wait()
	if s.pending() {
		t.Error("pending after the write")
	}
}

func TestBrief(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want string
	}{
		{errors.New("grid.columns must be from 1 to 24"), "grid.columns must be from 1 to 24"},
		{errors.Join(errors.New("panel a does not fit"), errors.New("panel b does not fit"), errors.New("panel c does not fit")),
			"panel a does not fit (and 2 more; see the log)"},
		{fmt.Errorf("parsing: %w", errors.Join(errors.New("one"))), "one"},
		{errors.New("[3:1] unexpected key\n>  3 | x: 1\n       ^"), "[3:1] unexpected key (see the log)"},
	} {
		if got := brief(tt.err); got != tt.want {
			t.Errorf("brief(%q) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestReloadError(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want string
	}{
		{errors.New("grid.columns must be from 1 to 24"), "config.yaml not applied: grid.columns must be from 1 to 24"},
		{&config.WatchError{Err: errors.New("fsnotify: queue or buffer overflow")},
			"changes to config.yaml may be missed: fsnotify: queue or buffer overflow"},
	} {
		if got := reloadError("config.yaml", tt.err, brief).Error(); got != tt.want {
			t.Errorf("reloadError(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestMinWindowHeight(t *testing.T) {
	gtx := layout.Context{Metric: unit.Metric{PxPerDp: 2, PxPerSp: 2}}
	for _, tt := range []struct {
		px   int
		want unit.Dp
	}{{100, minHeight}, {1000, 500}, {1001, 501}} {
		if got := minWindowHeight(gtx, tt.px); got != tt.want {
			t.Errorf("minWindowHeight(%d px) = %v, want %v", tt.px, got, tt.want)
		}
	}
}

func TestEventOutcome(t *testing.T) {
	scroll := &tesserav1.HandleEventRequest{Event: &tesserav1.HandleEventRequest_Scroll{Scroll: &tesserav1.ScrollEvent{WidgetId: "w"}}}
	activate := &tesserav1.HandleEventRequest{Event: &tesserav1.HandleEventRequest_Activate{Activate: &tesserav1.ActivateEvent{ActionId: "x"}}}
	other := errors.New("no enabled panel")
	for _, tt := range []struct {
		name string
		ev   *tesserav1.HandleEventRequest
		err  error
		want outcome
	}{
		{"sent", scroll, nil, outcome{}},
		{"busy scroll", scroll, pluginhost.ErrBusy, outcome{resend: true}},
		{"scroll not live", scroll, pluginhost.ErrNotLive, outcome{}},
		{"busy activation", activate, pluginhost.ErrBusy, outcome{unselect: true, toast: "Panel busy; try again"}},
		{"activation not live", activate, pluginhost.ErrNotLive, outcome{unselect: true}},
		{"activation failed", activate, other, outcome{unselect: true, logged: other}},
		{"scroll failed", scroll, other, outcome{logged: other}},
	} {
		if got := eventOutcome(tt.ev, tt.err); got != tt.want {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}
