// SPDX-License-Identifier: GPL-3.0-or-later

// Command calendar is the next events panel: the events of one or more
// iCalendar feeds, read from URLs or files, for the rest of the day.
// Activating an event shows its details below the list, and the Hide
// details item at their end hides them.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	// The time zone database, for systems without one, such as Windows.
	_ "time/tzdata"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/sdk"
)

const schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "sources": {
      "type": "array",
      "description": "iCalendar feeds: http, https, or webcal URLs, absolute paths, or paths starting with ~/.",
      "items": {"type": "string"},
      "minItems": 1
    },
    "title": {
      "type": "string",
      "description": "The panel's title; empty keeps the default.",
      "default": "Next events"
    },
    "refresh": {
      "type": "string",
      "description": "How often to read the feeds again, from 1m to 24h.",
      "default": "15m"
    }
  },
  "required": ["sources"],
  "additionalProperties": false
}`

type settings struct {
	Sources []string `json:"sources"`
	Refresh string   `json:"refresh"`
	Title   string   `json:"title"`
}

// config is the settings, checked.
type config struct {
	sources []source
	places  []int // each source's place in the settings, from 1
	refresh time.Duration
	title   string
}

// read is the outcome of reading one source: its feed, the last one
// read if this read failed, and the feed's occurrences from the time of
// the read until two days after the day shown, so that the panel can
// show them until the next read without following every rule again.
type read struct {
	feed   feed
	events []event
	err    error
	done   bool // whether the source has been read
}

// calendar is the panel's state. Configure, Activate, the loop that
// reads the feeds, and the minute ticks run concurrently, so mu guards
// it. The loop and the ticks start on the first Configure.
type calendar struct {
	views chan<- sdk.View
	start sync.Once
	// wake tells the loop to read the feeds now: the settings changed,
	// or the refresh interval passed while the computer slept.
	wake chan struct{}
	// retick tells the ticks that the next change to the list moved.
	retick chan struct{}

	mu       sync.Mutex
	cfg      config
	gen      int    // counts the settings, so a read of old ones is dropped
	reads    []read // by source
	readAt   time.Time
	selected string    // the key of the event whose details show
	next     time.Time // when a listed event next starts or ends
	// cancel stops the reads of the feeds under way.
	cancel context.CancelFunc
}

func main() {
	views := make(chan sdk.View)
	c := &calendar{views: views, wake: make(chan struct{}, 1), retick: make(chan struct{}, 1)}
	sdk.Serve(sdk.Plugin{
		Name:         "calendar",
		Version:      tessera.Version,
		Description:  "The rest of today's events in iCalendar feeds.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 1, Rows: 2},
		MinBody:      minBody,
		Views:        views,
		Configure:    c.configure,
		Activate:     c.activate,
	})
}

func (c *calendar) configure(_ context.Context, cfg sdk.Config) error {
	s := settings{Refresh: "15m"}
	if err := cfg.Decode(&s); err != nil {
		return err
	}
	checked, err := check(s)
	if err != nil {
		return sdk.InvalidSettings("%v", err)
	}
	c.mu.Lock()
	if c.reads != nil && slices.Equal(checked.sources, c.cfg.sources) && checked.refresh == c.cfg.refresh {
		// Only the title, or the tile's size, changed: the feeds as read
		// still hold.
		c.cfg = checked
		c.show(time.Now())
		c.mu.Unlock()
		return nil
	}
	if !slices.Equal(checked.sources, c.cfg.sources) || c.reads == nil {
		c.reads = carry(c.cfg.sources, c.reads, checked.sources)
	}
	c.cfg = checked
	c.gen++
	if c.cancel != nil {
		c.cancel()
	}
	c.show(time.Now())
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	c.start.Do(func() {
		go c.loop()
		go c.tick()
	})
	return nil
}

// carry starts the reads of sources, keeping the feed last read from
// each that was among old, whose reads are reads, and its events: they
// show until it is read again, and the feed is fallen back on if that
// fails.
func carry(old []source, reads []read, sources []source) []read {
	next := make([]read, len(sources))
	for i, src := range sources {
		if j := slices.Index(old, src); j >= 0 && j < len(reads) {
			next[i].feed, next[i].events = reads[j].feed, reads[j].events
		}
	}
	return next
}

// check checks the settings.
func check(s settings) (config, error) {
	if len(s.Sources) == 0 {
		return config{}, fmt.Errorf("sources: give at least one iCalendar feed")
	}
	refresh, err := time.ParseDuration(cmp.Or(strings.TrimSpace(s.Refresh), "15m"))
	if err != nil || refresh < time.Minute || refresh > 24*time.Hour {
		return config{}, fmt.Errorf("refresh: %q is not a duration from 1m to 24h, such as 15m", s.Refresh)
	}
	cfg := config{refresh: refresh, title: cmp.Or(strings.TrimSpace(s.Title), "Next events")}
	for i, raw := range s.Sources {
		src, err := parseSource(raw)
		if err != nil {
			return config{}, err
		}
		// The same feed twice would only show its events twice over.
		if !slices.Contains(cfg.sources, src) {
			cfg.sources = append(cfg.sources, src)
			cfg.places = append(cfg.places, i+1)
		}
	}
	return cfg, nil
}

func (c *calendar) activate(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key == hideAction {
		key = ""
	}
	c.selected = key
	c.show(time.Now())
	return nil
}

// loop reads the feeds, then again whenever the settings change or
// their refresh interval passes. Each source's events show as soon as it
// has been read, so a slow one holds up only itself.
func (c *calendar) loop() {
	failures := 0
	for {
		c.mu.Lock()
		// This read is for the settings now, so a wake from Configure
		// before it is spent.
		select {
		case <-c.wake:
		default:
		}
		cfg, gen, last := c.cfg, c.gen, slices.Clone(c.reads)
		ctx, cancel := context.WithCancel(context.Background())
		c.cancel = cancel
		// The wall clock, which keeps counting while the computer sleeps.
		c.readAt = time.Now().Round(0)
		c.mu.Unlock()
		now := time.Now().Local()
		y, m, d := now.Date()
		to := time.Date(y, m, d+3, 0, 0, 0, 0, now.Location())
		var wg sync.WaitGroup
		var failed atomic.Bool
		for i, src := range cfg.sources {
			wg.Go(func() {
				r := readSource(ctx, src, last[i].feed, now, to)
				// A read cancelled for new settings did not fail.
				if r.err != nil && !errors.Is(r.err, errTooSlow) && ctx.Err() == nil {
					failed.Store(true)
				}
				c.mu.Lock()
				if c.gen == gen {
					c.reads[i] = r
					c.show(time.Now())
				}
				c.mu.Unlock()
			})
		}
		wg.Wait()
		cancel()
		if failed.Load() {
			failures++
		} else {
			failures = 0
		}
		due := time.NewTimer(retryAfter(failures, cfg.refresh))
		select {
		case <-c.wake:
		case <-due.C:
		}
		due.Stop()
	}
}

// retryAfter is how long to wait before reading the feeds again after
// failures rounds of reads in a row with one failing, as when the
// computer starts or wakes before its network: a minute, doubling, up
// to refresh.
func retryAfter(failures int, refresh time.Duration) time.Duration {
	if failures == 0 {
		return refresh
	}
	return min(refresh, time.Minute<<min(failures-1, 10))
}

// expandBudget is how long a feed's recurring events may take to follow
// before the rest are left out.
var expandBudget = 2 * time.Second

var errTooSlow = errors.New("some repeating events took too long to follow and are left out")

// readSource reads src, falling back to last, its feed as read before,
// if it fails, and follows the feed's events from now until to.
func readSource(ctx context.Context, src source, last feed, now, to time.Time) read {
	f, err := src.fetch(ctx)
	if err != nil {
		f = last
	}
	r := read{feed: f, err: err, done: true}
	if f.cal != nil {
		budget, cancel := context.WithTimeout(ctx, expandBudget)
		defer cancel()
		var complete bool
		r.events, complete = f.occurrences(budget, now, to, now.Location())
		if !complete && ctx.Err() == nil && r.err == nil {
			r.err = errTooSlow
		}
		for j := range r.events {
			r.events[j].source = cmp.Or(f.name, src.String())
		}
	}
	return r
}

// tick shows the events at the start of every minute and whenever a
// listed one starts or ends, and wakes the loop if the refresh interval
// has passed by the wall clock, as it does while the computer sleeps,
// when the loop's timer stops.
func (c *calendar) tick() {
	for {
		now := time.Now()
		at := now.Truncate(time.Minute).Add(time.Minute)
		c.mu.Lock()
		if !c.next.IsZero() && c.next.Before(at) {
			at = c.next
		}
		c.mu.Unlock()
		timer := time.NewTimer(at.Sub(now))
		select {
		case <-c.retick:
			timer.Stop()
			continue
		case <-timer.C:
		}
		c.mu.Lock()
		c.show(time.Now())
		due := !c.readAt.IsZero() && time.Now().Round(0).Sub(c.readAt) >= c.cfg.refresh
		c.mu.Unlock()
		if due {
			select {
			case c.wake <- struct{}{}:
			default:
			}
		}
	}
}

// nextChange is when one of events next starts or ends after now, or
// zero if none does.
func nextChange(now time.Time, events []event) time.Time {
	var next time.Time
	for _, e := range events {
		for _, t := range []time.Time{e.start, e.end} {
			if t.After(now) && (next.IsZero() || t.Before(next)) {
				next = t
			}
		}
	}
	return next
}

// label names source i, by its host or file name, and its place among
// the sources if another has the same name. c.mu is held.
func (c *calendar) label(i int) string {
	name := c.cfg.sources[i].String()
	for j, src := range c.cfg.sources {
		if j != i && src.String() == name {
			return fmt.Sprintf("%s (source %d)", name, c.cfg.places[i])
		}
	}
	return name
}

// show sends the view at now. c.mu is held.
func (c *calendar) show(now time.Time) {
	now = now.Local()
	from := now
	y, m, d := now.Date()
	to := time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())
	a := agenda{title: c.cfg.title, loaded: !slices.ContainsFunc(c.reads, func(r read) bool { return !r.done })}
	seen := map[string]bool{}
	for i, r := range c.reads {
		if r.err != nil {
			a.failed = append(a.failed, c.label(i)+": "+r.err.Error())
		}
		for _, e := range r.events {
			if seen[e.key] || !e.end.After(from) || !e.start.Before(to) {
				continue
			}
			seen[e.key] = true
			a.events = append(a.events, e)
		}
	}
	slices.SortStableFunc(a.events, func(x, y event) int { return x.start.Compare(y.start) })
	if !slices.ContainsFunc(a.events, func(e event) bool { return e.key == c.selected }) && a.loaded {
		c.selected = ""
	}
	if next := nextChange(now, a.events); !next.Equal(c.next) {
		c.next = next
		select {
		case c.retick <- struct{}{}:
		default:
		}
	}
	c.views <- view(now, a, c.selected)
}

// minBody is the least room the panel's view needs, in dp: room for
// the date, the count, and two events of the list.
var minBody = sdk.BodySize{Width: 168, Height: 209}
