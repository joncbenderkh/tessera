// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"errors"
	"strings"
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

	// A plugin that exits trying them is not "interrupted": the
	// settings may be why.
	if err := m.TrySettings(t.Context(), "p0", config.Settings{"timezone": "Crash"}); !errors.Is(err, ErrTrialExited) {
		t.Errorf("Crash: %v, want ErrTrialExited", err)
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

// A plugin that declares no schema has its settings unchecked by the
// host: it checks its own, as before there were schemas.
func TestNoSchema(t *testing.T) {
	dir := install(t, "no-schema")
	logger, _ := newLogger()
	cfg := panels("no-schema")
	cfg.Panels[0].Settings = config.Settings{"a": int64(1)}
	m := StartWith(cfg, dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	if err := m.TrySettings(t.Context(), "p0", config.Settings{"a": int64(2)}); err != nil {
		t.Errorf("TrySettings = %v", err)
	}
	if strings.Contains(strings.Join(m.Logs("p0"), "\n"), "not checked by the host") {
		t.Errorf("logged a missing schema as invalid: %q", m.Logs("p0"))
	}
}

// Reconfigure gives a live panel's plugin its settings again.
func TestReconfigure(t *testing.T) {
	dir := install(t, "schema")
	logger, _ := newLogger()
	m := StartWith(panels("schema"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	for deadline := time.Now().Add(10 * time.Second); countLines(m.Logs("p0"), "configured p0") < 1; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("never configured")
		}
	}
	m.Reconfigure("p0")
	for deadline := time.Now().Add(10 * time.Second); countLines(m.Logs("p0"), "configured p0") < 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("not configured again: %q", m.Logs("p0"))
		}
	}
}

// A panel stopped while its plugin tries settings gives ErrInterrupted,
// and nothing is saved.
func TestTrySettingsInterrupted(t *testing.T) {
	dir := install(t, "hangs-configure-later")
	logger, _ := newLogger()
	cfg := panels("hangs-configure-later")
	opts := fast
	opts.CallTimeout = 10 * time.Second
	m := StartWith(cfg, dir, logger, opts)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	done := make(chan error, 1)
	go func() { done <- m.TrySettings(t.Context(), "p0", config.Settings{"a": int64(1)}) }()
	for deadline := time.Now().Add(5 * time.Second); countLines(m.Logs("p0"), "configure hangs") < 1; {
		if time.Now().After(deadline) {
			t.Fatalf("the trial never reached the plugin: %q", m.Logs("p0"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cfg.SetEnabled("p0", false); err != nil {
		t.Fatal(err)
	}
	m.Apply(cfg)
	select {
	case err := <-done:
		if !errors.Is(err, ErrInterrupted) {
			t.Errorf("TrySettings = %v, want ErrInterrupted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TrySettings still waiting after the panel stopped")
	}
}
