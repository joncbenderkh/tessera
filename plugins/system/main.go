// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

// Command system is the CPU and memory panel: how busy the CPUs are and
// how much memory is in use, each with a sparkline of its recent
// samples. How often it samples is a setting.
package main

import (
	"context"
	"errors"
	"log"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/plugins/internal/metric"
	"github.com/joncbenderkh/tessera/sdk"
)

const defaultInterval = "2s"

var schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    ` + metric.IntervalSchema(defaultInterval) + `
  },
  "additionalProperties": false
}`

type settings struct {
	Interval string `json:"interval"`
}

// system is the panel's state. Configure and the poller run
// concurrently, so mu guards it. Polling starts on the first Configure,
// so no view samples at an interval the settings do not ask for.
type system struct {
	views   chan<- sdk.View
	poller  metric.Poller
	sampler metric.Sampler

	mu       sync.Mutex
	interval time.Duration
	cores    int
	cpu      metric.History
	memory   metric.History
	last     *cpu.TimesStat
	lastAt   time.Time
	used     uint64
	total    uint64
	err      error
}

func main() {
	views := make(chan sdk.View)
	s := &system{views: views}
	s.cores, _ = cpu.Counts(true)
	sdk.Serve(sdk.Plugin{
		Name:         "system",
		Version:      tessera.Version,
		Description:  "CPU and memory use, with their recent history.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 2, Rows: 2},
		Views:        views,
		Configure:    s.configure,
	})
}

func (s *system) configure(_ context.Context, cfg sdk.Config) error {
	set := settings{Interval: defaultInterval}
	if err := cfg.Decode(&set); err != nil {
		return err
	}
	interval, err := metric.ParseInterval(set.Interval)
	if err != nil {
		return err
	}
	s.mu.Lock()
	changed := interval != s.interval
	if changed {
		s.interval = interval
		s.last = nil
		s.cpu.Reset()
		s.memory.Reset()
	}
	s.mu.Unlock()
	if changed {
		s.poller.Start(s.poll)
	}
	return nil
}

// cpuTimes reads the CPU times; tests replace it.
var cpuTimes = cpu.TimesWithContext

// poll samples the CPU and memory, shows them, and returns when to
// poll next: soon after the first sample, which has nothing to compare
// the CPU times with, so it is only the baseline for the next, of
// memory too, which keeps the two histories in step.
func (s *system) poll(ctx context.Context) time.Duration {
	s.mu.Lock()
	interval := s.interval
	s.mu.Unlock()
	type reading struct {
		at             time.Time
		times          []cpu.TimesStat
		vm             *mem.VirtualMemoryStat
		cpuErr, memErr error
	}
	r, err := metric.Sample(ctx, &s.sampler, metric.Limit(interval), func(ctx context.Context) (reading, error) {
		r := reading{at: time.Now()}
		r.times, r.cpuErr = cpuTimes(ctx, false)
		r.vm, r.memErr = mem.VirtualMemoryWithContext(ctx)
		return r, nil
	})
	if ctx.Err() != nil {
		// The settings changed; the poller was woken.
		return 0
	}
	times, vm, cpuErr, memErr := r.times, r.vm, r.cpuErr, r.memErr
	if err != nil {
		cpuErr, memErr = err, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if interval != s.interval {
		// The settings changed while sampling, clearing the history,
		// which this sample is not part of; the poller was woken.
		return 0
	}
	sampled := cpuErr == nil && len(times) > 0
	if sampled && s.last != nil && r.at.Sub(s.lastAt) < metric.MinElapsed {
		// Too soon after the baseline to measure the CPU over, as when
		// a wake follows the poll that took it: keep it, and wait.
		return metric.MinElapsed
	}
	baseline := sampled && s.last == nil
	prevErr := s.err
	s.err = cpuErr
	if memErr != cpuErr {
		s.err = errors.Join(cpuErr, memErr)
	}
	switch {
	case !sampled:
		// The next reading is a baseline: measured from the last, the
		// CPU would be averaged over the whole gap.
		s.cpu.Add(math.NaN())
		s.last = nil
	case !baseline:
		s.cpu.Add(busy(*s.last, times[0]))
		fallthrough
	default:
		s.last, s.lastAt = &times[0], r.at
	}
	switch {
	case baseline:
	case memErr != nil:
		s.memory.Add(math.NaN())
	default:
		s.used, s.total = vm.Used, vm.Total
		s.memory.Add(vm.UsedPercent)
	}
	if s.err != nil && (prevErr == nil || prevErr.Error() != s.err.Error()) {
		log.Print(s.err)
	}
	s.views <- view(s.state())
	if baseline {
		return 500 * time.Millisecond
	}
	return s.interval
}

// state is what view shows. s.mu is held.
func (s *system) state() state {
	return state{
		interval: s.interval,
		cores:    s.cores,
		cpu:      s.cpu.Samples(),
		memory:   s.memory.Samples(),
		used:     s.used,
		total:    s.total,
		err:      s.err,
	}
}

// busy is the percentage of CPU time, between the samples a and b,
// spent other than idle or waiting for I/O.
func busy(a, b cpu.TimesStat) float64 {
	total := cpuTotal(b) - cpuTotal(a)
	idle := (b.Idle + b.Iowait) - (a.Idle + a.Iowait)
	if total <= 0 {
		return math.NaN()
	}
	return min(max((total-idle)/total*100, 0), 100)
}

// cpuTotal is all the CPU time in t. Guest time is already counted in
// user time, so it is left out.
func cpuTotal(t cpu.TimesStat) float64 {
	return t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
}

type state struct {
	interval    time.Duration
	cores       int
	cpu, memory []float64
	used, total uint64
	err         error
}

func view(st state) sdk.View {
	span := metric.Span(st.interval)
	cpuLast, cpuPrev := lastTwo(st.cpu)
	cpuStat := sdk.Stat("CPU utilization", metric.Percent(cpuLast)).Unit("%")
	if d := metric.Delta(cpuLast, cpuPrev); d != "" {
		cpuStat.Caption(d)
	} else if st.cores > 0 {
		cpuStat.Caption(cores(st.cores))
	}
	if math.IsNaN(cpuLast) && st.err == nil {
		cpuStat.Loading()
	}

	memLast, _ := lastTwo(st.memory)
	var memStat *sdk.StatWidget
	if math.IsNaN(memLast) {
		memStat = sdk.Stat("Memory in use", "")
		if st.err == nil {
			memStat.Loading()
		}
	} else {
		value, unit := metric.Bytes(st.used)
		memStat = sdk.Stat("Memory in use", value).Unit(unit).
			Caption("of " + metric.BytesString(st.total) + " · " + metric.Percent(memLast) + "%")
	}

	body := sdk.Column(
		sdk.Row(
			sdk.Weighted(1, sdk.Column(
				cpuStat,
				sdk.Sparkline("", st.cpu...).Range(0, 100).Unit("%").Labels(span, "now").Tone(sdk.ToneAccent),
			).Gap(sdk.SpacingSmall)),
			sdk.Weighted(1, sdk.Column(
				memStat,
				sdk.Sparkline("", st.memory...).Range(0, 100).Unit("%").Labels(span, "now"),
			).Gap(sdk.SpacingSmall)),
		).Gap(sdk.SpacingLarge),
	)
	if st.err != nil {
		body.Add(sdk.Text("Cannot read the CPU or memory").Size(sdk.SizeSmall).Tone(sdk.ToneError))
		body.Add(sdk.Text("Retrying; the details are in the panel's logs.").Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
	}
	return sdk.View{Title: "CPU & memory", Body: body}
}

// lastTwo returns the last sample and the one before it, NaN where
// there is none.
func lastTwo(samples []float64) (last, previous float64) {
	last, previous = math.NaN(), math.NaN()
	if n := len(samples); n > 1 {
		last, previous = samples[n-1], samples[n-2]
	} else if n == 1 {
		last = samples[0]
	}
	return last, previous
}

// cores names n logical CPUs, which is what the utilization is a share
// of.
func cores(n int) string {
	if n == 1 {
		return "1 CPU"
	}
	return strconv.Itoa(n) + " CPUs"
}
