// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

func viewText(s PanelState) string { return s.View.GetBody().GetText().GetValue() }

func showing(text string) func(PanelState) bool {
	return func(s PanelState) bool { return s.State == Live && viewText(s) == text }
}

func activate(id string) *tesserav1.HandleEventRequest {
	return &tesserav1.HandleEventRequest{Event: &tesserav1.HandleEventRequest_Activate{Activate: &tesserav1.ActivateEvent{ActionId: id}}}
}

func TestWatchedViewArrives(t *testing.T) {
	dir := install(t, "clock")
	logger, _ := newLogger()
	m := StartWith(panels("clock"), dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "showing its view", showing("clock"))
	if s.Polled || s.Updated.IsZero() {
		t.Errorf("got %+v", s)
	}
}

func TestHeartbeatKeepsPanelLive(t *testing.T) {
	dir := install(t, "clock")
	logger, _ := newLogger()
	opts := fast
	opts.Heartbeat = time.Second
	m := StartWith(panels("clock"), dir, logger, opts)
	defer m.Close()
	waitFor(t, m, "p0", "showing its view", showing("clock"))
	time.Sleep(3500 * time.Millisecond) // past the silence that would be a hang
	if s := m.States()[0]; s.State != Live || viewText(s) != "clock" || countLines(m.Logs("p0"), "starting plugin") != 1 {
		t.Errorf("heartbeats did not keep the view: %+v\n%s", s, strings.Join(m.Logs("p0"), "\n"))
	}
}

func TestWatchFailures(t *testing.T) {
	for _, tt := range []struct{ mode, want string }{
		{"silent-watch", "hung: its Watch stream was silent for"},
		{"ends-watch", `plugin "ends-watch" ended its Watch stream`},
		{"late-unimplemented", `plugin "late-unimplemented" ended its Watch stream: rpc error: code = Unimplemented`},
		{"quota-watch", `plugin "quota-watch" ended its Watch stream: rpc error: code = ResourceExhausted`},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			dir := install(t, tt.mode)
			logger, _ := newLogger()
			opts := fast
			opts.Heartbeat = time.Second
			m := StartWith(panels(tt.mode), dir, logger, opts)
			defer m.Close()
			s := waitFor(t, m, "p0", "restarting", is(Restarting))
			if !strings.Contains(s.Err.Error(), tt.want) {
				t.Errorf("got %v, want %q", s.Err, tt.want)
			}
		})
	}
}

func TestUnimplementedWatchIsPolled(t *testing.T) {
	dir := install(t, "polled")
	logger, _ := newLogger()
	m := StartWith(panels("polled"), dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "polled three times", showing("render 2"))
	if !s.Polled || countLines(m.Logs("p0"), "polling Render") != 1 {
		t.Errorf("got %+v\n%s", s, strings.Join(m.Logs("p0"), "\n"))
	}
}

func TestFailingRenderIsStale(t *testing.T) {
	dir := install(t, "polled-failing")
	logger, _ := newLogger()
	m := StartWith(panels("polled-failing"), dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "stale", is(Stale))
	if viewText(s) != "render 0" || s.ViewErr == nil || !strings.Contains(s.ViewErr.Error(), "source down") {
		t.Errorf("Stale without the last view and the error: %+v", s)
	}
	if err := os.WriteFile(filepath.Join(dir, "polled-failing"+exeSuffix()+".open"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s = waitFor(t, m, "p0", "live again", showing("render 1"))
	if s.ViewErr != nil || countLines(m.Logs("p0"), "starting plugin") != 1 {
		t.Errorf("got %+v after Render recovered\n%s", s, strings.Join(m.Logs("p0"), "\n"))
	}
}

func TestPluginsResourceExhaustedIsStale(t *testing.T) {
	dir := install(t, "polled-quota")
	logger, _ := newLogger()
	m := StartWith(panels("polled-quota"), dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "live after the failed Render", showing("render 2"))
	logs := m.Logs("p0")
	if s.ViewErr != nil || countLines(logs, "Render failed") != 1 || countLines(logs, "size limit") != 0 {
		t.Errorf("a quota error was not a failed Render: %+v\n%s", s, strings.Join(logs, "\n"))
	}
}

func TestRenderWithNoViewClearsStale(t *testing.T) {
	dir := install(t, "polled-empty")
	logger, _ := newLogger()
	m := StartWith(panels("polled-empty"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live again with the last view", func(s PanelState) bool {
		return showing("render 0")(s) && s.ViewErr == nil && countLines(m.Logs("p0"), "Render answers again") == 1
	})
}

func TestNoWatchNorRenderIsPermanent(t *testing.T) {
	dir := install(t, "no-views")
	logger, _ := newLogger()
	m := StartWith(panels("no-views"), dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	if !strings.Contains(s.Err.Error(), `plugin "no-views" implements neither Watch nor Render`) || countLines(m.Logs("p0"), "starting plugin") != 1 {
		t.Errorf("got %+v\n%s", s, strings.Join(m.Logs("p0"), "\n"))
	}
}

func TestHungRenderIsAHang(t *testing.T) {
	dir := install(t, "hangs-render")
	logger, _ := newLogger()
	m := StartWith(panels("hangs-render"), dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "restarting", is(Restarting))
	if !strings.Contains(s.Err.Error(), "hung: Render did not answer") {
		t.Errorf("got %v", s.Err)
	}
}

func TestViewOverLimitIsRefused(t *testing.T) {
	for _, tt := range []struct{ mode, want string }{
		{"deep-view", `plugin "deep-view" sent a view nested 17 deep, over the limit of 16`},
		{"too-deep-to-decode", `plugin "too-deep-to-decode" sent a view nested over the limit of 16`},
		{"big-view", `plugin "big-view" sent a view over the size limit`},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			dir := install(t, tt.mode)
			logger, _ := newLogger()
			m := StartWith(panels(tt.mode), dir, logger, fast)
			defer m.Close()
			s := waitFor(t, m, "p0", "refusing the view", func(s PanelState) bool { return s.ViewErr != nil })
			if s.State != Live || s.View != nil || !strings.Contains(s.ViewErr.Error(), tt.want) {
				t.Errorf("got %+v, want Live with no view and %q", s, tt.want)
			}
			time.Sleep(time.Second)
			if s := m.States()[0]; s.State != Live || countLines(m.Logs("p0"), "starting plugin") != 1 {
				t.Errorf("restarted over a view limit: %+v", s)
			}
		})
	}
}

func TestViewsAreCoalesced(t *testing.T) {
	dir := install(t, "spams-views")
	logger, _ := newLogger()
	m := StartWith(panels("spams-views"), dir, logger, fast)
	defer m.Close()
	first := waitFor(t, m, "p0", "showing views", func(s PanelState) bool { return s.View != nil })
	signals := 0
	for timeout := time.After(time.Second); ; {
		select {
		case <-m.Changed():
			signals++
			continue
		case <-timeout:
		}
		break
	}
	last := m.States()[0]
	if signals > 40 { // 30 a second, and a few state changes
		t.Errorf("%d Changed signals in a second", signals)
	}
	if viewText(last) == viewText(first) || !last.Updated.After(first.Updated) {
		t.Errorf("view stuck at %q", viewText(last))
	}
}

func TestEventsReachPlugin(t *testing.T) {
	dir := install(t, "events")
	logger, _ := newLogger()
	m := StartWith(panels("events"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", showing("events"))
	if err := m.HandleEvent("p0", activate("pid:4821")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "p0", "showing the event", showing("got pid:4821"))
}

func TestEventsReachPolledPluginInOrder(t *testing.T) {
	dir := install(t, "polled-events")
	logger, _ := newLogger()
	opts := fast
	opts.PollInterval = time.Hour // only an event makes it render
	m := StartWith(panels("polled-events"), dir, logger, opts)
	defer m.Close()
	waitFor(t, m, "p0", "rendered", showing("render"))
	for _, id := range []string{"a", "b", "c"} {
		if err := m.HandleEvent("p0", activate(id)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, m, "p0", "rendering after each event, in order", showing("render,a,render,b,render,c,render"))
}

func TestHungEventIsAHang(t *testing.T) {
	dir := install(t, "hangs-event")
	logger, _ := newLogger()
	m := StartWith(panels("hangs-event"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	if err := m.HandleEvent("p0", activate("x")); err != nil {
		t.Fatal(err)
	}
	s := waitFor(t, m, "p0", "restarting", is(Restarting))
	if !strings.Contains(s.Err.Error(), "hung: HandleEvent did not answer") {
		t.Errorf("got %v", s.Err)
	}
}

func TestHandleEventErrors(t *testing.T) {
	dir := install(t, "hangs-event", "rejects")
	logger, _ := newLogger()
	opts := fast
	opts.CallTimeout = 10 * time.Second // the first event holds the rest
	m := StartWith(panels("hangs-event", "rejects"), dir, logger, opts)
	waitFor(t, m, "p0", "live", is(Live))
	waitFor(t, m, "p1", "crashed", is(Crashed))
	if err := m.HandleEvent("nope", activate("x")); err == nil {
		t.Error("an event for an unknown panel was taken")
	}
	if err := m.HandleEvent("p1", activate("x")); !errors.Is(err, ErrNotLive) {
		t.Errorf("crashed panel: got %v, want ErrNotLive", err)
	}
	var err error
	for range maxEvents + 2 { // one taken, maxEvents waiting
		if err = m.HandleEvent("p0", activate("x")); err != nil {
			break
		}
	}
	if !errors.Is(err, ErrBusy) {
		t.Errorf("full queue: got %v, want ErrBusy", err)
	}
	m.Close()
	if err := m.HandleEvent("p0", activate("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("after Close: got %v, want ErrClosed", err)
	}
}

func TestRestartClearsStaleView(t *testing.T) {
	dir := install(t, "polled-failing")
	logger, _ := newLogger()
	m := StartWith(panels("polled-failing"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "stale", is(Stale))
	if err := m.Restart("p0"); err != nil {
		t.Fatalf("Restart from Stale: %v", err)
	}
	s := waitFor(t, m, "p0", "starting", is(Starting))
	if s.View != nil || s.ViewErr != nil || s.Polled {
		t.Errorf("the last launch's view survived the restart: %+v", s)
	}
	waitFor(t, m, "p0", "live again", showing("render 0"))
}
