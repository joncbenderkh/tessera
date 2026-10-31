// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package main

import (
	"context"
	"errors"
	"image"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	psnet "github.com/shirou/gopsutil/v4/net"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/plugins/internal/metric"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
	"github.com/joncbenderkh/tessera/sdk"
)

func TestMain(m *testing.M) { plugintest.Main(m, main) }

func TestRates(t *testing.T) {
	at := time.Now()
	reading := func(after time.Duration, bytes map[string]traffic) counters {
		return counters{at: at.Add(after), bytes: bytes}
	}
	before := reading(0, map[string]traffic{"eth0": {1000, 500}, "veth1": {126, 126}})
	for _, tc := range []struct {
		name     string
		after    counters
		down, up float64
	}{
		{"steady", reading(2*time.Second, map[string]traffic{"eth0": {7000, 1500}, "veth1": {126, 126}}), 24000, 4000},
		{"one went away", reading(2*time.Second, map[string]traffic{"eth0": {7000, 1500}}), 24000, 4000},
		{"one came", reading(2*time.Second, map[string]traffic{"eth0": {7000, 1500}, "veth1": {126, 126}, "wg0": {1 << 30, 1 << 30}}), 24000, 4000},
		{"one was reset", reading(2*time.Second, map[string]traffic{"eth0": {7000, 1500}, "veth1": {0, 0}}), 24000, 4000},
		{"too soon", reading(10*time.Millisecond, map[string]traffic{"eth0": {7000, 1500}, "veth1": {126, 126}}), math.NaN(), math.NaN()},
		{"all reset", reading(time.Second, map[string]traffic{"eth0": {10, 10}, "veth1": {0, 0}}), math.NaN(), math.NaN()},
		{"none in common", reading(time.Second, map[string]traffic{"wg0": {10, 10}}), math.NaN(), math.NaN()},
	} {
		down, up := rates(before, tc.after)
		same := func(a, b float64) bool { return a == b || math.IsNaN(a) && math.IsNaN(b) }
		if !same(down, tc.down) || !same(up, tc.up) {
			t.Errorf("%s: rates %v, %v, want %v, %v", tc.name, down, up, tc.down, tc.up)
		}
	}
}

func TestDelta(t *testing.T) {
	for _, tc := range []struct {
		last, previous float64
		want           string
	}{
		{48_200_000, 45_800_000, "+2.4 Mb/s · last sample"},
		{45_800_000, 48_200_000, "−2.4 Mb/s · last sample"},
		{1000, 1000, "±0 b/s · last sample"},
		{1000, math.NaN(), ""},
	} {
		if got := delta(tc.last, tc.previous); got != tc.want {
			t.Errorf("delta(%v, %v) = %q, want %q", tc.last, tc.previous, got, tc.want)
		}
	}
}

// stats returns the values and units of the stat widgets in v, in order.
func stats(v *tesserav1.View) []string {
	var out []string
	for _, w := range plugintest.Widgets(v.GetBody()) {
		if s := w.GetStat(); s != nil {
			out = append(out, s.GetLabel()+" "+s.GetValue()+" "+s.GetUnit())
		}
	}
	return out
}

func TestView(t *testing.T) {
	var down, up metric.History
	down.Add(45_800_000)
	down.Add(48_200_000)
	up.Add(1_200_000)
	up.Add(900_000)
	v := view(state{iface: "eth0", interval: 2 * time.Second, down: down.Samples(), up: up.Samples()}).Proto()
	if got, want := stats(v), []string{"Down 48.2 Mb/s", "Up 900 kb/s"}; !slices.Equal(got, want) {
		t.Errorf("stats %q, want %q", got, want)
	}
	if got, want := plugintest.Texts(v.GetBody()), []string{"eth0"}; !slices.Equal(got, want) {
		t.Errorf("texts %q, want %q", got, want)
	}
	for _, w := range plugintest.Widgets(v.GetBody()) {
		if w.GetStat() != nil && w.GetLoading() {
			t.Errorf("stat %q loading with rates shown", w.GetStat().GetLabel())
		}
	}
	gap := down
	gap.Add(math.NaN())
	v = view(state{interval: time.Second, down: gap.Samples(), up: gap.Samples()}).Proto()
	for _, w := range plugintest.Widgets(v.GetBody()) {
		if w.GetStat() != nil && w.GetLoading() {
			t.Errorf("stat %q loading at a gap after rates", w.GetStat().GetLabel())
		}
	}
	v = view(state{physical: true, interval: time.Second, down: down.Samples(), up: up.Samples()}).Proto()
	if got, want := plugintest.Texts(v.GetBody()), []string{"Physical interfaces"}; !slices.Equal(got, want) {
		t.Errorf("texts %q, want %q", got, want)
	}
	var none metric.History
	v = view(state{interval: time.Second, down: none.Samples(), up: none.Samples(), errorText: "No interface wg0"}).Proto()
	if got, want := plugintest.Texts(v.GetBody()), []string{"No interface wg0", "Retrying; the details are in the panel's logs."}; !slices.Equal(got, want) {
		t.Errorf("texts %q, want %q", got, want)
	}
}

func TestNetwork(t *testing.T) {
	p := plugintest.Launch(t, "network")
	if got := p.Info.GetName(); got != "network" {
		t.Errorf("name %q, want network", got)
	}
	for _, s := range []map[string]any{{"interface": 1.0}, {"interval": "2h"}} {
		if err := p.Configure(s); status.Code(err) != codes.InvalidArgument {
			t.Errorf("settings %v: %v, want INVALID_ARGUMENT", s, err)
		}
	}
	if err := p.Configure(map[string]any{"interface": "tessera-test0"}); err != nil {
		t.Fatal(err)
	}
	p.AwaitTexts("No interface tessera-test0", "Retrying; the details are in the panel's logs.")
	if err := p.Configure(map[string]any{"interval": "1s"}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		if slices.Contains(plugintest.Texts(v.GetBody()), "No network interface but loopback") {
			// As in a sandbox with no network.
			return true
		}
		s := stats(v)
		return len(s) == 2 && s[0] != "Down  b/s" && s[1] != "Up  b/s"
	})
}

func TestRead(t *testing.T) {
	c, err := read(context.Background(), "")
	if errors.Is(err, errNoInterface) {
		t.Skip("no network interface but loopback")
	}
	if err != nil {
		t.Fatal(err)
	}
	for name := range c.bytes {
		if c.physical && !isPhysical(name) {
			t.Errorf("virtual interface %s counted with the physical ones", name)
		}
	}
}

// TestReadUnlisted checks that the counters are read, but for lo, when
// the interfaces cannot be listed.
func TestReadUnlisted(t *testing.T) {
	defer func(f func(context.Context) (psnet.InterfaceStatList, error)) { interfaces = f }(interfaces)
	interfaces = func(context.Context) (psnet.InterfaceStatList, error) {
		return nil, errors.New("netlink blocked")
	}
	c, err := read(context.Background(), "")
	if errors.Is(err, errNoInterface) {
		t.Skip("no network interface but loopback")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.bytes["lo"]; ok {
		t.Error("loopback counted")
	}
}

// TestPoll checks that the first reading is followed soon, and that one
// too soon after the last to measure the rates over is not kept.
func TestPoll(t *testing.T) {
	views := make(chan sdk.View, 2)
	n := &network{views: views, interval: time.Second}
	next := n.poll(context.Background())
	if n.errorText == "No network interface but loopback" {
		t.Skip("no network interface but loopback")
	}
	if next != 500*time.Millisecond {
		t.Fatalf("first poll: next %v", next)
	}
	n.last.at = time.Now() // however long the first reading took
	last := n.last
	if next := n.poll(context.Background()); next != metric.MinElapsed || n.last != last || len(views) != 1 {
		t.Errorf("poll too soon: next %v, reading kept %v, %d views", next, n.last != last, len(views))
	}
}

func TestTop(t *testing.T) {
	for _, tc := range []struct {
		samples []float64
		want    float64
	}{
		{[]float64{math.NaN(), 0, 0}, 1},
		{[]float64{0, 2400, math.NaN(), 800}, 2400},
	} {
		if got := top(tc.samples); got != tc.want {
			t.Errorf("top(%v) = %v, want %v", tc.samples, got, tc.want)
		}
	}
}

// TestIsPhysicalHostSys checks that the devices are looked up under
// HOST_SYS, with the counters read from HOST_PROC.
func TestIsPhysicalHostSys(t *testing.T) {
	sys := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sys, "class/net/nic9/device"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func(old string) { hostSys = old }(hostSys)
	hostSys = sys
	if !isPhysical("nic9") || isPhysical("br9") {
		t.Errorf("isPhysical under %s: nic9 %v, br9 %v; want true, false", sys, isPhysical("nic9"), isPhysical("br9"))
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
	var down, up metric.History
	for i := range 60 {
		down.Add(float64(i) * 1e6)
		up.Add(9e5)
	}
	return []sdk.View{view(state{iface: "eth0", interval: 2 * time.Second, down: down.Samples(), up: up.Samples()})}
}
