// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package main

import (
	"context"
	"errors"
	"image"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/plugins/internal/metric"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
	"github.com/joncbenderkh/tessera/sdk"
)

func TestMain(m *testing.M) { plugintest.Main(m, main) }

func TestBusy(t *testing.T) {
	a := cpu.TimesStat{User: 100, System: 50, Idle: 800, Iowait: 50}
	b := cpu.TimesStat{User: 130, System: 60, Idle: 850, Iowait: 60, Guest: 20}
	if got := busy(a, b); math.Abs(got-40) > 1e-9 {
		t.Errorf("busy = %v, want 40", got)
	}
	if got := busy(b, b); !math.IsNaN(got) {
		t.Errorf("busy with no time passed = %v, want NaN", got)
	}
}

// stats returns the stat widgets of v by label.
func stats(v *tesserav1.View) map[string]*tesserav1.Stat {
	m := map[string]*tesserav1.Stat{}
	for _, w := range plugintest.Widgets(v.GetBody()) {
		if s := w.GetStat(); s != nil {
			m[s.GetLabel()] = s
		}
	}
	return m
}

func TestView(t *testing.T) {
	var cpuH, memH metric.History
	cpuH.Add(24.8)
	cpuH.Add(22.7)
	memH.Add(38.8)
	v := view(state{
		interval: 2 * time.Second,
		cores:    8,
		cpu:      cpuH.Samples(),
		memory:   memH.Samples(),
		used:     12_400 << 20,
		total:    32 << 30,
	}).Proto()
	s := stats(v)
	if c := s["CPU utilization"]; c.GetValue() != "22.7" || c.GetUnit() != "%" || c.GetCaption() != "−2.1 pp · last sample" {
		t.Errorf("CPU stat %v", c)
	}
	if m := s["Memory in use"]; m.GetValue() != "12.1" || m.GetUnit() != "GiB" || m.GetCaption() != "of 32.0 GiB · 38.8%" {
		t.Errorf("memory stat %v", m)
	}
	first := view(state{interval: time.Second, cores: 8, cpu: new(metric.History).Samples(), memory: memH.Samples()}).Proto()
	if c := stats(first)["CPU utilization"]; c.GetCaption() != "8 CPUs" {
		t.Errorf("first CPU caption %q, want 8 CPUs", c.GetCaption())
	}
	failed := view(state{interval: time.Second, cores: 8, err: errors.New("open /proc/stat: permission denied")}).Proto()
	if texts := plugintest.Texts(failed.GetBody()); !slices.Contains(texts, "Cannot read the CPU or memory") || slices.ContainsFunc(texts, func(s string) bool { return s == "open /proc/stat: permission denied" }) {
		t.Errorf("failed view texts %q", texts)
	}
}

func TestSystem(t *testing.T) {
	p := plugintest.Launch(t, "system")
	if got := p.Info.GetName(); got != "system" {
		t.Errorf("name %q, want system", got)
	}
	for _, interval := range []any{"soon", "10ms", 2.0} {
		if err := p.Configure(map[string]any{"interval": interval}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("interval %v: %v, want INVALID_ARGUMENT", interval, err)
		}
	}
	if err := p.Configure(map[string]any{"interval": "1s"}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		s := stats(v)
		return s["CPU utilization"].GetValue() != "" && s["Memory in use"].GetValue() != ""
	})
}

// TestPoll checks that the first poll is only a baseline, of memory as
// well as CPU, and that one too soon after it measures nothing.
func TestPoll(t *testing.T) {
	views := make(chan sdk.View, 3)
	s := &system{views: views, interval: time.Second}
	if next := s.poll(context.Background()); next != 500*time.Millisecond || s.cpu.Len() != 0 || s.memory.Len() != 0 {
		t.Fatalf("baseline: next %v, %d CPU and %d memory samples", next, s.cpu.Len(), s.memory.Len())
	}
	s.lastAt = time.Now() // however long the first sample took
	if next := s.poll(context.Background()); next != metric.MinElapsed || s.cpu.Len() != 0 || s.memory.Len() != 0 {
		t.Fatalf("too soon: next %v, %d CPU and %d memory samples", next, s.cpu.Len(), s.memory.Len())
	}
	time.Sleep(metric.MinElapsed)
	if next := s.poll(context.Background()); next != time.Second || s.cpu.Len() != 1 || s.memory.Len() != 1 {
		t.Fatalf("second sample: next %v, %d CPU and %d memory samples", next, s.cpu.Len(), s.memory.Len())
	}
	if len(views) != 2 {
		t.Errorf("%d views, want 2", len(views))
	}
}

// TestPollAfterFailure checks that the reading after a failed one is a
// baseline, not measured over the gap.
func TestPollAfterFailure(t *testing.T) {
	views := make(chan sdk.View, 3)
	s := &system{views: views, interval: time.Second}
	s.poll(context.Background())
	defer func(f func(context.Context, bool) ([]cpu.TimesStat, error)) { cpuTimes = f }(cpuTimes)
	cpuTimes = func(context.Context, bool) ([]cpu.TimesStat, error) { return nil, errors.New("no /proc/stat") }
	time.Sleep(metric.MinElapsed)
	s.poll(context.Background())
	cpuTimes = cpu.TimesWithContext
	time.Sleep(metric.MinElapsed)
	if next := s.poll(context.Background()); next != 500*time.Millisecond {
		t.Errorf("after a failure: next %v, want a baseline", next)
	}
}

// The views fit the least body the plugin asks for, minBody, at the
// default text size.
func TestMinBody(t *testing.T) {
	// From the narrowest body a tile has, or the least width it asks for.
	for _, width := range []int{124, 168, 300, 432, 600, 900} {
		testMinBodyAt(t, max(width, minBody.Width))
	}
}

func testMinBodyAt(t *testing.T, width int) {
	t.Helper()
	for i, v := range minBodyViews() {
		if !ui.ViewFits(v.Proto(), image.Pt(width, minBody.Height), 1) {
			t.Errorf("view %d does not fit %d by %d dp", i, width, minBody.Height)
		}
	}
}

// minBodyViews are the views minBody is measured with.
func minBodyViews() []sdk.View {
	var cpu, memory metric.History
	for i := range 60 {
		cpu.Add(float64(i))
		memory.Add(38.8)
	}
	return []sdk.View{view(state{interval: 2 * time.Second, cores: 8, cpu: cpu.Samples(), memory: memory.Samples(), used: 12_400 << 20, total: 32 << 30})}
}
