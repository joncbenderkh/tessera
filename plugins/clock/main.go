// SPDX-License-Identifier: GPL-3.0-or-later

// Command clock is the clock panel: the time to the second, the date,
// and the time zone, which is a setting.
package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
    "timezone": {
      "type": "string",
      "description": "An IANA time zone, such as Europe/Berlin, or Local for the system's.",
      "default": "Local"
    }
  },
  "additionalProperties": false
}`

type settings struct {
	Timezone string `json:"timezone"`
}

// clock is the panel's state. Configure and the ticker run
// concurrently, so mu guards it. The ticker starts on the first
// Configure, so no view shows a time zone the settings do not ask for.
type clock struct {
	views chan<- sdk.View
	start sync.Once

	mu   sync.Mutex
	zone string
	loc  *time.Location
}

func main() {
	views := make(chan sdk.View)
	c := &clock{views: views}
	sdk.Serve(sdk.Plugin{
		Name:         "clock",
		Version:      tessera.Version,
		Description:  "The time, date, and time zone.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 1, Rows: 1},
		Views:        views,
		Configure:    c.configure,
	})
}

func (c *clock) configure(_ context.Context, cfg sdk.Config) error {
	s := settings{Timezone: "Local"}
	if err := cfg.Decode(&s); err != nil {
		return err
	}
	loc, zone, err := location(s.Timezone)
	if err != nil {
		return sdk.InvalidSettings("%v", err)
	}
	c.mu.Lock()
	c.zone, c.loc = zone, loc
	c.show(time.Now())
	c.mu.Unlock()
	c.start.Do(func() { go c.tick() })
	return nil
}

// location loads the time zone name, in any case, and returns it with
// its IANA spelling. Only the names in zoneList are accepted, so a
// setting means the same on every system; "" and "Local" are the
// system's. Surrounding spaces are ignored.
func location(name string) (*time.Location, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "Local") {
		return time.Local, "Local", nil
	}
	spelt, ok := zoneNames[strings.ToLower(name)]
	if !ok {
		return nil, name, fmt.Errorf("unknown time zone %q", name)
	}
	loc, err := time.LoadLocation(spelt)
	return loc, spelt, err
}

// zoneNames maps the zones in zoneList, in lower case, to their IANA
// spelling. The system's zone database would otherwise accept names
// that others reject: misspelt ones on file systems that ignore case,
// as macOS's do by default, and its own extras such as posix/ paths.
var zoneNames = func() map[string]string {
	names := map[string]string{}
	for name := range strings.Lines(zoneList) {
		name = strings.TrimSpace(name)
		names[strings.ToLower(name)] = name
	}
	return names
}()

// tick shows the time now, then again at the start of every second.
func (c *clock) tick() {
	for {
		now := time.Now()
		c.mu.Lock()
		c.show(now)
		c.mu.Unlock()
		// Never more than a second, even when the wall clock is set back.
		time.Sleep(time.Second - time.Duration(time.Now().Nanosecond()))
	}
}

// show sends the view of now. c.mu is held.
func (c *clock) show(now time.Time) {
	c.views <- view(now.In(c.loc), c.zone)
}

// view shows now, in the time zone named zone.
func view(now time.Time, zone string) sdk.View {
	return sdk.View{
		Title: "Clock",
		Body: sdk.Column(
			sdk.Text(now.Format("15:04:05")).Size(sdk.SizeDisplay),
			sdk.Text(now.Format("Monday, January 2")).Tone(sdk.ToneMuted),
			sdk.Text(zoneLabel(now, zone)).Size(sdk.SizeSmall).Tone(sdk.ToneMuted).Monospace(),
		).Gap(sdk.SpacingSmall),
	}
}

// zoneLabel names the time zone of t: its name, and the abbreviation
// in effect, such as "Europe/Berlin · CET".
func zoneLabel(t time.Time, zone string) string {
	abbr, _ := t.Zone()
	if abbr == "" || abbr == zone {
		return zone
	}
	return zone + " · " + abbr
}
