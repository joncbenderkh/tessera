// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joncbenderkh/tessera/internal/config"
)

// syncBuffer is a log destination the plugins' goroutines can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func ignore(string) {}

func newLogger() (*log.Logger, *syncBuffer) {
	var buf syncBuffer
	return log.New(&buf, "", 0), &buf
}

// settle waits until no panel is Starting.
func settle(t *testing.T, m *Manager) []PanelState {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		states := m.States()
		done := true
		for _, s := range states {
			done = done && s.State != Starting
		}
		if done {
			return states
		}
		select {
		case <-m.Changed():
		case <-deadline:
			t.Fatalf("panels still starting: %+v", states)
		}
	}
}

func TestStartBringsPanelsUp(t *testing.T) {
	dir := install(t, "clock")
	logger, logs := newLogger()
	off := false
	cfg := config.Config{Version: 1, Grid: config.Grid{Columns: 4, RowHeight: 1}, Panels: []config.Panel{
		{ID: "a", Plugin: "clock", W: 2, H: 1, Settings: config.Settings{"timezone": "UTC", "n": config.Settings{"x": int64(1)}}},
		{ID: "b", Plugin: "clock", X: 2, W: 1, H: 1},
		{ID: "hidden", Plugin: "clock", Enabled: &off, W: 1, H: 1},
	}}
	m := Start(cfg, dir, logger)
	defer m.Close() // on an early failure; Close is idempotent
	states := settle(t, m)

	if len(states) != 2 {
		t.Fatalf("want the 2 enabled panels, got %+v", states)
	}
	for _, s := range states {
		if s.State != Live || s.Info.GetName() != "clock" {
			t.Errorf("panel %s: got %v %v, want live clock", s.ID, s.State, s.Err)
		}
	}
	// The plugins report Configure over GRPCStdio, which delivers it
	// asynchronously, so wait for it before closing them.
	for _, want := range []string{
		"[panel a] configured a map[n:map[x:1] timezone:UTC] 2x1",
		"[panel b] configured b map[] 1x1",
	} {
		for start := time.Now(); !strings.Contains(logs.String(), want); {
			if time.Since(start) > 10*time.Second {
				t.Fatalf("log lacks %q:\n%s", want, logs)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	insts := []*Instance{m.panels[0].inst, m.panels[1].inst}
	start := time.Now()
	m.Close()
	if d := time.Since(start); d > killTimeout {
		t.Errorf("Close took %v", d)
	}
	for i, inst := range insts {
		if !inst.Exited() {
			t.Errorf("plugin %d still running after Close", i)
		}
	}
}

func TestStartReportsFailures(t *testing.T) {
	dir := install(t, "rejects")
	logger, logs := newLogger()
	cfg := config.Config{Panels: []config.Panel{
		{ID: "r", Plugin: "rejects", W: 1, H: 1},
		{ID: "m", Plugin: "missing", W: 1, H: 1},
	}}
	m := Start(cfg, dir, logger)
	defer m.Close()
	states := settle(t, m)
	for i, want := range []string{
		`plugin "rejects" rejected the settings: timezone: unknown`,
		`plugin "missing" is not installed in ` + dir,
	} {
		if s := states[i]; s.State != Crashed || s.Failures != 1 || s.Err == nil || s.Err.Error() != want {
			t.Errorf("panel %s: got %v after %d attempts: %v, want crashed after 1: %s", s.ID, s.State, s.Failures, s.Err, want)
		}
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q", want)
		}
	}
}

func TestLaunchRejects(t *testing.T) {
	dir := install(t, "misnamed", "proto-two", "plaintext", "view-nine", "slow-describe")
	if err := os.WriteFile(filepath.Join(dir, "noexec"+exeSuffix()), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	logger, _ := newLogger()
	for _, tt := range []struct{ name, want string }{
		{"misnamed", `plugin "misnamed" calls itself "other"`},
		{"proto-two", `plugin "proto-two" does not speak protocol 1: incompatible API version`},
		{"plaintext", `plugin "plaintext": Describe: rpc error: code = Unavailable`},
		{"view-nine", `plugin "view-nine" chose view schema 9; this host renders [1]`},
		{"slow-describe", `plugin "slow-describe" hung: Describe did not answer within 5s`},
		{"noexec", `plugin "noexec": ` + filepath.Join(dir, "noexec") + ` is not executable`},
		{"Bad", `plugin "Bad" must be lowercase`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "noexec" && exeSuffix() != "" {
				t.Skip("no executable bit on Windows")
			}
			t.Parallel()
			inst, err := Launch(context.Background(), dir, tt.name, ignore, logger)
			if err == nil {
				inst.Close()
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCloseKillsUnresponsivePlugin(t *testing.T) {
	if !canFreeze {
		t.Skip("cannot stop a process or reach its children on this OS")
	}
	dir := install(t, "frozen", "forks")
	logger, _ := newLogger()
	for _, name := range []string{"frozen", "forks"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var child childPID
			inst, err := Launch(context.Background(), dir, name, child.output, logger)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if pid := child.get(); pid != 0 && !gone(pid) {
					t.Errorf("child %d still running after Close", pid)
				}
			}()
			time.Sleep(500 * time.Millisecond) // until frozen has stopped itself
			start := time.Now()
			inst.Close()
			if d := time.Since(start); d > killTimeout+time.Second {
				t.Errorf("Close took %v", d)
			}
			if !inst.Exited() {
				t.Error("plugin still running after Close")
			}
		})
	}
}

func TestCloseStopsHandshake(t *testing.T) {
	dir := install(t, "slow-start")
	logger, logs := newLogger()
	m := Start(config.Config{Panels: []config.Panel{{ID: "s", Plugin: "slow-start", W: 1, H: 1}}}, dir, logger)
	time.Sleep(300 * time.Millisecond) // until it is in the handshake
	start := time.Now()
	m.Close()
	if d := time.Since(start); d > time.Second {
		t.Errorf("Close took %v", d)
	}
	if strings.Contains(logs.String(), "canceled") {
		t.Errorf("Close logged the launch it stopped as a failure:\n%s", logs)
	}
}

func TestLaunchCanceled(t *testing.T) {
	dir := install(t, "slow-start")
	logger, _ := newLogger()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Launch(ctx, dir, "slow-start", ignore, logger)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("got %v, want a deadline error", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Launch took %v", d)
	}
}

// childPID records the PID a forks or exits plugin reports.
type childPID struct {
	mu  sync.Mutex
	pid int
}

func (c *childPID) output(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Sscanf(line, "child %d", &c.pid)
}

func (c *childPID) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pid
}

// gone waits up to a second for pid to exit and be reaped.
func gone(pid int) bool {
	for range 20 {
		if !alive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestExitKillsChildren(t *testing.T) {
	if !canFreeze || !canAwaitExit {
		t.Skip("children of an exited plugin are killed only by a forced Close on this OS")
	}
	dir := install(t, "exits")
	logger, _ := newLogger()
	var child childPID
	inst, err := Launch(context.Background(), dir, "exits", child.output, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	for start := time.Now(); !inst.Exited() && time.Since(start) < 10*time.Second; {
		time.Sleep(20 * time.Millisecond) // the plugin exits 200ms after Describe
	}
	if !inst.Exited() {
		t.Fatal("plugin did not exit")
	}
	if pid := child.get(); pid == 0 || !gone(pid) {
		t.Errorf("child %d of an exited plugin still running before Close", pid)
	}
}

// TestExitSeenWhileChildHoldsStderr runs wherever an exit watcher
// does, Windows included: there a child is never killed with its
// plugin, so a plain child holds the plugin's stderr after it exits.
func TestExitSeenWhileChildHoldsStderr(t *testing.T) {
	if !canAwaitExit {
		t.Skip("no exit watcher on this OS")
	}
	dir := install(t, "exits")
	logger, _ := newLogger()
	var child childPID
	inst, err := Launch(context.Background(), dir, "exits", child.output, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	defer func() {
		if p, err := os.FindProcess(child.get()); err == nil && child.get() != 0 {
			p.Kill()
		}
	}()
	select {
	case <-inst.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("exit not seen while a child holds the plugin's stderr")
	}
	time.Sleep(pipeGrace + drainTimeout + 500*time.Millisecond) // watch reaps and drains
	if s := inst.ExitStatus(); s != "exit status 0" {
		t.Errorf("got exit status %q, want exit status 0", s)
	}
}

func TestExitSeenDespiteEscapedChild(t *testing.T) {
	if !canFreeze || !canAwaitExit {
		t.Skip("needs process groups and an exit watcher")
	}
	dir := install(t, "escapes")
	logger, _ := newLogger()
	var child childPID
	runtime.GC() // release the handles of plugins earlier tests dropped
	before := pidfds()
	inst, err := Launch(context.Background(), dir, "escapes", child.output, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close() // on an early failure; Close is idempotent
	// The child left the group, so nothing else ends it.
	defer func() {
		if p, err := os.FindProcess(child.get()); err == nil && child.get() != 0 {
			p.Kill()
		}
	}()
	for start := time.Now(); !inst.Exited() && time.Since(start) < 10*time.Second; {
		time.Sleep(20 * time.Millisecond) // the plugin exits 200ms after Describe
	}
	if !inst.Exited() {
		t.Fatal("exited plugin reported as running while an escaped child holds its stderr")
	}
	// Close at once, before watch has reaped the plugin; watch still
	// releases the pipes, so go-plugin finishes.
	start := time.Now()
	inst.Close()
	if d := time.Since(start); d > pipeGrace+drainTimeout+time.Second {
		t.Errorf("Close of an exited plugin took %v", d)
	}
	if !inst.client.Exited() {
		t.Error("go-plugin still waiting on the pipes the escaped child holds")
	}
	if n := pidfds(); n > before {
		t.Errorf("%d pidfds open after Close, %d before Launch", n, before)
	}
}

func TestCloseEndsEscapedPipesWhenWatchFails(t *testing.T) {
	if !canFreeze {
		t.Skip("needs process groups")
	}
	defer func(f func(*os.Process) bool) { awaitExitHook = f }(awaitExitHook)
	awaitExitHook = func(*os.Process) bool { return false }
	dir := install(t, "escapes")
	logger, _ := newLogger()
	var child childPID
	inst, err := Launch(context.Background(), dir, "escapes", child.output, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if p, err := os.FindProcess(child.get()); err == nil && child.get() != 0 {
			p.Kill()
		}
	}()
	time.Sleep(500 * time.Millisecond) // the plugin exits 200ms after Describe
	start := time.Now()
	inst.Close()
	if d := time.Since(start); d > killTimeout+reapTimeout+pipeTimeout+time.Second {
		t.Errorf("Close took %v", d)
	}
	select {
	case <-inst.runner.reaped:
	case <-time.After(2 * time.Second):
		t.Error("plugin left unreaped, and its start thread held, after Close")
	}
}

func TestCancelWithEscapedChild(t *testing.T) {
	if !canFreeze || !canAwaitExit {
		t.Skip("needs process groups and an exit watcher")
	}
	dir := install(t, "escapes-early")
	logger, _ := newLogger()
	var child childPID
	defer func() {
		if p, err := os.FindProcess(child.get()); err == nil && child.get() != 0 {
			p.Kill()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Launch(ctx, dir, "escapes-early", child.output, logger)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("got %v, want a deadline error", err)
	}
	if d := time.Since(start); d > pipeGrace+drainTimeout+2*time.Second {
		t.Errorf("Launch took %v after its context ended", d)
	}
}

// TestEarlyReapKeepsOutput stalls the host's reading of an exited
// plugin's stderr past pipeGrace, so watch reaps the plugin while its
// last line is still unread.
func TestEarlyReapKeepsOutput(t *testing.T) {
	if !canFreeze || !canAwaitExit {
		t.Skip("needs process groups and an exit watcher")
	}
	dir := install(t, "escapes")
	logger, _ := newLogger()
	var child childPID
	lastWords := make(chan struct{}, 1)
	inst, err := Launch(context.Background(), dir, "escapes", func(line string) {
		child.output(line)
		switch {
		case strings.HasPrefix(line, "child "):
			time.Sleep(pipeGrace + time.Second) // a stalled log
		case line == "last words":
			lastWords <- struct{}{}
		}
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	defer func() {
		if p, err := os.FindProcess(child.get()); err == nil && child.get() != 0 {
			p.Kill()
		}
	}()
	select {
	case <-lastWords:
	case <-time.After(10 * time.Second):
		t.Fatal("the plugin's last line was lost")
	}
}

func TestCloseKillsDetachedChildren(t *testing.T) {
	if !canFreeze || !canAwaitExit {
		t.Skip("children of a plugin that exits on Shutdown survive on this OS")
	}
	dir := install(t, "detached")
	logger, _ := newLogger()
	var child childPID
	inst, err := Launch(context.Background(), dir, "detached", child.output, logger)
	if err != nil {
		t.Fatal(err)
	}
	inst.Close()
	if pid := child.get(); pid == 0 || !gone(pid) {
		t.Errorf("child %d of a plugin that exited on Shutdown still running", pid)
	}
}

func TestCloseKillsPluginWhenWatchFails(t *testing.T) {
	if !canFreeze {
		t.Skip("cannot stop a process on this OS")
	}
	defer func(f func(*os.Process) bool) { awaitExitHook = f }(awaitExitHook)
	awaitExitHook = func(*os.Process) bool { return false }
	dir := install(t, "mute-frozen")
	logger, _ := newLogger()
	inst, err := Launch(context.Background(), dir, "mute-frozen", ignore, logger)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // until it has closed its output and stopped
	inst.Close()
	if !inst.Exited() {
		t.Fatal("plugin still running after Close, with no exit watcher")
	}
}

func TestCanceledStartClosesPipes(t *testing.T) {
	r := &procRunner{path: os.Args[0]}
	if _, err := r.runnerFunc(nil, exec.Command(""), ""); err != nil {
		t.Fatal(err)
	}
	r.kill()
	if err := r.Start(context.Background()); err == nil {
		t.Fatal("Start ran after kill")
	}
	for i, f := range r.childEnds {
		if _, err := f.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
			t.Errorf("child end %d: got %v, want it closed", i, err)
		}
	}
	for i, rc := range []io.Reader{r.stdout, r.stderr} {
		if _, err := rc.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Errorf("read end %d: got %v, want it closed", i, err)
		}
	}
}

func TestCloseDespiteStalledOutput(t *testing.T) {
	if !canFreeze {
		t.Skip("needs process groups")
	}
	dir := install(t, "detached")
	logger, _ := newLogger()
	stall := make(chan struct{})
	defer close(stall)
	var child childPID
	inst, err := Launch(context.Background(), dir, "detached", func(line string) {
		child.output(line)
		<-stall // the host's log never returns
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // until the callback has stalled
	start := time.Now()
	inst.Close()
	if d := time.Since(start); d > killTimeout+reapTimeout+pipeTimeout+time.Second {
		t.Errorf("Close took %v", d)
	}
	if pid := child.get(); pid != 0 && !gone(pid) {
		t.Errorf("child %d still running", pid)
	}
}

func TestFailedStartCleansUp(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp) // Windows
	t.Setenv("TEMP", tmp)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "junk"+exeSuffix()), []byte("not a program\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	logger, _ := newLogger()
	_, err := Launch(context.Background(), dir, "junk", ignore, logger)
	if err == nil || !strings.Contains(err.Error(), "plugin \"junk\" failed to start: ") || strings.Contains(err.Error(), "protocol") {
		t.Fatalf("got %v, want a start error without a protocol hint", err)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("left in TMPDIR: %v", entries)
	}
}

func TestStdoutFloodDoesNotBlock(t *testing.T) {
	dir := install(t, "floods-stdout")
	logger, _ := newLogger()
	flooded := make(chan struct{}, 1)
	inst, err := Launch(context.Background(), dir, "floods-stdout", func(line string) {
		if line == "flooded" {
			flooded <- struct{}{}
		}
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	select {
	case <-flooded:
	case <-time.After(10 * time.Second):
		t.Fatal("plugin blocked writing to stdout")
	}
}

func TestPluginDiesWithHost(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux kills a plugin when its host dies")
	}
	dir := install(t, "clock")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	host := exec.Command(self)
	// Its own TMPDIR: the killed host cannot remove go-plugin's socket directory.
	host.Env = append(os.Environ(), hostKey+"="+dir, "TMPDIR="+t.TempDir())
	out, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Fscan(out, &pid); err != nil {
		host.Process.Kill()
		t.Fatalf("host did not report a plugin PID: %v", err)
	}
	host.Process.Kill()
	host.Wait()
	if !gone(pid) {
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill()
		}
		t.Fatalf("plugin %d outlived its SIGKILLed host", pid)
	}
}

func TestLineLimiter(t *testing.T) {
	long := bytes.Repeat([]byte("x"), 2*maxStdoutLine+10)
	in := append([]byte("1|1|unix|/s|grpc|cert\n"), long...)
	got, err := io.ReadAll(&lineLimiter{ReadCloser: io.NopCloser(bytes.NewReader(in))})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(got), "\n")
	if lines[0] != "1|1|unix|/s|grpc|cert" || len(lines) != 4 || len(lines[1]) != maxStdoutLine || len(lines[3]) != 10 {
		t.Fatalf("got lines of %d", len(lines))
	}
}

func TestLineWriter(t *testing.T) {
	var got []string
	w := &lineWriter{output: func(line string) { got = append(got, line) }}
	w.Write([]byte("one\ntw"))
	w.Write([]byte("o\nthree"))
	w.Flush()
	w.Flush()
	if want := []string{"one", "two", "three"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	got = nil
	w.Write([]byte("crlf\r\nbare\r"))
	w.Flush()
	if want := []string{"crlf", "bare\r"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	got = nil
	w.mu.Lock() // as Write holds it while a stalled callback runs
	flushed := make(chan struct{})
	go func() { w.Flush(); close(flushed) }()
	select {
	case <-flushed:
	case <-time.After(time.Second):
		t.Fatal("Flush waited for a busy Write")
	}
	w.mu.Unlock()

	got = nil
	w.Write(append(bytes.Repeat([]byte("x"), maxLine-1), "é\n"...))
	if len(got) != 2 || len(got[0]) != maxLine-1 || got[1] != "é" {
		t.Fatalf("a character across the cap: got pieces %d long", len(got))
	}

	got = nil
	w.Write(bytes.Repeat([]byte("x"), 2*maxLine+1))
	w.Flush()
	if len(got) != 3 || len(got[0]) != maxLine || len(got[1]) != maxLine || got[2] != "x" {
		t.Fatalf("a line of 2*maxLine+1 bytes: got pieces of %d bytes", len(got))
	}
}

func TestDescribe(t *testing.T) {
	dir := install(t, "clock")
	logger, _ := newLogger()
	info, err := Describe(context.Background(), dir, "clock", ignore, logger)
	if err != nil {
		t.Fatal(err)
	}
	if info.GetName() != "clock" || info.GetViewVersion() != 1 {
		t.Fatalf("unexpected description: %v", info)
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	for name, mode := range map[string]os.FileMode{
		"clock" + exeSuffix():   0o755,
		"weather" + exeSuffix(): 0o755,
		"clock.py":              0o755,
		"Bad" + exeSuffix():     0o755,
		"notes" + exeSuffix():   0o644,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"+exeSuffix()), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []string{"clock", "weather"}
	if exeSuffix() != "" {
		// No executable bit, and the suffix in any case.
		if err := os.WriteFile(filepath.Join(dir, "shout.EXE"), nil, 0o755); err != nil {
			t.Fatal(err)
		}
		want = []string{"clock", "notes", "shout", "weather"}
	}
	got, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, err := List(filepath.Join(dir, "missing")); err != nil || got != nil {
		t.Fatalf("missing dir: got %v, %v", got, err)
	}
}

func TestToStruct(t *testing.T) {
	s, err := toStruct(config.Settings{
		"nested": config.Settings{"list": []any{config.Settings{"a": int64(1)}, "x"}},
		"f":      2.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"nested": map[string]any{"list": []any{map[string]any{"a": 1.0}, "x"}}, "f": 2.5}
	if got := s.AsMap(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if s, err := toStruct(nil); err != nil || len(s.GetFields()) != 0 {
		t.Fatalf("nil settings: got %v, %v", s, err)
	}
}
