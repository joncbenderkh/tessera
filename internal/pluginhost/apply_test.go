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
	m.mu.Lock()
	disabled := m.panels[1].inst
	m.mu.Unlock()

	cfg := panels("clock", "disk")
	cfg.Panels[1].Enabled = new(bool) // disabled
	cfg.Panels = append(cfg.Panels, config.Panel{ID: "p2", Plugin: "disk", X: 2, W: 1, H: 1})
	m.Apply(cfg)
	if got := ids(m); !slices.Equal(got, []string{"p0", "p2"}) {
		t.Fatalf("panels %v after Apply, want [p0 p2]", got)
	}
	select {
	case <-disabled.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the disabled panel's plugin is still running")
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

	// Fixing the settings relaunches it, with no Restart.
	cfg.Panels[0].Settings = config.Settings{"timezone": "UTC"}
	m.Apply(cfg)
	waitFor(t, m, "p0", "live again", is(Live))
}

// A panel waiting out its backoff restarts at once when its settings
// change, since the edit may be the fix.
func TestApplyRestartsBackingOffPanel(t *testing.T) {
	dir := install(t, "crashes")
	logger, _ := newLogger()
	slow := fast
	slow.Backoff = time.Minute
	m := StartWith(panels("crashes"), dir, logger, slow)
	defer m.Close()
	waitFor(t, m, "p0", "restarting", is(Restarting))
	cfg := panels("crashes")
	cfg.Panels[0].Settings = config.Settings{"timezone": "UTC"}
	m.Apply(cfg)
	waitFor(t, m, "p0", "launched again before its backoff", func(PanelState) bool {
		return countLines(m.Logs("p0"), "plugin exited: ") >= 2
	})
}

// A panel disabled and enabled again, or given another plugin, waits for
// its old plugin to exit before it launches the new one.
func TestApplyWaitsForOldPlugin(t *testing.T) {
	dir := install(t, "slow-shutdown")
	logger, _ := newLogger()
	m := StartWith(panels("slow-shutdown"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	m.mu.Lock()
	old := m.panels[0].inst
	m.mu.Unlock()

	off := panels("slow-shutdown")
	off.Panels[0].Enabled = new(bool)
	m.Apply(off)
	m.Apply(panels("slow-shutdown"))
	waitFor(t, m, "p0", "live again", is(Live))
	if !old.Exited() {
		t.Error("the new plugin launched while the old one was still shutting down")
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

func TestApplyWaitsForOldPluginOnPluginChange(t *testing.T) {
	dir := install(t, "slow-shutdown", "clock")
	logger, _ := newLogger()
	m := StartWith(panels("slow-shutdown"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	m.mu.Lock()
	old := m.panels[0].inst
	m.mu.Unlock()

	m.Apply(panels("clock"))
	waitFor(t, m, "p0", "live with the new plugin", func(s PanelState) bool { return s.State == Live && s.Plugin == "clock" })
	if !old.Exited() {
		t.Error("the new plugin launched while the old one was still shutting down")
	}
}

func TestSameSettings(t *testing.T) {
	for _, tt := range []struct {
		a, b config.Settings
		want bool
	}{
		{nil, config.Settings{}, true},
		{config.Settings{"zone": "UTC"}, config.Settings{"zone": "UTC"}, true},
		{nil, config.Settings{"zone": "UTC"}, false},
	} {
		if got := sameSettings(tt.a, tt.b); got != tt.want {
			t.Errorf("sameSettings(%v, %v) = %t", tt.a, tt.b, got)
		}
	}
}

// A polled plugin configured again is rendered at once, not at its next
// poll.
func TestApplyRendersPolledPluginAgain(t *testing.T) {
	dir := install(t, "polled-events")
	logger, _ := newLogger()
	opts := fast
	opts.PollInterval = time.Hour // only the reconfigure makes it render
	m := StartWith(panels("polled-events"), dir, logger, opts)
	defer m.Close()
	waitFor(t, m, "p0", "rendered", showing("render"))
	cfg := panels("polled-events")
	cfg.Panels[0].Settings = config.Settings{"units": "metric"}
	m.Apply(cfg)
	waitFor(t, m, "p0", "rendered after configuring again", showing("render,render"))
}

// A panel whose views stopped over a limit is relaunched when its
// settings change, as a crashed one is: they may fit the limits now.
func TestApplyRelaunchesPanelOverLimit(t *testing.T) {
	dir := install(t, "big-view")
	logger, _ := newLogger()
	m := StartWith(panels("big-view"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "refusing the view", func(s PanelState) bool { return s.ViewErr != nil })
	cfg := panels("big-view")
	cfg.Panels[0].Settings = config.Settings{"items": 10}
	m.Apply(cfg)
	deadline := time.Now().Add(5 * time.Second)
	for countLines(m.Logs("p0"), "starting plugin") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("not relaunched after new settings:\n%s", strings.Join(m.Logs("p0"), "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if logs := m.Logs("p0"); countLines(logs, "settings or size changed; restarting") != 1 || countLines(logs, "restart requested") != 0 {
		t.Errorf("relaunch logged as:\n%s", strings.Join(logs, "\n"))
	}
}

// A live plugin that does not answer Configure again is restarted with
// backoff, as a hang, not left crashed.
func TestApplyHungReconfigureRestarts(t *testing.T) {
	dir := install(t, "hangs-configure-later")
	logger, _ := newLogger()
	m := StartWith(panels("hangs-configure-later"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	cfg := panels("hangs-configure-later")
	cfg.Panels[0].Settings = config.Settings{"timezone": "UTC"}
	m.Apply(cfg)
	s := waitFor(t, m, "p0", "restarted", func(s PanelState) bool { return s.Failures > 0 })
	if s.State == Crashed {
		t.Fatalf("crashed after a hung Configure: %+v", s)
	}
	// The next launch's first Configure answers.
	waitFor(t, m, "p0", "live again", is(Live))
	if n := countLines(m.Logs("p0"), "starting plugin"); n != 2 {
		t.Errorf("%d launches, want 2:\n%s", n, strings.Join(m.Logs("p0"), "\n"))
	}
}

// Settings changed while a panel is launching are the ones its first
// Configure sends, and it is not configured a second time for them.
func TestApplyDuringLaunch(t *testing.T) {
	dir := install(t, "clock")
	logger, _ := newLogger()
	held, release := make(chan struct{}), make(chan struct{})
	beforeLaunchHook = func(*panel) {
		held <- struct{}{}
		<-release
	}
	defer func() { beforeLaunchHook = func(*panel) {} }()
	m := StartWith(panels("clock"), dir, logger, fast)
	defer m.Close()
	<-held
	cfg := panels("clock")
	cfg.Panels[0].Settings = config.Settings{"timezone": "UTC"}
	m.Apply(cfg)
	beforeLaunchHook = func(*panel) {}
	close(release)
	waitFor(t, m, "p0", "live", is(Live))
	time.Sleep(200 * time.Millisecond) // time for a second Configure, if any
	logs := m.Logs("p0")
	if countLines(logs, "configured p0") != 1 || countLines(logs, "configured p0 map[timezone:UTC]") != 1 {
		t.Errorf("want one Configure, with the new settings:\n%s", strings.Join(logs, "\n"))
	}
}
