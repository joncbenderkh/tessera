// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"reflect"
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
	states := settle(t, m)

	if len(states) != 2 {
		t.Fatalf("want the 2 enabled panels, got %+v", states)
	}
	for _, s := range states {
		if s.State != Ready || s.Info.GetName() != "clock" {
			t.Errorf("panel %s: got %v %v, want ready clock", s.ID, s.State, s.Err)
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
	// Checked after Close, which waits for the plugins' output.
	for _, want := range []string{
		"[panel a] configured a map[n:map[x:1] timezone:UTC] 2x1",
		"[panel b] configured b map[] 1x1",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
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
		if s := states[i]; s.State != Failed || s.Err == nil || s.Err.Error() != want {
			t.Errorf("panel %s: got %v %v, want failed: %s", s.ID, s.State, s.Err, want)
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
		{"proto-two", `plugin "proto-two" failed to start (this host speaks protocol 1)`},
		{"plaintext", `plugin "plaintext": Describe: rpc error: code = Unavailable`},
		{"view-nine", `plugin "view-nine" chose view schema 9; this host renders [1]`},
		{"slow-describe", `plugin "slow-describe": Describe: rpc error: code = DeadlineExceeded`},
		{"noexec", `plugin "noexec": ` + filepath.Join(dir, "noexec") + ` is not executable`},
		{"Bad", `plugin "Bad" must be lowercase`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "noexec" && exeSuffix() != "" {
				t.Skip("no executable bit on Windows")
			}
			t.Parallel()
			inst, err := Launch(context.Background(), dir, tt.name, &bytes.Buffer{}, logger)
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
		t.Skip("cannot stop a process on this OS")
	}
	dir := install(t, "frozen")
	logger, _ := newLogger()
	inst, err := Launch(context.Background(), dir, "frozen", &bytes.Buffer{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // until it has stopped itself
	start := time.Now()
	inst.Close()
	if d := time.Since(start); d > killTimeout+2*time.Second {
		t.Errorf("Close took %v", d)
	}
	if !inst.Exited() {
		t.Error("plugin still running after Close")
	}
}

func TestDescribe(t *testing.T) {
	dir := install(t, "clock")
	logger, _ := newLogger()
	info, err := Describe(context.Background(), dir, "clock", &bytes.Buffer{}, logger)
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
		"con" + exeSuffix():     0o755,
		"notes" + exeSuffix():   0o644,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"+exeSuffix()), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"clock", "weather"}
	if exeSuffix() != "" {
		want = []string{"clock", "notes", "weather"} // no executable bit
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
