// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"reflect"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui"
)

// tried is TrySettings' answer for settings the Configure dialog saves.
type tried struct {
	panel    string
	settings config.Settings
	err      error
}

// openConfigure opens the Configure dialog for panel id, made from the
// config schema its plugin last described. A plugin that has not
// described itself, as one that is missing or fails to start, has no
// form to give.
func openConfigure(d *ui.ConfigDialog, toast *ui.Toaster, cfg config.Config, states []pluginhost.PanelState, id string) {
	var p *config.Panel
	for i := range cfg.Panels {
		if cfg.Panels[i].ID == id {
			p = &cfg.Panels[i]
		}
	}
	for _, s := range states {
		if s.ID != id || p == nil {
			continue
		}
		if !s.Described {
			toast.Show("Settings not available: plugin " + s.Plugin + " has not described them; edit the config file")
			return
		}
		d.Open(id, s.Plugin, s.Schema, p.Settings)
		return
	}
	toast.Show("Settings not available: no panel " + id)
}

// settingsTried acts on TrySettings' answer t: settings the schema and
// the plugin take, or that only the schema could check, are saved and
// the dialog closes; otherwise it shows why not.
func (w *windowConfig) settingsTried(d *ui.ConfigDialog, t tried) {
	var invalid *config.SettingsError
	var rejected *pluginhost.RejectedError
	switch {
	case t.err == nil || errors.Is(t.err, pluginhost.ErrNotLive):
		if err := w.setSettings(t.panel, t.settings); err != nil {
			d.Failed(nil, "Not saved: "+err.Error())
			return
		}
		d.Close()
	case errors.As(t.err, &invalid):
		d.Failed(invalid.Problems, "")
	case errors.As(t.err, &rejected):
		d.Failed(nil, "The plugin rejected these settings: "+rejected.Message)
	default:
		d.Failed(nil, "Not saved: "+t.err.Error())
	}
}

// setSettings gives panel id settings at once, and saves them, as
// change does a layout change. Settings the panel already has are not
// saved again, since a save drops the file's comments.
func (w *windowConfig) setSettings(id string, settings config.Settings) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	w.takeReloadLocked()
	c := layoutChange(func(c *config.Config) error { return c.SetSettings(id, settings) })
	next, err := edited(w.cfg, c)
	if err != nil {
		return err
	}
	for i, p := range w.cfg.Panels {
		if p.ID == id && sameSettings(p.Settings, next.Panels[i].Settings) {
			return nil
		}
	}
	w.cfg = next
	w.layouts.save(c)
	w.apply(w.cfg)
	w.redraw()
	return nil
}

// sameSettings reports whether a and b are the same settings; none and
// empty ones are the same.
func sameSettings(a, b config.Settings) bool {
	return len(a) == 0 && len(b) == 0 || reflect.DeepEqual(a, b)
}
