// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

func TestParseStat(t *testing.T) {
	data := []byte("412 (a) b (c) S 1 412 412 0 -1 4194560 100 0 0 0 250 150 0 0 20 0 1 0 12345 1000000 300 18446744073709551615 0 0 0\n")
	st, err := parseStat(data)
	if err != nil {
		t.Fatal(err)
	}
	if st.comm != "a) b (c" || st.start != 12345 || st.rss != 300*uint64(os.Getpagesize()) {
		t.Errorf("parseStat = %+v", st)
	}
	if want := 400 / float64(clockTicks()); st.seconds != want {
		t.Errorf("seconds %v, want %v", st.seconds, want)
	}
	for _, bad := range []string{"", "412 (a S 1", "412 (a) S 1 2 3"} {
		if _, err := parseStat([]byte(bad)); err == nil {
			t.Errorf("parseStat(%q) succeeded", bad)
		}
	}
}

// TestSampleMatchesGopsutil checks the stat file's reading of this
// process against gopsutil's.
func TestSampleMatchesGopsutil(t *testing.T) {
	ps, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	// A fresh test binary may not have used a clock tick of CPU yet,
	// so spin until it has.
	var (
		pr  proc
		cpu cpuTime
		ok  bool
	)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if pr, cpu, ok = sample(ps.Pid); !ok || cpu.seconds > 0 {
			break
		}
	}
	if !ok {
		t.Fatal("this process is gone")
	}
	name, _ := ps.Name()
	created, _ := ps.CreateTime()
	if pr.name != name {
		t.Errorf("name %q, want %q", pr.name, name)
	}
	st, err := readStat(ps.Pid)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := st.created()
	if d := mine - created; err != nil || d < -1000 || d > 1000 || pr.start != st.start || cpu.start != st.start {
		t.Errorf("created %d (%v), gopsutil %d; start %d, CPU's %d, want %d", mine, err, created, pr.start, cpu.start, st.start)
	}
	if pr.rss == 0 || cpu.seconds <= 0 {
		t.Errorf("rss %d, CPU %v s", pr.rss, cpu.seconds)
	}
}

// TestSampleLongName checks that a name longer than the 15 bytes the
// kernel keeps is the executable's, not taken from a long argv[0].
func TestSampleLongName(t *testing.T) {
	const base = "abcdefghijklmnopqrst"
	cmd := startAs(t, base, base+strings.Repeat("x", 100_000))
	pr, _, ok := sample(int32(cmd.Process.Pid))
	if !ok {
		t.Fatal("process gone")
	}
	if pr.name != base {
		t.Errorf("name %.40q, want %q", pr.name, base)
	}
}

func BenchmarkTake(b *testing.B) {
	for b.Loop() {
		if _, err := take(context.Background(), reading{}, 1); err != nil {
			b.Fatal(err)
		}
	}
}

// TestBootTimeRetry checks that a failed read of the boot time is not
// kept, which would leave every process's start time in its details
// unknown.
func TestBootTimeRetry(t *testing.T) {
	boot.Lock()
	saved, read := boot.secs, readBootTime
	boot.secs = 0
	readBootTime = func() (uint64, error) { return 0, errors.New("no uptime") }
	boot.Unlock()
	t.Cleanup(func() {
		boot.Lock()
		boot.secs, readBootTime = saved, read
		boot.Unlock()
	})
	if _, err := bootTime(); err == nil {
		t.Fatal("bootTime succeeded")
	}
	boot.Lock()
	readBootTime = func() (uint64, error) { return 1_700_000_000, nil }
	boot.Unlock()
	if secs, err := bootTime(); secs != 1_700_000_000 || err != nil {
		t.Errorf("bootTime after a failure = %d, %v", secs, err)
	}
}

// TestLookupNoBootTime checks that a row's action tells its process
// from a later one given its ID even when the boot time, and so the
// creation time, cannot be read.
func TestLookupNoBootTime(t *testing.T) {
	boot.Lock()
	saved, read := boot.secs, readBootTime
	boot.secs = 0
	readBootTime = func() (uint64, error) { return 0, errors.New("no uptime") }
	boot.Unlock()
	t.Cleanup(func() {
		boot.Lock()
		boot.secs, readBootTime = saved, read
		boot.Unlock()
	})
	pr, _, ok := sample(int32(os.Getpid()))
	if !ok {
		t.Fatal("this process is gone")
	}
	sel, err := parseAction(action(pr))
	if err != nil {
		t.Fatal(err)
	}
	if d := lookup(context.Background(), sel); d == nil || d.exited {
		t.Errorf("lookup of the test itself: %+v", d)
	}
	sel.start++
	if d := lookup(context.Background(), sel); d == nil || !d.exited {
		t.Errorf("lookup of a later process with the test's ID: %+v, want exited", d)
	}
}
