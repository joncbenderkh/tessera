// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"errors"
	"testing"
	"time"

	"github.com/joncbenderkh/tessera/internal/config"
)

// Settings that fail the plugin's schema never reach it: the panel
// crashes with the problems, keeps the schema for the Configure dialog,
// and launches again once they are fixed.
func TestSettingsFailSchema(t *testing.T) {
	dir := install(t, "schema")
	logger, _ := newLogger()
	cfg := panels("schema")
	cfg.Panels[0].Settings = config.Settings{"colour": "red"}
	m := StartWith(cfg, dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	var invalid *config.SettingsError
	if !errors.As(s.Err, &invalid) || len(invalid.Problems) != 1 || invalid.Problems[0].String() != "settings.colour: not a known setting" {
		t.Errorf("error %v, want the schema's problem", s.Err)
	}
	if Retryable(s.Err) {
		t.Error("a schema failure is retried")
	}
	if !s.Described || s.Schema != testSchema {
		t.Errorf("Described %v, Schema %q; want the plugin's", s.Described, s.Schema)
	}
	if n := countLines(m.Logs("p0"), "configured p0"); n != 0 {
		t.Errorf("the plugin was configured %d times", n)
	}

	cfg.Panels[0].Settings = config.Settings{"timezone": "UTC"}
	m.Apply(cfg)
	waitFor(t, m, "p0", "live", is(Live))
	if s := m.States()[0]; !s.Described || s.Schema != testSchema {
		t.Errorf("live: Described %v, Schema %q", s.Described, s.Schema)
	}
}

func TestBadSchema(t *testing.T) {
	dir := install(t, "bad-schema")
	logger, _ := newLogger()
	m := StartWith(panels("bad-schema"), dir, logger, fast)
	defer m.Close()
	// The schema checks nothing; the logs say why.
	waitFor(t, m, "p0", "live", is(Live))
	if countLines(m.Logs("p0"), "settings not checked by the host: the plugin declares an invalid config schema") != 1 {
		t.Errorf("logs %q, want the invalid schema", m.Logs("p0"))
	}
	if err := m.TrySettings(t.Context(), "p0", config.Settings{"timezone": int64(3)}); err != nil {
		t.Errorf("TrySettings = %v, want the plugin to decide", err)
	}
}

// A null setting is left out, for the plugin's default, not checked
// against the schema as a value.
func TestNullSettings(t *testing.T) {
	dir := install(t, "schema")
	logger, _ := newLogger()
	cfg := panels("schema")
	cfg.Panels[0].Settings = config.Settings{"timezone": nil}
	m := StartWith(cfg, dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	for deadline := time.Now().Add(10 * time.Second); countLines(m.Logs("p0"), "configured p0 map[] 1x1") != 1; {
		if time.Now().After(deadline) {
			t.Fatalf("logs %q, want configured without settings", m.Logs("p0"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.TrySettings(t.Context(), "p0", config.Settings{"timezone": nil}); err != nil {
		t.Errorf("TrySettings = %v", err)
	}
}

func TestTrySettings(t *testing.T) {
	dir := install(t, "schema")
	logger, _ := newLogger()
	m := StartWith(panels("schema"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	await := func(want string, n int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); countLines(m.Logs("p0"), want) < n; {
			if time.Now().After(deadline) {
				t.Fatalf("no %d× %q in %q", n, want, m.Logs("p0"))
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	await("configured p0 map[] 1x1", 1)

	var invalid *config.SettingsError
	if err := m.TrySettings(t.Context(), "p0", config.Settings{"timezone": int64(3)}); !errors.As(err, &invalid) {
		t.Errorf("wrong type: %v, want a SettingsError", err)
	}

	// Values the config file cannot hold never reach the plugin.
	if err := m.TrySettings(t.Context(), "p0", config.Settings{"timezone": "\xff"}); err == nil || errors.As(err, &invalid) {
		t.Errorf("invalid UTF-8: %v, want a plain error", err)
	}

	var rejected *RejectedError
	err := m.TrySettings(t.Context(), "p0", config.Settings{"timezone": "Mars"})
	if !errors.As(err, &rejected) || rejected.Message != "timezone: unknown" {
		t.Errorf("Mars: %v, want the plugin's rejection", err)
	}
	// Given its own settings back, and still live.
	await("configured p0 map[] 1x1", 2)
	if s := m.States()[0]; s.State != Live || s.Err != nil {
		t.Errorf("after a rejection: %v %v", s.State, s.Err)
	}

	if err := m.TrySettings(t.Context(), "p0", config.Settings{"timezone": "UTC"}); err != nil {
		t.Errorf("UTC: %v", err)
	}
	await("configured p0 map[timezone:UTC] 1x1", 1)
	if n := countLines(m.Logs("p0"), "configured p0"); n != 3 {
		t.Errorf("configured %d times, want 3: %q", n, m.Logs("p0"))
	}

	if err := m.TrySettings(t.Context(), "p9", nil); err == nil {
		t.Error("an unknown panel tried settings")
	}
}

// A panel that is not live has only its schema checked.
func TestTrySettingsNotLive(t *testing.T) {
	dir := install(t, "rejects")
	logger, _ := newLogger()
	m := StartWith(panels("rejects"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "crashed", is(Crashed))
	if err := m.TrySettings(t.Context(), "p0", config.Settings{"timezone": "UTC"}); !errors.Is(err, ErrNotLive) {
		t.Errorf("TrySettings = %v, want ErrNotLive", err)
	}
}

// A plugin that declares no schema takes no settings.
func TestNoSchema(t *testing.T) {
	dir := install(t, "no-schema")
	logger, _ := newLogger()
	cfg := panels("no-schema")
	m := StartWith(cfg, dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	var invalid *config.SettingsError
	if err := m.TrySettings(t.Context(), "p0", config.Settings{"a": int64(1)}); !errors.As(err, &invalid) {
		t.Errorf("TrySettings = %v, want a SettingsError", err)
	}
	cfg.Panels[0].Settings = config.Settings{"a": int64(1)}
	m.Apply(cfg)
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	if !errors.As(s.Err, &invalid) {
		t.Errorf("error %v, want a SettingsError", s.Err)
	}
}
