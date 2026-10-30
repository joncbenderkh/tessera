// SPDX-License-Identifier: GPL-3.0-or-later

package metric

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseInterval(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"2s", 2 * time.Second},
		{"1s", time.Second},
		{"1h", time.Hour},
		{"1m30s", 90 * time.Second},
	} {
		if got, err := ParseInterval(tc.in); err != nil || got != tc.want {
			t.Errorf("ParseInterval(%q) = %v, %v, want %v", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "2", "fast", "999ms", "-1s", "61m"} {
		if _, err := ParseInterval(in); err == nil {
			t.Errorf("ParseInterval(%q) succeeded", in)
		}
	}
}

func TestHistory(t *testing.T) {
	var h History
	if s := h.Samples(); len(s) != HistoryLen || !math.IsNaN(s[0]) || !math.IsNaN(s[HistoryLen-1]) {
		t.Fatalf("empty history samples %v", s)
	}
	for i := range HistoryLen + 5 {
		h.Add(float64(i))
	}
	s := h.Samples()
	if len(s) != HistoryLen || s[0] != 5 || s[HistoryLen-1] != HistoryLen+4 || h.Len() != HistoryLen {
		t.Errorf("full history: len %d, first %v, last %v", len(s), s[0], s[HistoryLen-1])
	}
	h.Reset()
	h.Add(7)
	s = h.Samples()
	if h.Len() != 1 || s[HistoryLen-1] != 7 || !math.IsNaN(s[HistoryLen-2]) {
		t.Errorf("after reset: len %d, tail %v", h.Len(), s[HistoryLen-2:])
	}
}

func TestSpan(t *testing.T) {
	for _, tc := range []struct {
		interval time.Duration
		want     string
	}{
		{time.Second, "59s ago"},
		{2 * time.Second, "1m58s ago"},
		{30 * time.Second, "29m30s ago"},
		{time.Minute, "59m ago"},
		{2 * time.Minute, "1h58m ago"},
		{time.Hour, "59h ago"},
	} {
		if got := Span(tc.interval); got != tc.want {
			t.Errorf("Span(%v) = %q, want %q", tc.interval, got, tc.want)
		}
	}
}

func TestDelta(t *testing.T) {
	for _, tc := range []struct {
		last, previous float64
		want           string
	}{
		{22.7, 24.8, "−2.1 pp · last sample"},
		{24.8, 22.7, "+2.1 pp · last sample"},
		{5, 5.01, "±0.0 pp · last sample"},
		{5, math.NaN(), ""},
		{math.NaN(), 5, ""},
	} {
		if got := Delta(tc.last, tc.previous); got != tc.want {
			t.Errorf("Delta(%v, %v) = %q, want %q", tc.last, tc.previous, got, tc.want)
		}
	}
}

func TestBytes(t *testing.T) {
	for _, tc := range []struct {
		n    uint64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{842 << 20, "842 MiB"},
		{1023 << 20, "1.0 GiB"},
		{12_400_000_000, "11.5 GiB"},
		{32 << 30, "32.0 GiB"},
		{math.MaxUint64, "16.0 EiB"},
	} {
		if got := BytesString(tc.n); got != tc.want {
			t.Errorf("BytesString(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestBitRate(t *testing.T) {
	for _, tc := range []struct {
		bps         float64
		value, unit string
	}{
		{0, "0", "b/s"},
		{999, "999", "b/s"},
		{48_200_000, "48.2", "Mb/s"},
		{999_600, "1.0", "Mb/s"},
		{math.NaN(), "", "b/s"},
	} {
		if v, u := BitRate(tc.bps); v != tc.value || u != tc.unit {
			t.Errorf("BitRate(%v) = %q %q, want %q %q", tc.bps, v, u, tc.value, tc.unit)
		}
	}
}

func TestPoller(t *testing.T) {
	var p Poller
	var calls atomic.Int32
	polled := make(chan struct{}, 10)
	poll := func(context.Context) time.Duration {
		calls.Add(1)
		polled <- struct{}{}
		return time.Hour
	}
	p.Start(poll)
	<-polled
	p.Start(poll)
	select {
	case <-polled:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not wake the poller")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("%d polls, want 2", n)
	}
}

func TestLimit(t *testing.T) {
	for _, tc := range []struct{ interval, want time.Duration }{
		{time.Second, 5 * time.Second},
		{10 * time.Second, 20 * time.Second},
		{time.Hour, 30 * time.Second},
	} {
		if got := Limit(tc.interval); got != tc.want {
			t.Errorf("Limit(%v) = %v, want %v", tc.interval, got, tc.want)
		}
	}
}

func TestSample(t *testing.T) {
	var s Sampler
	if v, err := Sample(context.Background(), &s, time.Second, func(context.Context) (int, error) { return 7, nil }); v != 7 || err != nil {
		t.Fatalf("quick sample = %v, %v", v, err)
	}
	release := make(chan struct{})
	stuck := func(context.Context) (int, error) { <-release; return 1, nil }
	if _, err := Sample(context.Background(), &s, 10*time.Millisecond, stuck); !errors.Is(err, ErrTimeout) {
		t.Fatalf("hung sample: %v, want ErrTimeout", err)
	}
	if !s.Busy() {
		t.Error("not busy with a hung sample")
	}
	ran := false
	if _, err := Sample(context.Background(), &s, 10*time.Millisecond, func(context.Context) (int, error) { ran = true; return 0, nil }); !errors.Is(err, ErrTimeout) || ran {
		t.Fatalf("sample while one is stuck: %v, ran %v; want ErrTimeout without running", err, ran)
	}
	go func() { time.Sleep(10 * time.Millisecond); close(release) }()
	if v, err := Sample(context.Background(), &s, 5*time.Second, func(context.Context) (int, error) { return 8, nil }); v != 8 || err != nil {
		t.Fatalf("sample waiting for the stuck one = %v, %v", v, err)
	}
	if s.Busy() {
		t.Error("busy after the samples returned")
	}
}

// TestPollerAgain checks that a poll asking to poll again now, while a
// wake is waiting, polls once more rather than twice.
func TestPollerAgain(t *testing.T) {
	var p Poller
	var calls atomic.Int32
	var poll func(context.Context) time.Duration
	poll = func(context.Context) time.Duration {
		if calls.Add(1) == 1 {
			p.Start(poll)
			return 0
		}
		return time.Hour
	}
	p.Start(poll)
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 2 {
		t.Errorf("%d polls, want 2", n)
	}
}

// TestPollerCancel checks that waking the poller cancels the context
// of a poll running, and that the next poll gets a fresh one.
func TestPollerCancel(t *testing.T) {
	var p Poller
	running := make(chan struct{})
	again := make(chan error, 1)
	var calls atomic.Int32
	poll := func(ctx context.Context) time.Duration {
		if calls.Add(1) == 1 {
			close(running)
			<-ctx.Done()
			return 0
		}
		again <- ctx.Err()
		return time.Hour
	}
	p.Start(poll)
	<-running
	p.Start(poll)
	select {
	case err := <-again:
		if err != nil {
			t.Errorf("next poll's context: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waking did not cancel the running poll")
	}
}

func TestSampleCanceled(t *testing.T) {
	var s Sampler
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	defer close(release)
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	_, err := Sample(ctx, &s, time.Hour, func(ctx context.Context) (int, error) {
		select {
		case <-ctx.Done():
		case <-release:
		}
		return 0, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled sample: %v, want context.Canceled", err)
	}
}

func TestSampleEnded(t *testing.T) {
	var s Sampler
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Bool
	_, err := Sample(ctx, &s, time.Hour, func(context.Context) (int, error) { ran.Store(true); return 0, nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("sample with an ended context: %v, want context.Canceled", err)
	}
	// Waits for a sample the first started, if it did.
	if _, err := Sample(context.Background(), &s, 5*time.Second, func(context.Context) (int, error) { return 0, nil }); err != nil {
		t.Fatal(err)
	}
	if ran.Load() {
		t.Error("sample started with an ended context")
	}
}

// TestPollerCadence checks that the delay a poll returns counts from
// when it started, so a slow poll does not stretch the interval.
func TestPollerCadence(t *testing.T) {
	var p Poller
	starts := make(chan time.Time, 2)
	p.Start(func(context.Context) time.Duration {
		starts <- time.Now()
		time.Sleep(500 * time.Millisecond)
		return 600 * time.Millisecond
	})
	first, second := <-starts, <-starts
	if gap := second.Sub(first); gap > time.Second {
		t.Errorf("polls %v apart, want about 600ms", gap)
	}
}
