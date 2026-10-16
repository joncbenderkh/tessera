// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/joncbenderkh/tessera/internal/config"
)

// fast is Options short enough for tests.
var fast = Options{Backoff: 20 * time.Millisecond, MaxAttempts: 3, CallTimeout: 300 * time.Millisecond}

func panels(plugins ...string) config.Config {
	var c config.Config
	for i, p := range plugins {
		c.Panels = append(c.Panels, config.Panel{ID: fmt.Sprint("p", i), Plugin: p, X: i, W: 1, H: 1})
	}
	return c
}

// waitFor waits until panel id's state satisfies ok, and returns it.
func waitFor(t *testing.T, m *Manager, id string, what string, ok func(PanelState) bool) PanelState {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		for _, s := range m.States() {
			if s.ID == id && ok(s) {
				return s
			}
		}
		select {
		case <-m.Changed():
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("panel %s never %s: %+v\nlogs:\n%s", id, what, m.States(), strings.Join(m.Logs(id), "\n"))
		}
	}
}

func is(state State) func(PanelState) bool {
	return func(s PanelState) bool { return s.State == state }
}

func countLines(lines []string, substr string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

func TestRestartsAfterCrashes(t *testing.T) {
	dir := install(t, "flaky")
	logger, _ := newLogger()
	m := StartWith(panels("flaky"), dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "restarting", is(Restarting))
	if s.Failures != 1 || s.RestartAt.IsZero() || !strings.Contains(s.Err.Error(), "exited unexpectedly") {
		t.Errorf("first restart: %+v", s)
	}
	waitFor(t, m, "p0", "live after two crashes", func(s PanelState) bool {
		return s.State == Live && countLines(m.Logs("p0"), "plugin exited: ") == 2
	})
	logs := m.Logs("p0")
	for _, want := range []string{"HOST  plugin exited: exit status 3", "restarting in", "PLUGIN  configured p0"} {
		if countLines(logs, want) == 0 {
			t.Errorf("logs lack %q:\n%s", want, strings.Join(logs, "\n"))
		}
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	dir := install(t, "crashes")
	logger, logs := newLogger()
	m := StartWith(panels("crashes"), dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	if s.Failures != fast.MaxAttempts || s.ExitStatus != "exit status 3" {
		t.Errorf("got attempt %d, status %q; want %d, exit status 3", s.Failures, s.ExitStatus, fast.MaxAttempts)
	}
	if n := countLines(m.Logs("p0"), "starting plugin"); n != fast.MaxAttempts {
		t.Errorf("launched %d times, want %d", n, fast.MaxAttempts)
	}
	if countLines(m.Logs("p0"), "giving up after failure 3/3") != 1 {
		t.Errorf("no give-up event:\n%s", strings.Join(m.Logs("p0"), "\n"))
	}
	if strings.Contains(logs.String(), "plugin process exited") {
		t.Errorf("go-plugin repeats the exit in the host log:\n%s", logs)
	}
}

func TestHangCountsAsFailure(t *testing.T) {
	dir := install(t, "hangs-configure")
	logger, _ := newLogger()
	opts := fast
	opts.MaxAttempts = 2
	m := StartWith(panels("hangs-configure"), dir, logger, opts)
	defer m.Close()
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	if s.Failures != 2 || !strings.Contains(s.Err.Error(), "hung: Configure did not answer within 300ms") {
		t.Errorf("got %+v", s)
	}
}

func TestPermanentFailureIsNotRetried(t *testing.T) {
	dir := install(t, "misnamed")
	logger, _ := newLogger()
	m := StartWith(panels("misnamed"), dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	time.Sleep(10 * fast.Backoff)
	if n := countLines(m.Logs("p0"), "starting plugin"); s.Failures != 1 || n != 1 {
		t.Errorf("attempt %d, %d launches; want 1 of each", s.Failures, n)
	}
	if logs := m.Logs("p0"); countLines(logs, "stopping plugin: ") != 1 || countLines(logs, "HOST  not restarting") != 1 {
		t.Errorf("no stopping and not-restarting events:\n%s", strings.Join(logs, "\n"))
	}
}

func TestManualRestart(t *testing.T) {
	dir := install(t, "crashes", "clock")
	logger, _ := newLogger()
	opts := fast
	opts.MaxAttempts = 1
	m := StartWith(panels("crashes", "clock"), dir, logger, opts)
	defer m.Close()

	waitFor(t, m, "p0", "crashed", is(Crashed))
	if err := m.Restart("p0"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "p0", "relaunched and crashed again", func(s PanelState) bool {
		return s.State == Crashed && countLines(m.Logs("p0"), "starting plugin") == 2
	})

	waitFor(t, m, "p1", "live", is(Live))
	m.mu.Lock()
	first := m.panels[1].inst
	m.mu.Unlock()
	if err := m.Restart("p1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "p1", "live again in a new process", func(s PanelState) bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return s.State == Live && m.panels[1].inst != nil && m.panels[1].inst != first
	})
	if !first.Exited() {
		t.Error("the replaced plugin is still running") // so two ran at once
	}

	if err := m.Restart("nope"); err == nil {
		t.Error("Restart of an unknown panel succeeded")
	}
	if m.Logs("nope") != nil {
		t.Error("Logs of an unknown panel is not nil")
	}
}

func TestAttemptsResetAfterStayingLive(t *testing.T) {
	dir := install(t, "crashes")
	logger, _ := newLogger()
	opts := fast
	opts.MaxAttempts = 2
	opts.ResetAfter = 50 * time.Millisecond // shorter than its 100ms of life
	m := StartWith(panels("crashes"), dir, logger, opts)
	defer m.Close()
	waitFor(t, m, "p0", "restarted three times", func(PanelState) bool {
		return countLines(m.Logs("p0"), "plugin exited: ") >= 3
	})
	for _, s := range m.States() {
		if s.State == Crashed {
			t.Fatalf("gave up despite each launch staying live: %+v", s)
		}
	}
}

func TestFailuresClearOnceSettled(t *testing.T) {
	dir := install(t, "flaky")
	logger, _ := newLogger()
	opts := fast
	opts.ResetAfter = 500 * time.Millisecond // longer than a crash takes
	m := StartWith(panels("flaky"), dir, logger, opts)
	defer m.Close()
	waitFor(t, m, "p0", "live after two failures", func(s PanelState) bool { return s.State == Live && s.Failures == 2 })
	waitFor(t, m, "p0", "cleared its failures", func(s PanelState) bool { return s.State == Live && s.Failures == 0 })
}

func TestDescribeLogsExit(t *testing.T) {
	dir := install(t, "dies-at-start")
	logger, logs := newLogger()
	if _, err := Describe(context.Background(), dir, "dies-at-start", ignore, logger); err == nil {
		t.Fatal("Describe of a plugin that dies succeeded")
	}
	if !strings.Contains(logs.String(), "plugin process exited") {
		t.Errorf("no exit in the host log, with no supervisor to report it:\n%s", logs.String())
	}
}

func TestCloseDuringBackoff(t *testing.T) {
	dir := install(t, "crashes")
	logger, _ := newLogger()
	opts := fast
	opts.Backoff = time.Hour
	m := StartWith(panels("crashes"), dir, logger, opts)
	waitFor(t, m, "p0", "restarting", is(Restarting))
	start := time.Now()
	m.Close()
	if d := time.Since(start); d > time.Second {
		t.Errorf("Close took %v during a backoff", d)
	}
}

func TestCrashLeavesOthersLive(t *testing.T) {
	dir := install(t, "crashes", "clock")
	logger, _ := newLogger()
	m := StartWith(panels("crashes", "clock"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "crashed", is(Crashed))
	if s := m.States()[1]; s.State != Live {
		t.Errorf("clock is %v while another panel crashes", s.State)
	}
}

func TestHangReportedWhicheverSideTimesOut(t *testing.T) {
	// The plugin also sees Configure's deadline and may answer
	// DeadlineExceeded before the host's own timer fires.
	for range 5 {
		TestHangCountsAsFailure(t)
	}
}

func TestDoubleRestartRelaunchesOnce(t *testing.T) {
	dir := install(t, "clock")
	logger, _ := newLogger()
	m := StartWith(panels("clock"), dir, logger, fast)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	m.Restart("p0")
	waitFor(t, m, "p0", "starting", is(Starting))
	if err := m.Restart("p0"); !errors.Is(err, ErrStarting) { // while starting: the same relaunch
		t.Errorf("Restart while starting: got %v, want ErrStarting", err)
	}
	waitFor(t, m, "p0", "live again", func(s PanelState) bool {
		return s.State == Live && countLines(m.Logs("p0"), "starting plugin") >= 2
	})
	// A Restart left queued would be taken at once by the live plugin's
	// watch and shut it down, which a clock plugin finishes well within
	// this.
	time.Sleep(3 * time.Second)
	logs := m.Logs("p0")
	if n, r := countLines(logs, "starting plugin"), countLines(logs, "restart requested"); n != 2 || r != 1 {
		t.Errorf("%d launches and %d restarts for one restart:\n%s", n, r, strings.Join(logs, "\n"))
	}
}

func TestExitStatusOfFailedLaunch(t *testing.T) {
	dir := install(t, "dies-configuring")
	logger, _ := newLogger()
	opts := fast
	opts.MaxAttempts = 1
	m := StartWith(panels("dies-configuring"), dir, logger, opts)
	defer m.Close()
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	if s.ExitStatus != "exit status 4" {
		t.Errorf("got exit status %q, want exit status 4", s.ExitStatus)
	}
	if countLines(m.Logs("p0"), "plugin exited: exit status 4") != 1 {
		t.Errorf("exit not logged:\n%s", strings.Join(m.Logs("p0"), "\n"))
	}
}

func TestExitStatusIsThisAttempts(t *testing.T) {
	dir := install(t, "crash-then-hang")
	logger, _ := newLogger()
	opts := fast
	opts.MaxAttempts = 2
	m := StartWith(panels("crash-then-hang"), dir, logger, opts)
	defer m.Close()
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	if !strings.Contains(s.Err.Error(), "hung") || s.ExitStatus != "" {
		t.Errorf("got %v with exit status %q; want the hang, with no status", s.Err, s.ExitStatus)
	}
}

func TestLivenessCheckCatchesFrozenPlugin(t *testing.T) {
	if !canFreeze {
		t.Skip("cannot stop a process on this OS")
	}
	dir := install(t, "freezes-live")
	logger, _ := newLogger()
	opts := fast
	opts.PingInterval = 100 * time.Millisecond
	m := StartWith(panels("freezes-live"), dir, logger, opts)
	defer m.Close()
	s := waitFor(t, m, "p0", "restarting", is(Restarting))
	if !strings.Contains(s.Err.Error(), "hung: health check did not answer") {
		t.Errorf("got %v", s.Err)
	}
}

func TestDescribeUsesCallTimeout(t *testing.T) {
	dir := install(t, "slow-describe")
	logger, _ := newLogger()
	opts := fast
	opts.MaxAttempts = 1
	start := time.Now()
	m := StartWith(panels("slow-describe"), dir, logger, opts)
	defer m.Close()
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("a hung Describe took %v with a %v call timeout", d, opts.CallTimeout)
	}
	if !strings.Contains(s.Err.Error(), "hung: Describe did not answer") {
		t.Errorf("got %v", s.Err)
	}
}

func TestUnhealthyPluginIsRestarted(t *testing.T) {
	dir := install(t, "not-serving")
	logger, _ := newLogger()
	opts := fast
	opts.PingInterval = 100 * time.Millisecond
	m := StartWith(panels("not-serving"), dir, logger, opts)
	defer m.Close()
	s := waitFor(t, m, "p0", "restarting", is(Restarting))
	if !strings.Contains(s.Err.Error(), "failed its health check: plugin reports NOT_SERVING") {
		t.Errorf("got %v", s.Err)
	}
}

func TestFailedHandshakeHasNoExitStatus(t *testing.T) {
	t.Parallel()
	dir := install(t, "junk-stdout")
	logger, _ := newLogger()
	_, exit, err := launch(context.Background(), dir, "junk-stdout", ignore, logger, callTimeout, nil)
	if err == nil || !strings.Contains(err.Error(), "Unrecognized remote plugin message") {
		t.Fatalf("got %v, want a handshake error", err)
	}
	if exit != "" {
		t.Errorf("got exit status %q for a plugin the host killed", exit)
	}
}

func TestNoHandshakeIsAHang(t *testing.T) {
	t.Parallel() // waits startTimeout
	dir := install(t, "slow-start")
	logger, _ := newLogger()
	_, exit, err := launch(context.Background(), dir, "slow-start", ignore, logger, callTimeout, nil)
	var hung hangError
	if !errors.As(err, &hung) || !strings.Contains(err.Error(), "hung: no handshake within") {
		t.Fatalf("got %v, want a hang", err)
	}
	if exit != "" {
		t.Errorf("got exit status %q", exit)
	}
}

func TestBackoffIsCapped(t *testing.T) {
	o := Options{Backoff: time.Second, MaxAttempts: 100}.withDefaults()
	for _, tt := range []struct {
		attempt int
		want    time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {5, 16 * time.Second}, {9, 256 * time.Second}, {10, maxBackoff}, {80, maxBackoff}} {
		if got := o.backoff(tt.attempt); got != tt.want {
			t.Errorf("attempt %d: got %v, want %v", tt.attempt, got, tt.want)
		}
	}
	if o := (Options{Backoff: -time.Second, MaxAttempts: -1}).withDefaults(); o.Backoff != time.Second || o.MaxAttempts != 5 {
		t.Errorf("negative options not defaulted: %+v", o)
	}
}

func TestExitStatusOfStartupCrash(t *testing.T) {
	dir := install(t, "dies-at-start", "fails-at-start")
	logger, _ := newLogger()
	opts := fast
	opts.MaxAttempts = 1
	m := StartWith(panels("dies-at-start", "fails-at-start"), dir, logger, opts)
	defer m.Close()
	// Status 1 is also what Windows's fallback kill gives: a kill that
	// hits the exited plugin must not be taken for its cause.
	if s := waitFor(t, m, "p1", "crashed", is(Crashed)); s.ExitStatus != "exit status 1" {
		t.Errorf("got exit status %q, want exit status 1", s.ExitStatus)
	}
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	if s.ExitStatus != "exit status 7" {
		t.Errorf("got exit status %q, want exit status 7", s.ExitStatus)
	}
	logs := m.Logs("p0")
	for _, want := range []string{"PLUGIN  boom", "plugin exited: exit status 7"} {
		if countLines(logs, want) == 0 {
			t.Errorf("logs lack %q:\n%s", want, strings.Join(logs, "\n"))
		}
	}
	for _, l := range logs {
		if strings.Contains(l, "\n") {
			t.Errorf("a Logs entry spans lines: %q", l)
		}
	}
}

func TestPluginsOwnDeadlineIsNoHang(t *testing.T) {
	dir := install(t, "upstream-timeout")
	logger, _ := newLogger()
	opts := fast
	t.Setenv("GORACE", "atexit_sleep_ms=0") // a race-built plugin waits 1s to exit
	start := time.Now()
	m := StartWith(panels("upstream-timeout"), dir, logger, opts)
	defer m.Close()
	s := waitFor(t, m, "p0", "crashed", is(Crashed))
	if strings.Contains(s.Err.Error(), "hung") || !strings.Contains(s.Err.Error(), "weather service did not answer") {
		t.Errorf("got %v, want the plugin's own error, not a hang", s.Err)
	}
	// A plugin that answered is not waited for to exit.
	if d := time.Since(start); d > time.Duration(opts.MaxAttempts)*time.Second {
		t.Errorf("%d failures that the plugin answered took %v\n%s", opts.MaxAttempts, d, strings.Join(m.Logs("p0"), "\n"))
	}
}

func TestNoExitStatusForHostShutdown(t *testing.T) {
	dir := install(t, "misnamed", "slow-describe")
	logger, _ := newLogger()
	opts := fast
	opts.MaxAttempts = 1
	m := StartWith(panels("misnamed", "slow-describe"), dir, logger, opts)
	defer m.Close()
	for _, id := range []string{"p0", "p1"} {
		s := waitFor(t, m, id, "crashed", is(Crashed))
		if s.ExitStatus != "" || countLines(m.Logs(id), "plugin exited") != 0 {
			t.Errorf("%s: exit status %q for a plugin the host shut down:\n%s", s.Plugin, s.ExitStatus, strings.Join(m.Logs(id), "\n"))
		}
	}
}

func TestRestartWhileStarting(t *testing.T) {
	dir := install(t, "hangs-configure")
	logger, _ := newLogger()
	opts := fast
	opts.CallTimeout = 3 * time.Second
	m := StartWith(panels("hangs-configure"), dir, logger, opts)
	defer m.Close()
	waitFor(t, m, "p0", "starting", is(Starting))
	if err := m.Restart("p0"); !errors.Is(err, ErrStarting) {
		t.Errorf("got %v, want ErrStarting", err)
	}
}

func TestQueuedRestartResetsFailures(t *testing.T) {
	dir := install(t, "crashes")
	logger, _ := newLogger()
	opts := fast
	opts.MaxAttempts = 2
	launches := 0
	beforeLaunchHook = func(pn *panel) {
		launches++
		if launches == 2 { // a Restart that lands as the backoff ends
			pn.restart <- struct{}{}
		}
	}
	defer func() { beforeLaunchHook = func(*panel) {} }()
	m := StartWith(panels("crashes"), dir, logger, opts)
	defer m.Close()
	waitFor(t, m, "p0", "crashed", is(Crashed))
	// Without the reset, the second launch's failure is the second in a
	// row and the last; with it, a third launch follows.
	if n := countLines(m.Logs("p0"), "starting plugin"); n != 3 {
		t.Errorf("%d launches, want 3:\n%s", n, strings.Join(m.Logs("p0"), "\n"))
	}
}

func TestRestartLeavesLiveAtOnce(t *testing.T) {
	if !canFreeze {
		t.Skip("cannot stop a process on this OS")
	}
	dir := install(t, "freezes-live") // never answers Shutdown, so Close takes seconds
	logger, _ := newLogger()
	m := StartWith(panels("freezes-live"), dir, logger, fast) // no health check in the test's time
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	m.mu.Lock()
	first := m.panels[0].inst
	m.mu.Unlock()
	time.Sleep(500 * time.Millisecond) // frozen by now
	start := time.Now()
	if err := m.Restart("p0"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "p0", "stopping", is(Stopping))
	if d := time.Since(start); d > time.Second {
		t.Errorf("the panel stayed Live for %v after Restart", d)
	}
	waitFor(t, m, "p0", "starting again", is(Starting))
	if !first.Exited() {
		t.Error("the next plugin started while the old one still ran")
	}
}

func TestRestartEndsHealthCheck(t *testing.T) {
	if !canFreeze {
		t.Skip("cannot stop a process on this OS")
	}
	dir := install(t, "freezes-live")
	logger, _ := newLogger()
	opts := fast
	opts.CallTimeout = 5 * time.Second
	opts.PingInterval = 100 * time.Millisecond
	m := StartWith(panels("freezes-live"), dir, logger, opts)
	defer m.Close()
	waitFor(t, m, "p0", "live", is(Live))
	time.Sleep(600 * time.Millisecond) // frozen, with a health check waiting on it
	start := time.Now()
	if err := m.Restart("p0"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "p0", "no longer live", func(s PanelState) bool { return s.State != Live })
	if d := time.Since(start); d > time.Second {
		t.Errorf("Restart took %v to end the health check", d)
	}
}

// TestExitLeavesLiveAtOnce has a plugin exit while a child that left its
// group holds its stderr, so the reap that gives its status waits
// pipeGrace.
func TestExitLeavesLiveAtOnce(t *testing.T) {
	if !canFreeze || !canAwaitExit {
		t.Skip("needs process groups and an exit watcher")
	}
	dir := install(t, "escapes")
	logger, _ := newLogger()
	opts := fast
	opts.MaxAttempts = 1
	m := StartWith(panels("escapes"), dir, logger, opts)
	defer m.Close()
	defer func() {
		for _, l := range m.Logs("p0") {
			var pid int
			if _, err := fmt.Sscanf(l[strings.Index(l, "PLUGIN  ")+len("PLUGIN  "):], "child %d", &pid); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					p.Kill()
				}
			}
		}
	}()
	waitFor(t, m, "p0", "live", is(Live))
	m.mu.Lock()
	inst := m.panels[0].inst
	m.mu.Unlock()
	<-inst.Done()
	start := time.Now()
	waitFor(t, m, "p0", "no longer live", func(s PanelState) bool { return s.State != Live })
	if d := time.Since(start); d > pipeGrace/2 {
		t.Errorf("the panel stayed Live for %v after its plugin exited", d)
	}
}

func TestMissingInterpreterIsPermanent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no #! scripts on Windows")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clock"), []byte("#!/nonexistent/interpreter\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger, _ := newLogger()
	_, err := Launch(context.Background(), dir, "clock", ignore, logger)
	if err == nil || Retryable(err) || !strings.Contains(err.Error(), "cannot be run") {
		t.Fatalf("got %v, want a permanent error", err)
	}
}

func TestUnreadableDirIsPermanent(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions that bind")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	_, err := find(dir, "clock")
	if err == nil || Retryable(err) {
		t.Fatalf("got %v, want a permanent error", err)
	}
}

func TestFailedLaunchShowsStopping(t *testing.T) {
	if !canFreeze {
		t.Skip("cannot stop a process on this OS")
	}
	dir := install(t, "freezes-configuring") // so Close takes seconds
	logger, _ := newLogger()
	m := StartWith(panels("freezes-configuring"), dir, logger, fast)
	defer m.Close()
	s := waitFor(t, m, "p0", "stopping", is(Stopping))
	if s.Err == nil || !strings.Contains(s.Err.Error(), "hung: Configure") {
		t.Errorf("got %v", s.Err)
	}
	if countLines(m.Logs("p0"), "stopping plugin: ") == 0 {
		t.Errorf("Stopping shown before Logs say why:\n%s", strings.Join(m.Logs("p0"), "\n"))
	}
	if err := m.Restart("p0"); err != nil {
		t.Errorf("Restart while stopping: %v", err)
	}
}

func TestFencedOutputSkipsLogs(t *testing.T) {
	logger, logs := newLogger()
	m := &Manager{logger: logger}
	pn := &panel{PanelState: PanelState{ID: "p0"}, logs: newRing(10)}
	before, after := m.output(pn), m.output(pn)
	before("kept")
	pn.fence()
	after("late") // as a callback stalled past its launch's Close
	if got := pn.logs.lines(); len(got) != 1 || !strings.HasSuffix(got[0], "PLUGIN  kept") {
		t.Errorf("Logs after fence: %q", got)
	}
	if !strings.Contains(logs.String(), "[panel p0] late") {
		t.Errorf("the host log lost a fenced line: %q", logs.String())
	}
	m.output(pn)("next")
	if got := pn.logs.lines(); len(got) != 2 || !strings.HasSuffix(got[1], "PLUGIN  next") {
		t.Errorf("Logs of the next launch: %q", got)
	}
}

func TestRing(t *testing.T) {
	r := newRing(3)
	for i := range 5 {
		r.add("HOST", fmt.Sprint(i))
	}
	got := r.lines()
	if len(got) != 3 || !strings.HasSuffix(got[0], "HOST  2") || !strings.HasSuffix(got[2], "HOST  4") {
		t.Fatalf("got %q", got)
	}
	r.add("PLUGIN", strings.Repeat("x", logLine-4)+"ééé") // the cut falls inside the first é
	last := r.lines()[2]
	if line := last[strings.Index(last, "PLUGIN  ")+len("PLUGIN  "):]; line != strings.Repeat("x", logLine-4)+"…" {
		t.Errorf("a long line became %d bytes: …%q", len(line), line[max(0, len(line)-8):])
	}
}
