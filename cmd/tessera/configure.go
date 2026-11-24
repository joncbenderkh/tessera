// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"reflect"
	"slices"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui"
)

// tried is TrySettings' answer for settings the Configure dialog saves.
type tried struct {
	panel    string
	plugin   string          // the panel's plugin when the dialog opened
	original config.Settings // and its settings then
	settings config.Settings
	err      error
}

// errChanged is a save refused because the panel in the file is not as
// the dialog found it, changed there meanwhile or never saved there by
// an earlier write that failed: saving would undo a change, or give one
// plugin's settings to another.
var errChanged = errors.New("this panel in the config file differs from the one the dialog opened with; reopen Configure")

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
		if err := w.setSettings(t); err != nil {
			if t.err == nil && w.reconfigure != nil {
				// The plugin took the settings tried: give it the
				// file's back.
				w.reconfigure(t.panel)
			}
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

// setSettings gives the panel t names the settings t holds at once,
// and saves them, as change does a layout change, unless the file has
// changed the panel since the dialog opened (errChanged). Settings the
// panel already has are not saved again, since a save drops the file's
// comments.
func (w *windowConfig) setSettings(t tried) error {
	id, settings := t.panel, t.settings
	applyMu.Lock()
	defer applyMu.Unlock()
	w.takeReloadLocked()
	// Checked again as the file is written, as it may change first.
	c := layoutChange(func(c *config.Config) error {
		if changedIn(*c, t) {
			return errChanged
		}
		return c.SetSettings(id, settings)
	})
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

// changedSince reports whether the file has changed the panel t names,
// its plugin or settings, since the dialog opened, taking any reload.
func (w *windowConfig) changedSince(t tried) bool {
	applyMu.Lock()
	defer applyMu.Unlock()
	w.takeReloadLocked()
	return changedIn(w.cfg, t)
}

// changedIn reports whether cfg has the panel t names with another
// plugin or other settings than the dialog opened with.
func changedIn(cfg config.Config, t tried) bool {
	for _, p := range cfg.Panels {
		if p.ID == t.panel {
			return p.Plugin != t.plugin || !sameSettings(p.Settings, t.original)
		}
	}
	return false // gone: SetSettings says so
}

// sameSettings reports whether a and b are the same settings to their
// plugin: none and empty ones are the same, a null setting is a missing
// one, and 3 and 3.0 are one number.
func sameSettings(a, b config.Settings) bool {
	a, b = a.WithoutNulls(), b.WithoutNulls()
	return len(a) == 0 && len(b) == 0 || sameValue(map[string]any(a), map[string]any(b))
}

// sameValue reports whether settings values a and b are alike: an int64
// and a float64 are when they are the same number, and two int64s only
// when equal, though a plugin gets each as a float64, so that no edit
// to a large whole number is taken for no change.
func sameValue(a, b any) bool {
	switch a := a.(type) {
	case int64:
		switch b := b.(type) {
		case int64:
			return a == b
		case float64:
			return float64(a) == b && b >= -(1<<63) && b < 1<<63 && int64(b) == a
		}
		return false
	case float64:
		if _, ok := b.(int64); ok {
			return sameValue(b, a)
		}
	case map[string]any:
		b, ok := b.(map[string]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for k, x := range a {
			y, ok := b[k]
			if !ok || !sameValue(x, y) {
				return false
			}
		}
		return true
	case []any:
		b, ok := b.([]any)
		return ok && slices.EqualFunc(a, b, sameValue)
	}
	return reflect.DeepEqual(a, b)
}
