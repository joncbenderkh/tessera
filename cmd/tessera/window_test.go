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
	if err := saveTheme(path, false); err != nil {
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

func TestSaveThemeLeavesABrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	broken := []byte("version: 1\ngrid: [\n")
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveTheme(path, true); err == nil {
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
	s.save(false)
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
	s.save(false)
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
	s.write = func(string, bool) error {
		calls++
		if calls == 1 {
			close(started)
			<-release
			return config.ErrChanged
		}
		return nil
	}
	s.save(false)
	<-started
	s.save(true)
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
	s.write = func(string, bool) error {
		writes++
		return nil
	}
	// Both saves are asked for before either goroutine starts writing.
	s.mu.Lock()
	s.save(false)
	s.save(true)
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
	queue(reloads, valid, nil)
	queue(reloads, config.Config{}, errors.New("line 3: bad"))
	r := <-reloads
	if !r.valid || r.cfg.Theme != config.ThemeLight || r.err == nil {
		t.Errorf("after a valid then an invalid change: %+v, want the valid config and the error", r)
	}

	queue(reloads, config.Config{}, errors.New("line 3: bad"))
	queue(reloads, valid, nil)
	if r := <-reloads; !r.valid || r.err != nil {
		t.Errorf("after an invalid then a valid change: %+v, want only the valid config", r)
	}
}

func TestThemeOnReload(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		shown, was, now, saving bool
		want                    bool
	}{
		{"the file's theme changed", false, false, true, false, true},
		{"an edit elsewhere keeps a toggle whose save failed", true, false, false, false, true},
		{"a pending save wins over the file", true, true, false, true, true},
		{"the host's own save arrives", true, false, true, false, true},
	} {
		if got := themeOnReload(tt.shown, tt.was, tt.now, tt.saving); got != tt.want {
			t.Errorf("%s: dark %t, want %t", tt.name, got, tt.want)
		}
	}
}

func TestThemeSaverPending(t *testing.T) {
	release := make(chan struct{})
	s := &themeSaver{done: make(chan error, 1), invalidate: func() {},
		write: func(string, bool) error { <-release; return nil }}
	if s.pending() {
		t.Fatal("pending before any save")
	}
	s.save(true)
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
