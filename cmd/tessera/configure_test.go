// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"path/filepath"
	"reflect"
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
			w.settingsTried(&d, tried{"a", utc, tc.err})
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
	if err := w.setSettings("a", config.Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := w.setSettings("z", nil); !errors.Is(err, config.ErrNoPanel) {
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
