// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joncbenderkh/tessera/internal/config"
)

func ids(m *Manager) []string {
	var out []string
	for _, s := range m.States() {
		out = append(out, s.ID)
	}
	return out
}

func TestApplyAddsAndRemovesPanels(t *testing.T) {
	dir := install(t, "clock", "disk")
	logger, _ := newLogger()
	m := StartWith(panels("clock", "disk"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	waitFor(t, m, "p1", "live", is(Live))

	cfg := panels("clock", "disk")
	cfg.Panels[1].Enabled = new(bool) // disabled
	cfg.Panels = append(cfg.Panels, config.Panel{ID: "p2", Plugin: "disk", X: 2, W: 1, H: 1})
	m.Apply(cfg)
	if got := ids(m); !slices.Equal(got, []string{"p0", "p2"}) {
		t.Fatalf("panels %v after Apply, want [p0 p2]", got)
	}
	waitFor(t, m, "p2", "live", is(Live))
	if s := m.States()[0]; s.State != Live {
		t.Errorf("unchanged panel p0 is %v, want Live", s.State)
	}

	m.Apply(config.Config{})
	if got := ids(m); len(got) != 0 {
		t.Fatalf("panels %v after removing them all", got)
	}
}

func TestApplyRelaunchesChangedPlugin(t *testing.T) {
	dir := install(t, "clock", "disk")
	logger, _ := newLogger()
	m := StartWith(panels("clock"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	m.Apply(panels("disk"))
	s := waitFor(t, m, "p0", "showing disk", showing("disk"))
	if s.Plugin != "disk" {
		t.Errorf("plugin %q, want disk", s.Plugin)
	}
}

func TestApplyConfiguresChangedSettings(t *testing.T) {
	dir := install(t, "clock")
	logger, _ := newLogger()
	m := StartWith(panels("clock"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	cfg := panels("clock")
	cfg.Panels[0].Settings = config.Settings{"timezone": "UTC"}
	cfg.Panels[0].W = 2
	m.Apply(cfg)
	deadline := time.Now().Add(10 * time.Second)
	for countLines(m.Logs("p0"), "configured p0 map[timezone:UTC] 2x1") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("plugin not configured again; logs:\n%s", strings.Join(m.Logs("p0"), "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s := m.States()[0]; s.State != Live || s.Failures != 0 {
		t.Errorf("after configuring again: %+v", s)
	}

	moved := cfg
	moved.Panels = slices.Clone(cfg.Panels)
	moved.Panels[0].X, moved.Panels[0].Y = 1, 3
	m.Apply(moved)
	time.Sleep(100 * time.Millisecond)
	if n := countLines(m.Logs("p0"), "configured p0"); n != 2 {
		t.Errorf("configured %d times, want 2: moving a panel must not configure it", n)
	}
}

func TestApplyRejectedSettingsCrash(t *testing.T) {
	dir := install(t, "rejects-later")
	logger, _ := newLogger()
	m := StartWith(panels("rejects-later"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	cfg := panels("rejects-later")
	cfg.Panels[0].Settings = config.Settings{"timezone": "Mars"}
	m.Apply(cfg)
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	if s.Err == nil || !strings.Contains(s.Err.Error(), "rejected the settings") {
		t.Errorf("error %v, want the rejection", s.Err)
	}
}

func TestApplyAfterClose(t *testing.T) {
	dir := install(t, "clock")
	logger, _ := newLogger()
	m := StartWith(panels("clock"), dir, logger, fast)
	m.Close()
	m.Apply(panels("clock", "clock"))
	if got := ids(m); !slices.Equal(got, []string{"p0"}) {
		t.Errorf("panels %v after Apply following Close, want [p0]", got)
	}
}
