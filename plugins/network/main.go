// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

// Command network is the network throughput panel: how fast data comes
// in and goes out, each with a sparkline of its recent samples, over
// one interface or several added up: by default the physical ones, or
// all but loopback if there is none. The interface and how often it
// samples are settings.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	psnet "github.com/shirou/gopsutil/v4/net"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/plugins/internal/metric"
	"github.com/joncbenderkh/tessera/sdk"
)

const defaultInterval = "2s"

var schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "interface": {
      "type": "string",
      "description": "The network interface to show, such as eth0; by default the physical ones added up, or all but loopback if there is none."
    },
    ` + metric.IntervalSchema(defaultInterval) + `
  },
  "additionalProperties": false
}`

type settings struct {
	Interface string `json:"interface"`
	Interval  string `json:"interval"`
}

// counters is a reading of the byte counters of each interface shown,
// and when it was taken. physical is whether they are the physical
// interfaces only.
type counters struct {
	at       time.Time
	bytes    map[string]traffic
	physical bool
}

// traffic is an interface's byte counters.
type traffic struct{ recv, sent uint64 }

// network is the panel's state. Configure and the poller run
// concurrently, so mu guards it. Polling starts on the first Configure,
// so no view shows an interface the settings do not ask for.
type network struct {
	views   chan<- sdk.View
	poller  metric.Poller
	sampler metric.Sampler

	mu        sync.Mutex
	iface     string
	interval  time.Duration
	physical  bool
	last      *counters
	down, up  metric.History
	lastErr   string
	errorText string
}

func main() {
	views := make(chan sdk.View)
	n := &network{views: views}
	sdk.Serve(sdk.Plugin{
		Name:         "network",
		Version:      tessera.Version,
		Description:  "Network throughput in and out, with its recent history.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 2, Rows: 2},
		Views:        views,
		Configure:    n.configure,
	})
}

func (n *network) configure(_ context.Context, cfg sdk.Config) error {
	s := settings{Interval: defaultInterval}
	if err := cfg.Decode(&s); err != nil {
		return err
	}
	interval, err := metric.ParseInterval(s.Interval)
	if err != nil {
		return err
	}
	n.mu.Lock()
	changed := s.Interface != n.iface || interval != n.interval
	if changed {
		n.iface, n.interval = s.Interface, interval
		n.last = nil
		n.down.Reset()
		n.up.Reset()
	}
	n.mu.Unlock()
	if changed {
		n.poller.Start(n.poll)
	}
	return nil
}

// errNoInterface is the error when the interface setting names none
// the system has, which may yet appear, as a VPN's does, or when it is
// unset and there is none but loopback, as in a container without a
// network.
var errNoInterface = errors.New("no such interface")

func (n *network) poll(ctx context.Context) time.Duration {
	n.mu.Lock()
	iface, interval := n.iface, n.interval
	n.mu.Unlock()

	now, err := metric.Sample(ctx, &n.sampler, metric.Limit(interval), func(ctx context.Context) (counters, error) {
		return read(ctx, iface)
	})
	if ctx.Err() != nil {
		// The settings changed; the poller was woken.
		return 0
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if iface != n.iface || interval != n.interval {
		// The settings changed while sampling, clearing the history,
		// which this sample is not part of; the poller was woken.
		return 0
	}
	if err == nil && n.last != nil && now.at.Sub(n.last.at) < metric.MinElapsed {
		// Too soon after the last reading to measure the rates over:
		// keep that one, and wait.
		return metric.MinElapsed
	}
	baseline := err == nil && n.last == nil
	n.errorText = ""
	switch {
	case err != nil:
		n.errorText = "Cannot read the network counters"
		switch {
		case !errors.Is(err, errNoInterface):
		case iface == "":
			n.errorText = "No network interface but loopback"
		default:
			n.errorText = fmt.Sprintf("No interface %s", iface)
		}
		if err.Error() != n.lastErr {
			log.Print(err)
		}
		n.lastErr = err.Error()
		n.last = nil
		n.down.Add(math.NaN())
		n.up.Add(math.NaN())
	default:
		n.lastErr = ""
		if n.last != nil {
			down, up := rates(*n.last, now)
			n.down.Add(down)
			n.up.Add(up)
		}
		n.last = &now
		n.physical = now.physical
	}
	n.views <- view(n.state())
	if baseline {
		// Soon after a reading with none to measure from, at the start
		// or once an error clears.
		return 500 * time.Millisecond
	}
	return n.interval
}

// read reads, until ctx ends, the byte counters of iface, or if iface
// is "", of the physical interfaces, or of all but loopback if there is
// none, as in a container. Adding up virtual interfaces, such as a
// bridge, its containers' veths, or a VPN's tunnel, counts the same
// traffic more than once.
func read(ctx context.Context, iface string) (counters, error) {
	at := time.Now()
	stats, err := psnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return counters{}, err
	}
	skip := map[string]bool{}
	if iface == "" {
		skip = loopbacks(ctx)
	}
	c := counters{at: at, bytes: map[string]traffic{}}
	for _, s := range stats {
		if (iface != "" && s.Name != iface) || skip[s.Name] {
			continue
		}
		c.bytes[s.Name] = traffic{recv: s.BytesRecv, sent: s.BytesSent}
	}
	if iface == "" {
		physical := maps.Clone(c.bytes)
		maps.DeleteFunc(physical, func(name string, _ traffic) bool { return !isPhysical(name) })
		if len(physical) > 0 {
			c.bytes, c.physical = physical, true
		}
	}
	if len(c.bytes) == 0 && iface != "" {
		return counters{}, fmt.Errorf("%w: %s", errNoInterface, iface)
	}
	if len(c.bytes) == 0 {
		return counters{}, errNoInterface
	}
	return c, nil
}

// interfaces lists the network interfaces; tests replace it.
var interfaces = psnet.InterfacesWithContext

// loopbacks is the set of loopback interfaces, or just lo if they
// cannot be listed, as where netlink is blocked: the counters from
// /proc/net/dev are still worth showing.
func loopbacks(ctx context.Context) map[string]bool {
	ifs, err := interfaces(ctx)
	if err != nil {
		return map[string]bool{"lo": true}
	}
	skip := map[string]bool{}
	for _, i := range ifs {
		if slices.Contains(i.Flags, "loopback") {
			skip[i.Name] = true
		}
	}
	return skip
}

// hostSys is where sysfs is mounted: /sys, or HOST_SYS if set, as
// gopsutil reads it, with the counters from HOST_PROC.
var hostSys = cmp.Or(os.Getenv("HOST_SYS"), "/sys")

// isPhysical reports whether the interface name is backed by a
// device, as a NIC is and a bridge, veth, or tunnel is not.
func isPhysical(name string) bool {
	_, err := os.Stat(filepath.Join(hostSys, "class/net", name, "device"))
	return err == nil
}

// rates are the bits per second received and sent between two
// readings, added up over the interfaces in both. An interface that
// comes or goes in between, or whose counters went back, as when it is
// reset, is left out, so its lifetime's bytes are not taken for
// traffic; if that leaves none, the rates are a gap.
func rates(before, after counters) (down, up float64) {
	if after.at.Sub(before.at) < metric.MinElapsed {
		return math.NaN(), math.NaN()
	}
	elapsed := after.at.Sub(before.at).Seconds()
	var recv, sent uint64
	counted := false
	for name, a := range after.bytes {
		b, ok := before.bytes[name]
		if !ok || a.recv < b.recv || a.sent < b.sent {
			continue
		}
		counted = true
		recv += a.recv - b.recv
		sent += a.sent - b.sent
	}
	if !counted {
		return math.NaN(), math.NaN()
	}
	return float64(recv) * 8 / elapsed, float64(sent) * 8 / elapsed
}

type state struct {
	iface     string
	physical  bool
	interval  time.Duration
	down, up  []float64
	errorText string
}

// state is what view shows. n.mu is held.
func (n *network) state() state {
	return state{
		iface:     n.iface,
		physical:  n.physical,
		interval:  n.interval,
		down:      n.down.Samples(),
		up:        n.up.Samples(),
		errorText: n.errorText,
	}
}

func view(st state) sdk.View {
	span := metric.Span(st.interval)
	// Loading until the first rate; a gap later, as when counters are
	// reset, is a gap.
	loading := st.errorText == "" && !slices.ContainsFunc(st.down, func(v float64) bool { return !math.IsNaN(v) })
	direction := func(label string, samples []float64, tone sdk.Tone) sdk.Widget {
		last, previous := samples[len(samples)-1], samples[len(samples)-2]
		value, unit := metric.BitRate(last)
		stat := sdk.Stat(label, value).Unit(unit)
		if c := delta(last, previous); c != "" {
			stat.Caption(c)
		}
		if loading {
			stat.Loading()
		}
		return sdk.Weighted(1, sdk.Column(
			stat,
			sdk.Sparkline("", samples...).Range(0, top(samples)).Unit("b/s").Labels(span, "now").Tone(tone),
		).Gap(sdk.SpacingSmall))
	}
	name := st.iface
	switch {
	case name != "":
	case st.physical:
		name = "Physical interfaces"
	default:
		name = "All interfaces"
	}
	body := sdk.Column(
		sdk.Row(
			direction("Down", st.down, sdk.ToneAccent),
			direction("Up", st.up, sdk.ToneOK),
		).Gap(sdk.SpacingLarge),
	)
	if st.errorText != "" {
		body.Add(sdk.Text(st.errorText).Size(sdk.SizeSmall).Tone(sdk.ToneError))
		body.Add(sdk.Text("Retrying; the details are in the panel's logs.").Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
	} else {
		body.Add(sdk.Text(name).Size(sdk.SizeSmall).Tone(sdk.ToneMuted).Monospace())
	}
	return sdk.View{Title: "Network throughput", Body: body}
}

// top is the top of a sparkline's range: its largest sample, but at
// least 1 b/s, so an idle interface's line lies along the bottom rather
// than across the middle.
func top(samples []float64) float64 {
	t := 1.0
	for _, v := range samples {
		if v > t {
			t = v
		}
	}
	return t
}

// delta formats the change from previous to last, such as
// "+2.4 Mb/s · last sample", or "" if either is missing.
func delta(last, previous float64) string {
	if math.IsNaN(last) || math.IsNaN(previous) {
		return ""
	}
	d := last - previous
	value, unit := metric.BitRate(math.Abs(d))
	sign := "+"
	switch {
	case value == "0":
		sign = "±"
	case d < 0:
		sign = "−"
	}
	return sign + value + " " + unit + " · last sample"
}
