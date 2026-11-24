// SPDX-License-Identifier: GPL-3.0-or-later

// Command weather is the current conditions panel: the temperature, the
// sky, how warm it feels, and the humidity at one place, from
// Open-Meteo, which needs no API key. The place is a name to look up or
// its coordinates; the units and how often to fetch are settings.
package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/sdk"
)

const schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "location": {
      "type": "string",
      "description": "A place to look up by name, such as Berlin, or Springfield, Illinois; or give latitude and longitude instead."
    },
    "latitude": {
      "type": "number",
      "minimum": -90,
      "maximum": 90,
      "description": "Degrees north (south below 0), with longitude, instead of a location."
    },
    "longitude": {
      "type": "number",
      "minimum": -180,
      "maximum": 180,
      "description": "Degrees east (west below 0), with latitude."
    },
    "label": {
      "type": "string",
      "description": "The panel's title; by default the place's name."
    },
    "units": {
      "enum": ["metric", "imperial"],
      "description": "Degrees Celsius (metric) or Fahrenheit (imperial).",
      "default": "metric"
    },
    "refresh": {
      "type": "string",
      "description": "How often to fetch the weather, from 5m to 6h.",
      "default": "15m"
    }
  },
  "dependentRequired": {"latitude": ["longitude"], "longitude": ["latitude"]},
  "additionalProperties": false
}`

type settings struct {
	Location  string   `json:"location"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Label     string   `json:"label"`
	Units     string   `json:"units"`
	Refresh   string   `json:"refresh"`
}

// config is the settings, checked.
type config struct {
	// query is what to fetch for: a place to look up, or its
	// coordinates, and the units.
	query   query
	label   string
	refresh time.Duration
}

// query is what a fetch asks Open-Meteo for.
type query struct {
	location string // a name to look up, or "" for the coordinates
	lat, lon float64
	imperial bool
}

// weather is the panel's state. Configure and the loop that fetches run
// concurrently, so mu guards it. The loop starts on the first
// Configure.
type weather struct {
	views chan<- sdk.View
	start sync.Once
	// wake tells the loop to fetch now: what to fetch changed.
	wake chan struct{}

	mu  sync.Mutex
	cfg config
	gen int // counts the queries, so a fetch for an old one is dropped
	// place is where the query's location was found, kept so the name
	// is looked up once.
	place *place
	cur   *current // the latest conditions fetched, or nil
	err   error    // why the latest fetch failed, or nil
	// tried is when the latest fetch ended, by the wall clock, which
	// keeps counting while the computer sleeps.
	tried    time.Time
	failures int // fetches in a row that failed
	cancel   context.CancelFunc
}

func main() {
	views := make(chan sdk.View)
	w := &weather{views: views, wake: make(chan struct{}, 1)}
	sdk.Serve(sdk.Plugin{
		Name:         "weather",
		Version:      tessera.Version,
		Description:  "The current weather at one place, from Open-Meteo.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 1, Rows: 1},
		MinBody:      minBody,
		Views:        views,
		Configure:    w.configure,
	})
}

func (w *weather) configure(_ context.Context, c sdk.Config) error {
	s := settings{Units: "metric", Refresh: "15m"}
	if err := c.Decode(&s); err != nil {
		return err
	}
	cfg, err := check(s)
	if err != nil {
		return sdk.InvalidSettings("%v", err)
	}
	w.mu.Lock()
	same := w.gen > 0 && cfg.query == w.cfg.query
	w.cfg = cfg
	if !same {
		// Another place or other units: what was fetched no longer
		// holds.
		w.gen++
		w.place, w.cur, w.err, w.failures = nil, nil, nil, 0
		if w.cancel != nil {
			w.cancel()
		}
	}
	w.show(time.Now())
	w.mu.Unlock()
	if !same {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	w.start.Do(func() { go w.loop() })
	return nil
}

// check checks the settings.
func check(s settings) (config, error) {
	cfg := config{label: strings.TrimSpace(s.Label)}
	location := strings.TrimSpace(s.Location)
	switch {
	case location != "" && (s.Latitude != nil || s.Longitude != nil):
		return config{}, fmt.Errorf("give a location or latitude and longitude, not both")
	case location != "":
		if strings.TrimSpace(strings.Split(location, ",")[0]) == "" {
			return config{}, fmt.Errorf("location: %q names no place", s.Location)
		}
		cfg.query.location = location
	case s.Latitude == nil || s.Longitude == nil:
		return config{}, fmt.Errorf("give a location, such as Berlin, or latitude and longitude")
	case math.IsNaN(*s.Latitude) || math.Abs(*s.Latitude) > 90:
		return config{}, fmt.Errorf("latitude: %v is not from -90 to 90", *s.Latitude)
	case math.IsNaN(*s.Longitude) || math.Abs(*s.Longitude) > 180:
		return config{}, fmt.Errorf("longitude: %v is not from -180 to 180", *s.Longitude)
	default:
		cfg.query.lat, cfg.query.lon = *s.Latitude, *s.Longitude
	}
	switch strings.ToLower(strings.TrimSpace(s.Units)) {
	case "", "metric":
	case "imperial":
		cfg.query.imperial = true
	default:
		return config{}, fmt.Errorf("units: %q is not metric or imperial", s.Units)
	}
	refresh, err := time.ParseDuration(cmp.Or(strings.TrimSpace(s.Refresh), "15m"))
	if err != nil || refresh < 5*time.Minute || refresh > 6*time.Hour {
		return config{}, fmt.Errorf("refresh: %q is not a duration from 5m to 6h, such as 15m", s.Refresh)
	}
	cfg.refresh = refresh
	return cfg, nil
}

// loop fetches the weather, then again whenever what to fetch changes
// or the refresh interval passes, and shows each minute how old what it
// shows is.
func (w *weather) loop() {
	for {
		w.mu.Lock()
		// This fetch is for the settings now, so a wake from Configure
		// before it is spent.
		select {
		case <-w.wake:
		default:
		}
		q, gen, known := w.cfg.query, w.gen, w.place
		ctx, cancel := context.WithCancel(context.Background())
		w.cancel = cancel
		w.mu.Unlock()

		p, cur, err := fetch(ctx, q, known)
		cancel()

		w.mu.Lock()
		if w.gen == gen {
			w.place = p
			w.tried = time.Now().Round(0)
			if err != nil {
				if w.err == nil || err.Error() != w.err.Error() {
					log.Print(err)
				}
				w.err = err
				w.failures++
			} else {
				w.cur, w.err, w.failures = cur, nil, 0
			}
			w.show(time.Now())
		}
		w.mu.Unlock()
		w.waitDue()
	}
}

// waitDue returns when the next fetch is due: on a wake, or when the
// time to wait after the latest fetch has passed by the wall clock,
// which a timer alone misses while the computer sleeps. Meanwhile it
// shows the view each minute, as what it shows ages.
func (w *weather) waitDue() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-w.wake:
			return
		case <-tick.C:
		}
		w.mu.Lock()
		now := time.Now()
		w.show(now)
		due := now.Round(0).Sub(w.tried) >= retryAfter(w.failures, w.cfg.refresh)
		w.mu.Unlock()
		if due {
			return
		}
	}
}

// retryAfter is how long to wait before fetching again after failures
// fetches in a row failed, as when the computer starts or wakes before
// its network: a minute, doubling, up to refresh.
func retryAfter(failures int, refresh time.Duration) time.Duration {
	if failures == 0 {
		return refresh
	}
	return min(refresh, time.Minute<<min(failures-1, 10))
}

// show sends the view at now. w.mu is held.
func (w *weather) show(now time.Time) {
	w.views <- view(now, panelState{
		label:   w.cfg.label,
		place:   w.place,
		query:   w.cfg.query,
		cur:     w.cur,
		err:     w.err,
		refresh: w.cfg.refresh,
	})
}
