// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	"github.com/joncbenderkh/tessera/plugins/internal/metric"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
	"github.com/joncbenderkh/tessera/sdk"
)

// sleepEnv set makes the test binary sleep rather than test, as a
// process to look at under a name of the test's choosing.
const sleepEnv = "PROCESSES_TEST_SLEEP"

func TestMain(m *testing.M) {
	if os.Getenv(sleepEnv) != "" {
		time.Sleep(time.Minute)
		return
	}
	plugintest.Main(m, main)
}

// startAs starts a copy of the test binary, sleeping, as the
// executable name with argv[0] arg0, until the test ends. A copy of
// sleep would not do where it is a multi-call binary, as BusyBox and
// uutils are, which goes by argv[0].
func startAs(t *testing.T, name, arg0 string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(bin, data, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := &exec.Cmd{Path: bin, Args: []string{arg0}, Env: append(os.Environ(), sleepEnv+"=1")}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func TestCPUPercent(t *testing.T) {
	if got := cpuPercent(10, 13, 2*time.Second, 4); math.Abs(got-37.5) > 1e-9 {
		t.Errorf("cpuPercent = %v, want 37.5", got)
	}
	if got := cpuPercent(13, 10, time.Second, 4); !math.IsNaN(got) {
		t.Errorf("cpuPercent of a process that lost time = %v, want NaN", got)
	}
	if got := cpuPercent(0, 0.01, 20*time.Millisecond, 1); !math.IsNaN(got) {
		t.Errorf("cpuPercent over 20ms = %v, want NaN", got)
	}
}

func TestSort(t *testing.T) {
	procs := []proc{
		{pid: 30, name: "editor", cpu: 3.8, rss: 614 << 20},
		{pid: 10, name: "Browser", cpu: 5.2, rss: 2 << 30},
		{pid: 20, name: "go", cpu: math.NaN(), rss: 842 << 20},
		{pid: 5, name: "sh", cpu: 3.8, rss: 96 << 20},
		{pid: 40, name: "secret", cpu: math.NaN()},
	}
	for _, tc := range []struct {
		sort string
		want []int32
	}{
		{"cpu", []int32{10, 5, 30, 20, 40}},
		{"memory", []int32{10, 20, 30, 5, 40}},
		{"pid", []int32{5, 10, 20, 30, 40}},
		{"name", []int32{10, 30, 20, 40, 5}},
	} {
		sortProcs(procs, sorts[tc.sort])
		var got []int32
		for _, p := range procs {
			got = append(got, p.pid)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("sort %s: %v, want %v", tc.sort, got, tc.want)
		}
	}
}

func TestAction(t *testing.T) {
	if id := action(proc{pid: 4821, start: 1234}); id != "pid:4821:1234" {
		t.Errorf("action = %q", id)
	}
	if sel, err := parseAction("pid:4821:1234"); sel != (selection{pid: 4821, start: 1234}) || err != nil {
		t.Errorf("parseAction = %+v, %v", sel, err)
	}
	if sel, err := parseAction("pid:4821:0"); sel != (selection{pid: 4821}) || err != nil {
		t.Errorf("parseAction, started at boot = %+v, %v", sel, err)
	}
	for _, id := range []string{"4821", "pid:", "pid:4821", "pid:0:1", "pid:-1:1", "pid:x:1", "pid:99999999999:1", "pid:4821:", "pid:4821:x", "pid:4821:-1"} {
		if _, err := parseAction(id); err == nil {
			t.Errorf("parseAction(%q) succeeded", id)
		}
	}
}

// actionOf is the action ID of the row of the process pid.
func actionOf(t *testing.T, pid int) string {
	t.Helper()
	return action(proc{pid: int32(pid), start: selectionOf(t, int32(pid)).start})
}

// selectionOf is the selection of the process pid.
func selectionOf(t *testing.T, pid int32) selection {
	t.Helper()
	st, err := readStat(pid)
	if err != nil {
		t.Fatal(err)
	}
	return selection{pid: pid, start: st.start}
}

// table returns the first table in v.
func table(v *tesserav1.View) *tesserav1.Table {
	for _, w := range plugintest.Widgets(v.GetBody()) {
		if t := w.GetTable(); t != nil {
			return t
		}
	}
	return nil
}

func TestView(t *testing.T) {
	procs := []proc{{pid: 4821, name: "go", cpu: 12.6, rss: 842 << 20}, {pid: 1, name: "init", cpu: math.NaN(), rss: 12 << 20}}
	started := time.Now().Add(-time.Minute)
	sel := &details{pid: 4821, name: "go", parent: 1, user: "jon", status: "running", threads: 12, started: started, cmdline: "go build ./..."}
	v := view(sorts["cpu"], procs, 142, sel).Proto()
	if v.GetTitle() != "Top processes by CPU" {
		t.Errorf("title %q", v.GetTitle())
	}
	tb := table(v)
	if tb.GetSortColumn() != 2 || tb.GetSortOrder() != tesserav1.SortOrder_SORT_ORDER_DESCENDING {
		t.Errorf("sort column %d, order %v", tb.GetSortColumn(), tb.GetSortOrder())
	}
	rows := tb.GetRows()
	if len(rows) != 2 || !slices.Equal(rows[0].GetCells(), []string{"go", "4821", "12.6%", "842 MiB"}) ||
		rows[0].GetActionId() != "pid:4821:0" || rows[1].GetCells()[2] != "—" {
		t.Errorf("rows %v", rows)
	}
	want := []string{
		"2 of 142 processes",
		"go",
		"PID 4821 · parent 1 · jon",
		"Running · 12 threads · started " + started.Format("15:04"),
		"go build ./...",
	}
	if got := plugintest.Texts(v.GetBody()); !slices.Equal(got, want) {
		t.Errorf("texts %q, want %q", got, want)
	}
	v = view(sorts["name"], nil, 0, &details{pid: 7, exited: true}).Proto()
	if got, want := plugintest.Texts(v.GetBody()), []string{"0 of 0 processes", "Process 7 has exited."}; !slices.Equal(got, want) {
		t.Errorf("texts %q, want %q", got, want)
	}
}

func TestStarted(t *testing.T) {
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 10, 28, 9, 12, 0, 0, time.UTC), "09:12"},
		{time.Date(2026, 10, 27, 9, 12, 0, 0, time.UTC), "Oct 27 09:12"},
		{time.Date(2025, 12, 31, 9, 12, 0, 0, time.UTC), "Dec 31 2025"},
	} {
		if got := started(tc.t, now); got != tc.want {
			t.Errorf("started(%v) = %q, want %q", tc.t, got, tc.want)
		}
	}
}

func TestProcesses(t *testing.T) {
	p := plugintest.Launch(t, "processes")
	if got := p.Info.GetName(); got != "processes" {
		t.Errorf("name %q, want processes", got)
	}
	for _, s := range []map[string]any{{"sort": "size"}, {"rows": 0.0}, {"rows": 101.0}, {"rows": 2.5}, {"interval": "1ms"}} {
		if err := p.Configure(s); status.Code(err) != codes.InvalidArgument {
			t.Errorf("settings %v: %v, want INVALID_ARGUMENT", s, err)
		}
	}
	if err := p.Configure(map[string]any{"sort": "pid", "rows": 100.0}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool { return len(table(v).GetRows()) > 1 })
	// The test itself is a process; its details show whether or not
	// its row does.
	pid := os.Getpid()
	self := actionOf(t, pid)
	if err := p.Activate(self); err != nil {
		t.Fatal(err)
	}
	p.AwaitTextsFunc(func(texts []string) bool {
		return slices.ContainsFunc(texts, func(s string) bool { return strings.HasPrefix(s, "PID "+strconv.Itoa(pid)+" ") })
	})
	if err := p.Activate(self); err != nil {
		t.Fatal(err)
	}
	p.AwaitTextsFunc(func(texts []string) bool { return len(texts) == 1 })
}

func TestLookup(t *testing.T) {
	self := selectionOf(t, int32(os.Getpid()))
	if d := lookup(context.Background(), self); d == nil || d.exited || d.name == "" || d.started.IsZero() {
		t.Errorf("lookup of the test itself: %+v", d)
	}
	if d := lookup(context.Background(), selection{pid: self.pid, start: self.start - 1}); d == nil || !d.exited {
		t.Errorf("lookup of an earlier process with the test's ID: %+v, want exited", d)
	}
	if d := lookup(context.Background(), selection{}); d != nil {
		t.Errorf("lookup of no selection: %+v, want nil", d)
	}
}

// TestInvalidName checks that a process whose name is not UTF-8 shows,
// rather than every view being dropped.
func TestInvalidName(t *testing.T) {
	cmd := startAs(t, "a\xffb", "a\xffb")

	p := plugintest.Launch(t, "processes")
	if err := p.Configure(map[string]any{"sort": "pid", "rows": 100.0}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool { return len(table(v).GetRows()) > 1 })
	if err := p.Activate(actionOf(t, cmd.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	p.AwaitTextsFunc(func(texts []string) bool { return slices.Contains(texts, "a�b") })
}

// TestExited checks that a selected process that exits shows so once,
// and is then no longer selected.
func TestExited(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip(err)
	}
	cmd := exec.Command(sleep, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	p := plugintest.Launch(t, "processes")
	if err := p.Configure(map[string]any{"interval": "1s"}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool { return len(table(v).GetRows()) > 1 })
	if err := p.Activate(actionOf(t, pid)); err != nil {
		t.Fatal(err)
	}
	p.AwaitTextsFunc(func(texts []string) bool {
		return slices.ContainsFunc(texts, func(s string) bool { return strings.HasPrefix(s, "PID "+strconv.Itoa(pid)+" ") })
	})
	cmd.Process.Kill()
	cmd.Wait()
	exited := fmt.Sprintf("Process %d has exited.", pid)
	p.AwaitTextsFunc(func(texts []string) bool { return slices.Contains(texts, exited) })
	p.AwaitTextsFunc(func(texts []string) bool { return len(texts) == 1 })
}

// TestTakeExec checks that a process shows the name of the program it
// last executed.
func TestTakeExec(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	cmd := exec.Command(sh, "-c", "sleep 0.3; exec sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	pid := int32(cmd.Process.Pid)
	name := func(r reading) string {
		for _, pr := range r.procs {
			if pr.pid == pid {
				return pr.name
			}
		}
		return ""
	}
	first, err := take(context.Background(), reading{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := name(first); got != "sh" && got != filepath.Base(sh) {
		t.Fatalf("before exec, named %q, want sh", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r := first; name(r) != "sleep"; {
		if time.Now().After(deadline) {
			t.Fatalf("after exec, named %q, want sleep", name(r))
		}
		time.Sleep(100 * time.Millisecond)
		if r, err = take(context.Background(), r, 1); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPollTooSoon checks that a sample taken too soon after the last to
// measure the CPU over, as when settings come while one is taken, is
// not kept.
func TestPollTooSoon(t *testing.T) {
	views := make(chan sdk.View, 2)
	p := &processes{views: views, cores: 1, sort: defaultSort, rows: defaultRows, interval: time.Second}
	if next := p.poll(context.Background()); next != 500*time.Millisecond {
		t.Fatalf("first poll: next %v", next)
	}
	p.at = time.Now() // however long the first sample took
	at := p.at
	if next := p.poll(context.Background()); next != metric.MinElapsed || p.at != at || len(views) != 1 {
		t.Errorf("poll too soon: next %v, sample kept %v, %d views", next, p.at != at, len(views))
	}
}

func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"abcdef", 3, "abc…"},
		{"aéb", 2, "a…"},
	} {
		if got := truncate(tc.s, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

// TestShowLookupOlder checks that a lookup for an older selection than
// one already looked up neither stops that lookup nor shows anything.
func TestShowLookupOlder(t *testing.T) {
	views := make(chan sdk.View, 1)
	p := &processes{views: views, interval: time.Second, gen: 2, lookupGen: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.cancelLookup = cancel
	p.showLookup(1, selection{pid: int32(os.Getpid())})
	if ctx.Err() != nil {
		t.Error("an older lookup stopped the newer one")
	}
	if len(views) != 0 {
		t.Error("an older lookup showed a view")
	}
}

// TestSampleGone checks that a process that exits between being listed
// and being read is left out, rather than shown by its ID.
func TestSampleGone(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip(err)
	}
	cmd := exec.Command(sleep, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if pr, _, ok := sample(int32(cmd.Process.Pid)); ok {
		t.Errorf("exited process sampled as %+v", pr)
	}
}

// TestActivateStart checks that a selection keeps the start
// of the process its row showed, which tells it from a later one with
// its ID, even once the process has left the latest sample.
func TestActivateStart(t *testing.T) {
	p := &processes{procs: []proc{{pid: 412, name: "sleep", start: 5678}}}
	if err := p.activate(context.Background(), "pid:412:1234"); err != nil {
		t.Fatal(err)
	}
	if got := p.selected; got != (selection{pid: 412, start: 1234}) {
		t.Errorf("selection %+v, want PID 412 started at 1234", got)
	}
}

// TestShowFailed checks that after a failed sample the one before is
// not drawn again, as when the sort changes, as if it were current.
func TestShowFailed(t *testing.T) {
	views := make(chan sdk.View, 1)
	p := &processes{views: views, sort: defaultSort, rows: defaultRows, sampled: true, failed: true,
		procs: []proc{{pid: 412, name: "sleep"}}}
	p.show(nil)
	v := (<-views).Proto()
	if got, want := plugintest.Texts(v.GetBody()), plugintest.Texts(failed("").Proto().GetBody()); !slices.Equal(got, want) {
		t.Errorf("texts %q, want %q", got, want)
	}
	if got, want := v.GetTitle(), sorts[defaultSort].title; got != want {
		t.Errorf("title %q, want the table's, %q", got, want)
	}
}

// TestShowLookupStuck checks that while a lookup is stuck, as on a
// process stuck in the kernel, the next shows the table without details
// at once, waits for it within its limit, rather than starting another
// that could get stuck too, and then shows that the details cannot be
// read.
func TestShowLookupStuck(t *testing.T) {
	views := make(chan sdk.View, 1)
	p := &processes{views: views, sort: defaultSort, rows: defaultRows, interval: time.Second, sampled: true, gen: 1, selected: selection{pid: 412}, lookupLimit: 10 * time.Millisecond}
	release := make(chan struct{})
	defer close(release)
	stuck := p.lookupSampler(412)
	go metric.Sample(context.Background(), stuck, time.Millisecond, func(context.Context) (*details, error) {
		<-release
		return nil, nil
	})
	for !stuck.Busy() {
		time.Sleep(time.Millisecond)
	}
	go p.showLookup(1, selection{pid: 412})
	if got := plugintest.Texts((<-views).Proto().GetBody()); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "PID ") || strings.HasPrefix(s, "Cannot read") }) {
		t.Fatalf("first view while waiting: texts %q, want the table alone", got)
	}
	want := "Cannot read the details of process 412."
	if got := plugintest.Texts((<-views).Proto().GetBody()); !slices.Contains(got, want) {
		t.Errorf("texts %q, want %q among them", got, want)
	}
}

// TestShowLookupPolled checks that polls, each asking for the details
// again while a lookup is stuck, neither cancel it nor keep it from
// showing that the details cannot be read when it runs out of time.
func TestShowLookupPolled(t *testing.T) {
	views := make(chan sdk.View, 20)
	p := &processes{views: views, sort: defaultSort, rows: defaultRows, interval: time.Second, sampled: true, gen: 1,
		selected: selection{pid: 412}, lookupLimit: 100 * time.Millisecond}
	release := make(chan struct{})
	defer close(release)
	stuck := p.lookupSampler(412)
	go metric.Sample(context.Background(), stuck, time.Millisecond, func(context.Context) (*details, error) {
		<-release
		return nil, nil
	})
	for !stuck.Busy() {
		time.Sleep(time.Millisecond)
	}
	for range 10 {
		go p.showLookup(1, p.selected)
		time.Sleep(30 * time.Millisecond)
	}
	want := "Cannot read the details of process 412."
	for len(views) > 0 {
		if slices.Contains(plugintest.Texts((<-views).Proto().GetBody()), want) {
			return
		}
	}
	t.Errorf("no view with %q", want)
}

// TestShowLookupOthers checks that while one process's lookup is stuck,
// deselecting it shows no error, and another's details still show.
func TestShowLookupOthers(t *testing.T) {
	views := make(chan sdk.View, 4)
	p := &processes{views: views, sort: defaultSort, rows: defaultRows, interval: time.Second, sampled: true, gen: 1,
		selected: selection{pid: 412}, lookupLimit: 50 * time.Millisecond}
	release := make(chan struct{})
	defer close(release)
	stuck := p.lookupSampler(412)
	go metric.Sample(context.Background(), stuck, time.Millisecond, func(context.Context) (*details, error) {
		<-release
		return nil, nil
	})
	for !stuck.Busy() {
		time.Sleep(time.Millisecond)
	}
	p.gen, p.selected = 2, selection{}
	p.showLookup(2, p.selected)
	if got := plugintest.Texts((<-views).Proto().GetBody()); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "Cannot read") }) {
		t.Errorf("deselected: texts %q", got)
	}
	pid := int32(os.Getpid())
	p.gen, p.selected = 3, selectionOf(t, pid)
	go p.showLookup(3, p.selected)
	<-views // the table, while the details are read
	want := fmt.Sprintf("PID %d", pid)
	if got := plugintest.Texts((<-views).Proto().GetBody()); !slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, want) }) {
		t.Errorf("another process while one is stuck: texts %q, want its details", got)
	}
}

// TestShowLookupNew checks that a new selection shows the table without
// the last one's details at once, before its own are read, which may
// take until the limit.
func TestShowLookupNew(t *testing.T) {
	views := make(chan sdk.View, 2)
	pid := int32(os.Getpid())
	p := &processes{views: views, sort: defaultSort, rows: defaultRows, interval: time.Second, sampled: true, gen: 2,
		selected: selectionOf(t, pid)}
	p.showLookup(2, p.selected)
	isDetail := func(s string) bool { return strings.HasPrefix(s, "PID ") }
	if got := plugintest.Texts((<-views).Proto().GetBody()); slices.ContainsFunc(got, isDetail) {
		t.Fatalf("first view: texts %q, want the table alone", got)
	}
	if got := plugintest.Texts((<-views).Proto().GetBody()); !slices.ContainsFunc(got, isDetail) {
		t.Errorf("second view: texts %q, want the details", got)
	}
}
