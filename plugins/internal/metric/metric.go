// SPDX-License-Identifier: GPL-3.0-or-later

// Package metric holds what the system panels share: the polling
// interval setting, a history of samples for a sparkline, a loop that
// polls, a time limit on each sample, and number formatting.
package metric

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joncbenderkh/tessera/sdk"
)

// The bounds on the interval setting.
const (
	MinInterval = time.Second
	MaxInterval = time.Hour
)

// IntervalSchema is the JSON Schema of the interval setting, with def
// as its default.
func IntervalSchema(def string) string {
	return `"interval": {
      "type": "string",
      "description": "How often to sample, such as 2s or 1m, from 1s to 1h.",
      "default": "` + def + `"
    }`
}

// ParseInterval reads the interval setting s, a Go duration from
// MinInterval to MaxInterval. An invalid one rejects the settings.
func ParseInterval(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, sdk.InvalidSettings("interval %q is not a duration such as 2s", s)
	}
	if d < MinInterval || d > MaxInterval {
		return 0, sdk.InvalidSettings("interval %v is not between %v and %v", d, MinInterval, MaxInterval)
	}
	return d, nil
}

// HistoryLen is how many samples a sparkline shows.
const HistoryLen = 60

// History is the last HistoryLen samples, oldest first.
type History struct {
	samples []float64
}

// Add appends a sample, dropping the oldest once there are HistoryLen.
func (h *History) Add(v float64) {
	if len(h.samples) == HistoryLen {
		h.samples = append(h.samples[:0], h.samples[1:]...)
	}
	h.samples = append(h.samples, v)
}

// Reset drops every sample.
func (h *History) Reset() { h.samples = h.samples[:0] }

// Samples returns HistoryLen samples, oldest first, with NaN, a gap,
// before the first one taken, so the newest is always at the right
// end.
func (h *History) Samples() []float64 {
	out := make([]float64, HistoryLen-len(h.samples), HistoryLen)
	for i := range out {
		out[i] = math.NaN()
	}
	return append(out, h.samples...)
}

// Len is how many samples have been added since the last Reset, at
// most HistoryLen.
func (h *History) Len() int { return len(h.samples) }

// Span is the caption at the left end of a sparkline of HistoryLen
// samples taken every interval, such as "1m58s ago".
func Span(interval time.Duration) string {
	// HistoryLen samples are HistoryLen-1 intervals apart.
	span := (interval * (HistoryLen - 1)).Round(time.Second).String()
	if strings.HasSuffix(span, "m0s") {
		span = strings.TrimSuffix(span, "0s")
	}
	if strings.HasSuffix(span, "h0m") {
		span = strings.TrimSuffix(span, "0m")
	}
	return span + " ago"
}

// Poller calls a poll function in the background, again after the
// delay it returns each time, counted from when that poll started, so
// the time a poll takes does not stretch the interval; or sooner when
// woken.
type Poller struct {
	start sync.Once
	wake  chan struct{}

	mu     sync.Mutex
	cancel context.CancelFunc
}

// Start begins polling with poll, the first time it is called; later
// calls only wake the poller, so poll runs again now, and cancel the
// context of a poll running, whose settings are then out of date.
func (p *Poller) Start(poll func(ctx context.Context) time.Duration) {
	started := false
	p.start.Do(func() {
		started = true
		p.wake = make(chan struct{}, 1)
		go p.run(poll)
	})
	if started {
		return
	}
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Poller) run(poll func(ctx context.Context) time.Duration) {
	for {
		ctx, cancel := context.WithCancel(context.Background())
		p.mu.Lock()
		p.cancel = cancel
		p.mu.Unlock()
		// This poll answers any wake so far; one that comes while it
		// runs polls again.
		select {
		case <-p.wake:
		default:
		}
		started := time.Now()
		next := poll(ctx) - time.Since(started)
		p.mu.Lock()
		p.cancel = nil
		p.mu.Unlock()
		cancel()
		if next <= 0 {
			continue
		}
		t := time.NewTimer(next)
		select {
		case <-t.C:
		case <-p.wake:
			t.Stop()
		}
	}
}

// MinElapsed is the shortest time between two samples that a rate is
// measured over; over less, a few bytes or a clock tick of CPU time
// would read as a spike.
const MinElapsed = 250 * time.Millisecond

// ErrTimeout is the error of a sample that took longer than its limit,
// or that did not start because an earlier one is still running.
var ErrTimeout = errors.New("sampling timed out")

// Limit is how long a sample taken every interval may take: twice the
// interval, from 5 seconds to 30.
func Limit(interval time.Duration) time.Duration {
	return min(max(2*interval, 5*time.Second), 30*time.Second)
}

// Sampler takes samples with a time limit, so a call into the system
// that hangs, as statfs does on a network mount that went away, shows
// as an error instead of stopping the poller. A sample that misses its
// limit goes on running, and until it returns, a later one waits for
// it, within its own limit, so at most one is ever stuck.
type Sampler struct {
	mu sync.Mutex
	// running is closed when the sample running returns; nil if none
	// is.
	running chan struct{}
}

// Busy reports whether a sample is running, perhaps past its limit.
func (s *Sampler) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running != nil
}

// Sample returns what sample returns, or ErrTimeout if it, or an
// earlier sample still running, takes longer than limit, or ctx's
// error if ctx ends first. sample gets a context that ends then too.
func Sample[T any](ctx context.Context, s *Sampler, limit time.Duration, sample func(context.Context) (T, error)) (T, error) {
	var zero T
	limited, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ended := func() (T, error) {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return zero, ErrTimeout
	}
	s.mu.Lock()
	for s.running != nil {
		earlier := s.running
		s.mu.Unlock()
		select {
		case <-earlier:
		case <-limited.Done():
			return ended()
		}
		s.mu.Lock()
	}
	if limited.Err() != nil {
		// Ended before the sample started, so none is started that
		// could get stuck past the caller.
		s.mu.Unlock()
		return ended()
	}
	running := make(chan struct{})
	s.running = running
	s.mu.Unlock()

	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := sample(limited)
		s.mu.Lock()
		s.running = nil
		close(running)
		s.mu.Unlock()
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		if err := ctx.Err(); err != nil {
			// Ended too, while the sample returned.
			return zero, err
		}
		return r.v, r.err
	case <-limited.Done():
		return ended()
	}
}

// Percent formats a percentage to one decimal, such as "24.8"; NaN is
// "".
func Percent(v float64) string {
	if math.IsNaN(v) {
		return ""
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// Delta formats the change from previous to last, in percentage
// points, such as "−2.1 pp · last sample"; it is "" if either is NaN.
func Delta(last, previous float64) string {
	if math.IsNaN(last) || math.IsNaN(previous) {
		return ""
	}
	return signed(last-previous) + " pp · last sample"
}

// signed formats v to one decimal with its sign, a true minus sign for
// a negative one.
func signed(v float64) string {
	s := strconv.FormatFloat(math.Abs(v), 'f', 1, 64)
	if s == "0.0" {
		return "±0.0"
	}
	if v < 0 {
		return "−" + s
	}
	return "+" + s
}

// rollover is the smallest value that takes the next unit up, so no
// value rounds to four digits.
const rollover = 999.5

var binaryUnits = []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}

// Bytes formats a size in binary units, such as ("12.4", "GiB"): at
// most three digits, with one decimal below 100 of a unit beyond bytes.
func Bytes(n uint64) (value, unit string) {
	v, i := float64(n), 0
	for v >= rollover && i < len(binaryUnits)-1 {
		v /= 1024
		i++
	}
	return scaled(v, i), binaryUnits[i]
}

// BytesString is Bytes as one string, such as "12.4 GiB".
func BytesString(n uint64) string {
	v, u := Bytes(n)
	return v + " " + u
}

var bitRateUnits = []string{"b/s", "kb/s", "Mb/s", "Gb/s", "Tb/s"}

// BitRate formats a rate of at least 0 bits per second in decimal
// units, such as ("48.2", "Mb/s"); see Bytes for the decimals. NaN is
// ("", "b/s").
func BitRate(bps float64) (value, unit string) {
	if math.IsNaN(bps) {
		return "", bitRateUnits[0]
	}
	v, i := bps, 0
	for v >= rollover && i < len(bitRateUnits)-1 {
		v /= 1000
		i++
	}
	return scaled(v, i), bitRateUnits[i]
}

// scaled formats v, in the unit i steps above the base one.
func scaled(v float64, i int) string {
	if i == 0 || v >= 99.95 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}
