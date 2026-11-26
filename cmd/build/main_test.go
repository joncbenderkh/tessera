// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
)

// TestMain makes the test binary a host that exits with 7 on SIGTERM,
// when TestRunHostPassesSignal runs it as one, that kills itself, or
// that tells whether the terminal can stop it.
func TestMain(m *testing.M) {
	if os.Getenv("BUILD_TEST_HOST_BACKGROUND") != "" {
		if backgroundSafe() {
			os.Exit(0)
		}
		os.Exit(4)
	}
	if os.Getenv("BUILD_TEST_HOST_KILLED") != "" {
		self, _ := os.FindProcess(os.Getpid())
		self.Kill()
		time.Sleep(time.Minute)
	}
	if ready := os.Getenv("BUILD_TEST_HOST_READY"); ready != "" {
		terms := make(chan os.Signal, 1)
		signal.Notify(terms, syscall.SIGTERM)
		// Renamed into place, so the test never reads it half written.
		if os.WriteFile(ready+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600) != nil || os.Rename(ready+".tmp", ready) != nil {
			os.Exit(2)
		}
		select {
		case <-terms:
			os.Exit(7)
		case <-time.After(time.Minute):
			os.Exit(3)
		}
	}
	os.Exit(m.Run())
}

// A signal the build receives reaches the host it runs, which is in a
// process group of its own.
func TestRunHostPassesSignal(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("BUILD_TEST_HOST_READY", ready)
	signals := make(chan os.Signal, 1)
	code := make(chan int, 1)
	go func() { code <- runHost(os.Args[0], nil, signals) }()
	data, err := os.ReadFile(ready)
	for ; err != nil; data, err = os.ReadFile(ready) {
		time.Sleep(10 * time.Millisecond)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	checkOwnGroup(t, pid)
	signals <- syscall.SIGTERM
	select {
	case got := <-code:
		if got != 7 {
			t.Errorf("host exited with %d, want 7 (stopped by SIGTERM)", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("host still running after SIGTERM")
	}
}

// A signal before the watch ends stops the build, even one still
// waiting when it ends, and is not handed on; a later one is.
func TestWatch(t *testing.T) {
	signals := make(chan os.Signal, 1)
	ctx, stop := watch(signals)
	signals <- syscall.SIGTERM
	<-ctx.Done()
	if !stop() || len(signals) != 0 {
		t.Errorf("signal during the build: stopped %v, %d left for the host", ctx.Err() != nil, len(signals))
	}

	// Queued as the watch ends, when its goroutine may not have run.
	signals = make(chan os.Signal, 1)
	_, stop = watch(signals)
	signals <- syscall.SIGTERM
	if !stop() || len(signals) != 0 {
		t.Errorf("signal as the build ends: not stopped, or %d left for the host", len(signals))
	}

	signals = make(chan os.Signal, 1)
	ctx, stop = watch(signals)
	if stop() {
		t.Error("stopped without a signal")
	}
	signals <- syscall.SIGTERM
	if ctx.Err() != nil || len(signals) != 1 {
		t.Errorf("signal after the build: stopped %v, %d left for the host", ctx.Err() != nil, len(signals))
	}
}

// Every first-party plugin is installed under the name it gives itself,
// which the host finds, and nothing is left beside them; a relative
// destination is read in the working directory, not the module root.
func TestInstallPlugins(t *testing.T) {
	if testing.Short() {
		t.Skip("builds every plugin")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	dest := filepath.Join(dir, "plugins")
	names, err := installPlugins(t.Context(), root, "plugins")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := pluginhost.List(dest)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"calendar", "clock", "disk", "greeting", "month", "network", "notes", "processes", "services", "system", "weather", "worldclock"}
	for _, name := range want {
		if !slices.Contains(names, name) {
			t.Errorf("installed %q, missing %q", names, name)
		}
	}
	if !slices.Equal(names, listed) {
		t.Errorf("installed %q, host lists %q", names, listed)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Errorf("plugins directory holds %d entries, want %d", len(entries), len(names))
	}
}

// An installed plugin replaces the one before it.
func TestInstallReplaces(t *testing.T) {
	staging, dest := t.TempDir(), t.TempDir()
	for dir, content := range map[string]string{staging: "new", dest: "old"} {
		if err := os.WriteFile(filepath.Join(dir, "clock"), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	names, err := install(staging, dest)
	if err != nil || !slices.Equal(names, []string{"clock"}) {
		t.Fatalf("install: %q, %v", names, err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "clock")); err != nil || string(got) != "new" {
		t.Errorf("clock holds %q, %v; want new", got, err)
	}
}

// The host replaces the old one only once it is built, and a build
// stopped before go build starts leaves the old one and no staging.
func TestBuildHost(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":              "module example.com/host\n\ngo 1.25.0\n",
		"cmd/tessera/main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	host := filepath.Join(root, "tessera")
	if err := os.WriteFile(host, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	checkHost := func(wantOld bool) {
		t.Helper()
		got, err := os.ReadFile(host)
		if err != nil || (string(got) == "old") != wantOld {
			t.Errorf("host holds %d bytes, %v; want old: %v", len(got), err, wantOld)
		}
		if left, _ := filepath.Glob(filepath.Join(root, ".build-*")); len(left) > 0 {
			t.Errorf("staging left: %q", left)
		}
	}
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	if err := buildHost(stopped, root, host); err == nil {
		t.Error("stopped buildHost succeeded")
	}
	checkHost(true)
	if err := buildHost(context.Background(), root, host); err != nil {
		t.Fatal(err)
	}
	checkHost(false)
}

// The host runs on the installed plugins, by an absolute path, unless
// the arguments after -- say otherwise.
func TestHostArgs(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	got, err := hostArgs("plugins", []string{"--config", "x.yaml"})
	want := []string{"--plugins", filepath.Join(dir, "plugins"), "--config", "x.yaml"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("hostArgs: %q, %v; want %q", got, err, want)
	}
}

// Only --run takes arguments, and only after --, so a mistyped one is
// not passed on to the host, which would refuse it.
func TestCheckArgs(t *testing.T) {
	for _, c := range []struct {
		run  bool
		dash int
		args []string
		ok   bool
	}{
		{false, -1, nil, true},
		{true, -1, nil, true},
		{true, 0, []string{"--config", "x.yaml"}, true},
		{true, -1, []string{"foo"}, false},
		{true, 1, []string{"foo", "--config", "x.yaml"}, false},
		{false, 0, []string{"--config", "x.yaml"}, false},
		{false, -1, []string{"plugins"}, false},
	} {
		if err := checkArgs(c.run, c.dash, c.args); (err == nil) != c.ok {
			t.Errorf("checkArgs(%v, %d, %q) = %v, want ok %v", c.run, c.dash, c.args, err, c.ok)
		}
	}
}

func TestModulePath(t *testing.T) {
	for goMod, want := range map[string]string{
		"module github.com/joncbenderkh/tessera\n\ngo 1.25.0\n": module,
		"// a comment\nmodule \"example.com/x\" // why\n":       "example.com/x",
		"modules are not this\n":                                "",
	} {
		if got := modulePath([]byte(goMod)); got != want {
			t.Errorf("modulePath(%q) = %q, want %q", goMod, got, want)
		}
	}
}

// A host killed by a signal gives the shell's code for it.
func TestRunHostSignalled(t *testing.T) {
	t.Setenv("BUILD_TEST_HOST_KILLED", "1")
	if got := runHost(os.Args[0], nil, nil); got != 128+9 {
		t.Errorf("killed host gives %d, want %d", got, 128+9)
	}
}

// The host, in the terminal's background, is not stopped there for
// writing to it or reading from it.
func TestRunHostInBackground(t *testing.T) {
	t.Setenv("BUILD_TEST_HOST_BACKGROUND", "1")
	if got := runHost(os.Args[0], nil, nil); got != 0 {
		t.Errorf("host can be stopped by the terminal (exit %d)", got)
	}
}

// A build stopped partway, its output half written, leaves the old host
// whole and no staging behind.
func TestStageHostStopped(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "tessera")
	if err := os.WriteFile(host, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err := stageHost(ctx, root, host, func(ctx context.Context, out string) error {
		if err := os.WriteFile(out, []byte("half"), 0o755); err != nil {
			return err
		}
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	if err == nil {
		t.Error("stopped build succeeded")
	}
	if got, err := os.ReadFile(host); err != nil || string(got) != "old" {
		t.Errorf("host holds %q, %v; want the old one", got, err)
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".build-*")); len(left) > 0 {
		t.Errorf("staging left: %q", left)
	}
}

// A go build stopped partway leaves no work directory behind.
func TestGoBuildStoppedCleans(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GOTMPDIR", "")
	root := t.TempDir()
	var src strings.Builder
	src.WriteString("package main\n\nfunc main() {}\n")
	for i := range 20000 {
		fmt.Fprintf(&src, "func f%d(x int) int { return x*%d + %d }\n", i, i, i)
	}
	for name, content := range map[string]string{
		"go.mod":  "module example.com/slow\n\ngo 1.25.0\n",
		"main.go": src.String(),
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Stopped once the slow package compiles, so the stop lands partway
	// through.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var compiling atomic.Bool
	go func() {
		for ctx.Err() == nil {
			for _, p := range processesWith(tmp) {
				if strings.Contains(p, "/compile ") && strings.Contains(p, filepath.Join(root, "main.go")) {
					compiling.Store(true)
					cancel()
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	// The compilers ignore the interrupt, as a -toolexec wrapper may
	// make them, and are stopped all the same.
	toolexec := filepath.Join(t.TempDir(), "noint")
	if err := os.WriteFile(toolexec, []byte("#!/bin/sh\ntrap '' INT\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := goBuild(ctx, root, []string{"GOCACHE=" + t.TempDir()}, "-toolexec", toolexec, "-o", filepath.Join(root, "out"), ".")
	if err == nil {
		t.Skip("the build finished before it could be stopped")
	}
	if !compiling.Load() {
		t.Fatalf("go build failed before it was stopped: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "*")); len(left) > 0 {
		t.Errorf("left in TMPDIR: %q", left)
	}
	// Nor any compiler it ran, once they have had a moment to stop.
	for deadline := time.Now().Add(250 * time.Millisecond); ; time.Sleep(25 * time.Millisecond) {
		running := processesWith(tmp)
		if len(running) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("still running: %q", running)
			break
		}
	}
}

// processesWith is the command lines of the processes whose own
// mention s.
func processesWith(s string) []string {
	var out []string
	cmdlines, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, f := range cmdlines {
		b, err := os.ReadFile(f)
		if err == nil && strings.Contains(string(b), s) {
			out = append(out, strings.ReplaceAll(string(b), "\x00", " "))
		}
	}
	return out
}

// go build's work directory goes where GOTMPDIR says.
func TestGoBuildGOTMPDIR(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}
	gotmp := t.TempDir()
	t.Setenv("GOTMPDIR", gotmp)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":  "module example.com/tmp\n\ngo 1.25.0\n",
		"main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := goBuild(context.Background(), root, []string{"GOCACHE=" + t.TempDir()}, "-o", filepath.Join(root, "out"), "."); err != nil {
		t.Fatalf("build with TMPDIR missing and GOTMPDIR set: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(gotmp, "*")); len(left) > 0 {
		t.Errorf("left in GOTMPDIR: %q", left)
	}
}

// A GOTMPDIR whose name ends in a space is used as it is.
func TestGoBuildGOTMPDIRSpace(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}
	gotmp := filepath.Join(t.TempDir(), "tmp ")
	if err := os.Mkdir(gotmp, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOTMPDIR", gotmp)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":  "module example.com/space\n\ngo 1.25.0\n",
		"main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := goBuild(context.Background(), root, []string{"GOCACHE=" + t.TempDir()}, "-o", filepath.Join(root, "out"), "."); err != nil {
		t.Fatalf("build with GOTMPDIR %q: %v", gotmp, err)
	}
}

// A GOTMPDIR set with go env -w is where go build works too.
func TestGoBuildGoEnvGOTMPDIR(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}
	gotmp := t.TempDir()
	goenv := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(goenv, []byte("GOTMPDIR="+gotmp+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOENV", goenv)
	t.Setenv("GOTMPDIR", "")
	os.Unsetenv("GOTMPDIR")
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":  "module example.com/env\n\ngo 1.25.0\n",
		"main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := goBuild(context.Background(), root, []string{"GOCACHE=" + t.TempDir()}, "-o", filepath.Join(root, "out"), "."); err != nil {
		t.Fatalf("build with GOTMPDIR in the go env file: %v", err)
	}
}

// A relative GOTMPDIR is relative to root, where go runs.
func TestGoBuildRelativeGOTMPDIR(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go build")
	}
	t.Chdir(t.TempDir())
	t.Setenv("GOTMPDIR", "tmp")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"go.mod":  "module example.com/rel\n\ngo 1.25.0\n",
		"main.go": "package main\n\nfunc main() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := goBuild(context.Background(), root, []string{"GOCACHE=" + t.TempDir()}, "-o", filepath.Join(root, "out"), "."); err != nil {
		t.Fatalf("build with a relative GOTMPDIR: %v", err)
	}
}
