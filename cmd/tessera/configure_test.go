// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui"
)

func configureWindow(t *testing.T) (*windowConfig, *layoutSaver, *[]config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, layoutConfig()); err != nil {
		t.Fatal(err)
	}
	layouts := &layoutSaver{path: path, done: make(chan error, 1), invalidate: func() {}}
	var applied []config.Config
	w := &windowConfig{
		cfg:     layoutConfig(),
		reloads: make(chan reload, 1),
		layouts: layouts,
		themes:  &themeSaver{},
		toast:   &ui.Toaster{},
		apply:   func(c config.Config) { applied = append(applied, c) },
		redraw:  func() {},
		logf:    t.Logf,
	}
	return w, layouts, &applied
}

func TestSettingsTried(t *testing.T) {
	utc := config.Settings{"timezone": "UTC"}
	for _, tc := range []struct {
		name  string
		err   error
		saved bool
	}{
		{"taken", nil, true},
		{"not live", pluginhost.ErrNotLive, true},
		{"schema", &config.SettingsError{Problems: []config.Problem{{Path: []string{"timezone"}, Message: "bad"}}}, false},
		{"rejected", &pluginhost.RejectedError{Plugin: "clock", Message: "timezone: unknown"}, false},
		{"hung", errors.New("plugin hung"), false},
		{"interrupted", pluginhost.ErrInterrupted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, layouts, applied := configureWindow(t)
			var d ui.ConfigDialog
			d.Open("a", "clock", `{"type": "object", "properties": {"timezone": {"type": "string"}}}`, nil)
			w.settingsTried(&d, tried{panel: "a", plugin: "clock", settings: utc, err: tc.err})
			if d.IsOpen() == tc.saved {
				t.Errorf("open %v after %v", d.IsOpen(), tc.err)
			}
			layouts.wait()
			saved, _ := readConfig(t, layouts)
			got := saved.Panels[0].Settings
			if tc.saved != reflect.DeepEqual(got, utc) {
				t.Errorf("file has settings %v", got)
			}
			if tc.saved != (len(*applied) == 1) {
				t.Errorf("applied %d configs", len(*applied))
			}
			if tc.saved && !reflect.DeepEqual(w.cfg.Panels[0].Settings, utc) {
				t.Errorf("window has settings %v", w.cfg.Panels[0].Settings)
			}
		})
	}
}

// Settings the panel has already are not saved again, and a panel gone
// meanwhile keeps the dialog open.
func TestSetSettingsUnchanged(t *testing.T) {
	w, layouts, applied := configureWindow(t)
	if err := w.setSettings(tried{panel: "a", plugin: "clock", settings: config.Settings{}}); err != nil {
		t.Fatal(err)
	}
	if err := w.setSettings(tried{panel: "z"}); !errors.Is(err, config.ErrNoPanel) {
		t.Errorf("unknown panel: %v", err)
	}
	layouts.wait()
	if layouts.last != 0 || len(*applied) != 0 {
		t.Errorf("%d saves, %d applied", layouts.last, len(*applied))
	}
}

func TestOpenConfigure(t *testing.T) {
	cfg := layoutConfig()
	cfg.Panels[0].Settings = config.Settings{"timezone": "UTC"}
	var d ui.ConfigDialog
	var toast ui.Toaster
	states := []pluginhost.PanelState{{ID: "a", Plugin: "clock", Described: true, Schema: `{}`}, {ID: "b", Plugin: "clock"}}
	openConfigure(&d, &toast, cfg, states, "b")
	if d.IsOpen() {
		t.Error("opened for a plugin that never described itself")
	}
	openConfigure(&d, &toast, cfg, states, "a")
	if !d.IsOpen() || d.Panel != "a" || d.Plugin != "clock" {
		t.Errorf("open %v for %q of %q", d.IsOpen(), d.Panel, d.Plugin)
	}
}

// Settings the plugin gets alike are the same: a null setting is a
// missing one, and 3 is 3.0.
func TestSameSettingsAsThePluginSeesThem(t *testing.T) {
	file := config.Settings{"ratio": 3.0, "title": nil, "list": []any{int64(1), "a"}}
	form := config.Settings{"ratio": int64(3), "list": []any{1.0, "a"}}
	if !sameSettings(file, form) {
		t.Error("settings alike to the plugin are not the same")
	}
	if sameSettings(file, config.Settings{"ratio": int64(4)}) {
		t.Error("different settings are the same")
	}
}

// A panel the file changed while the dialog was open is not saved over:
// its settings, or its plugin.
func TestStaleSaveRefused(t *testing.T) {
	utc := config.Settings{"timezone": "UTC"}
	for name, change := range map[string]func(*config.Panel){
		"settings": func(p *config.Panel) { p.Settings = config.Settings{"hour12": true} },
		"plugin":   func(p *config.Panel) { p.Plugin = "worldclock" },
	} {
		t.Run(name, func(t *testing.T) {
			w, _, applied := configureWindow(t)
			var reconfigured []string
			w.reconfigure = func(id string) { reconfigured = append(reconfigured, id) }
			var d ui.ConfigDialog
			d.Open("a", "clock", `{"type": "object", "properties": {"timezone": {"type": "string"}}}`, nil)
			change(&w.cfg.Panels[0])
			tr := tried{panel: "a", plugin: "clock", settings: utc}
			if !w.changedSince(tr) {
				t.Error("changedSince missed the change")
			}
			w.settingsTried(&d, tr)
			if !d.IsOpen() || len(*applied) != 0 {
				t.Errorf("saved over a changed panel: open %v, applied %d", d.IsOpen(), len(*applied))
			}
			// The plugin took the settings tried: it gets the file's back.
			if !slices.Equal(reconfigured, []string{"a"}) {
				t.Errorf("reconfigured %v, want [a]", reconfigured)
			}
		})
	}
}

// Whole numbers past 2^53 differ when they do, though a plugin gets
// them alike, so the edit is saved.
func TestSameSettingsLargeWholeNumbers(t *testing.T) {
	if sameSettings(config.Settings{"n": int64(9007199254740993)}, config.Settings{"n": int64(9007199254740992)}) {
		t.Error("different whole numbers are the same")
	}
	if sameSettings(config.Settings{"n": int64(9007199254740993)}, config.Settings{"n": 9007199254740992.0}) {
		t.Error("a whole number and a float64 it rounds to are the same")
	}
}

// A panel the file changes after the window's check, before the save is
// written, is not saved over either.
func TestStaleSaveRefusedAsWritten(t *testing.T) {
	w, layouts, _ := configureWindow(t)
	hour12 := config.Settings{"hour12": true}
	if err := config.Update(layouts.path, func(c *config.Config) error { return c.SetSettings("a", hour12) }); err != nil {
		t.Fatal(err)
	}
	if err := w.setSettings(tried{panel: "a", plugin: "clock", settings: config.Settings{"timezone": "UTC"}}); err != nil {
		t.Fatal(err)
	}
	layouts.wait()
	select {
	case err := <-layouts.done:
		if !errors.Is(err, errChanged) {
			t.Errorf("write error %v, want errChanged", err)
		}
	default:
		t.Error("written over the file's change")
	}
	if saved, _ := readConfig(t, layouts); !reflect.DeepEqual(saved.Panels[0].Settings, hour12) {
		t.Errorf("file has settings %v, want %v", saved.Panels[0].Settings, hour12)
	}
}
