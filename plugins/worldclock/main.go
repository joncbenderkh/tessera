// SPDX-License-Identifier: GPL-3.0-or-later

// Command worldclock is the world clock panel: up to four digital
// clocks, each in a time zone its settings name.
package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/plugins/internal/zones"
	"github.com/joncbenderkh/tessera/sdk"
)

// maxClocks is the most clocks one panel shows.
const maxClocks = 4

const schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "clocks": {
      "type": "array",
      "description": "The clocks, in order: 1 to 4.",
      "minItems": 1,
      "maxItems": 4,
      "items": {
        "type": "object",
        "properties": {
          "timezone": {
            "type": "string",
            "description": "An IANA time zone, such as Asia/Tokyo, or Local for the system's.",
            "default": "Local"
          },
          "label": {
            "type": "string",
            "description": "The name above the time, on one line; the city in the time zone's name if unset."
          }
        },
        "additionalProperties": false
      },
      "default": [{"timezone": "Local"}]
    },
    "utc_offsets": {
      "type": "boolean",
      "description": "Name a time zone with no letter abbreviation by its UTC offset, such as UTC+7, on clocks with room for it.",
      "default": false
    }
  },
  "additionalProperties": false
}`

type settings struct {
	Clocks     []*clockSettings `json:"clocks"` // nil for a null entry
	UTCOffsets bool             `json:"utc_offsets"`
}

type clockSettings struct {
	Timezone string `json:"timezone"`
	Label    string `json:"label"`
}

// place is one clock: its label, and the time zone it shows.
type place struct {
	label string
	loc   *time.Location
}

// worldClock is the panel's state. Configure and the ticker run
// concurrently, so mu guards it. The ticker starts on the first
// Configure, so no view shows clocks the settings do not ask for.
type worldClock struct {
	views chan<- sdk.View
	start sync.Once

	mu         sync.Mutex
	places     []place
	utcOffsets bool
	size       sdk.Size
	shown      int64 // the minute of the latest view, in Unix minutes
}

func main() {
	views := make(chan sdk.View)
	c := &worldClock{views: views}
	sdk.Serve(sdk.Plugin{
		Name:         "worldclock",
		Version:      tessera.Version,
		Description:  "Up to four clocks, each in its own time zone.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 1, Rows: 1},
		MinBody:      minBody,
		Views:        views,
		Configure:    c.configure,
	})
}

func (c *worldClock) configure(_ context.Context, cfg sdk.Config) error {
	var s settings
	if err := cfg.Decode(&s); err != nil {
		return err
	}
	places, err := placesOf(s)
	if err != nil {
		return sdk.InvalidSettings("%v", err)
	}
	c.mu.Lock()
	c.places, c.utcOffsets, c.size = places, s.UTCOffsets, cfg.Shown
	c.show(time.Now())
	c.mu.Unlock()
	c.start.Do(func() { go c.tick() })
	return nil
}

// placesOf checks the clocks s asks for: one local clock if it names
// none, otherwise 1 to maxClocks of them, each in a known time zone.
func placesOf(s settings) ([]place, error) {
	if s.Clocks == nil {
		return []place{{label: "Local", loc: time.Local}}, nil
	}
	if n := len(s.Clocks); n < 1 || n > maxClocks {
		return nil, fmt.Errorf("setting \"clocks\": want 1 to %d clocks, not %d", maxClocks, n)
	}
	places := make([]place, len(s.Clocks))
	for i, cs := range s.Clocks {
		if cs == nil {
			return nil, fmt.Errorf("clock %d: want an object, not null", i+1)
		}
		loc, zone, err := zones.Load(cs.Timezone)
		if err != nil {
			return nil, fmt.Errorf("clock %d: %w", i+1, err)
		}
		label := strings.Join(strings.Fields(cs.Label), " ") // one line
		if label == "" {
			label = defaultLabel(zone, loc)
		}
		places[i] = place{label: label, loc: loc}
	}
	return places, nil
}

// defaultLabel names a clock in zone, loaded as loc: its UTC offset
// for a fixed Etc/GMT zone, whose name has the sign the other way
// round (Etc/GMT+5 is UTC−5), or GMT for one at zero, as for Etc/GMT
// itself, and otherwise the last part of its name with spaces for
// underscores: "Buenos Aires" for America/Argentina/Buenos_Aires.
func defaultLabel(zone string, loc *time.Location) string {
	if strings.HasPrefix(zone, "Etc/GMT") {
		if _, offset := time.Now().In(loc).Zone(); offset != 0 {
			return "UTC" + hours(offset)
		}
		return "GMT"
	}
	return strings.ReplaceAll(zone[strings.LastIndex(zone, "/")+1:], "_", " ")
}

// wake is the longest the ticker sleeps. A sleep does not count the
// time the computer is suspended, so one to the next minute could
// leave the clocks that far behind after a resume.
const wake = 5 * time.Second

// tick shows the time whenever its minute changes.
func (c *worldClock) tick() {
	for {
		now := time.Now()
		c.mu.Lock()
		show, sleep := due(now, c.shown)
		if show {
			c.show(now)
		}
		c.mu.Unlock()
		time.Sleep(sleep)
	}
}

// due says whether the minute of now is not the one shown, in Unix
// minutes, as after the minute turns or the clock is set back, and how
// long to sleep before looking again.
func due(now time.Time, shown int64) (show bool, sleep time.Duration) {
	into := time.Duration(now.Second())*time.Second + time.Duration(now.Nanosecond())
	return now.Unix()/60 != shown, min(time.Minute-into, wake)
}

// show sends the view of now. c.mu is held.
func (c *worldClock) show(now time.Time) {
	c.shown = now.Unix() / 60
	c.views <- view(now.In(time.Local), c.places, c.size, c.utcOffsets)
}

// view shows the time now at each place, laid out to suit a panel of
// size; utcOffsets is the setting. now is in the local time zone the
// clocks are compared with.
func view(now time.Time, places []place, size sdk.Size, utcOffsets bool) sdk.View {
	perRow := perRow(len(places), size)
	rows := (len(places) + perRow - 1) / perRow
	// A clock is compact, with a smaller time, tighter gaps, and a
	// shorter detail, when it has less than a grid cell to itself or
	// shares a row with two or more others: a narrow window squeezes a
	// wide panel into one column without telling the plugin.
	compact := perRow > 2 || perRow > max(size.Columns, 1) || rows > max(size.Rows, 1)
	timeSize, rowGap, lineGap := sdk.SizeDisplay, sdk.SpacingLarge, sdk.SpacingSmall
	if compact {
		timeSize, rowGap, lineGap = sdk.SizeLarge, sdk.SpacingSmall, sdk.SpacingExtraSmall
	}
	grid := sdk.Column().Gap(rowGap)
	for r := range rows {
		row := sdk.Row().Gap(sdk.SpacingLarge)
		for i := r * perRow; i < (r+1)*perRow; i++ {
			if i >= len(places) {
				row.Add(sdk.Weighted(1, sdk.Space())) // keeps the columns aligned
				continue
			}
			row.Add(sdk.Weighted(1, clock(now, places[i], timeSize, utcOffsets, compact).Gap(lineGap)))
		}
		grid.Add(row)
	}
	return sdk.View{Title: "World clock", Body: grid}
}

// clock is one place's label, time, and how it differs from local time,
// in full or, if compact, in short. A compact local clock names its
// time zone instead, unless that is its label.
func clock(now time.Time, p place, timeSize sdk.TextSize, utcOffsets, compact bool) *sdk.StackWidget {
	t := now.In(p.loc)
	c := sdk.Column(
		sdk.Text(p.label).Size(sdk.SizeSmall).Tone(sdk.ToneMuted),
		sdk.Text(t.Format("15:04")).Size(timeSize),
	)
	switch {
	case !compact:
		c.Add(sdk.Text(detail(t, now, p.label, utcOffsets)).Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
	case p.loc != now.Location():
		c.Add(sdk.Text(shortDetail(t, now)).Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
	case zoneName(t) != p.label:
		c.Add(sdk.Text(zoneName(t)).Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
	}
	return c
}

// zoneName is t's time zone abbreviation, such as CEST, or its UTC
// offset, such as UTC+7, for a zone with no letter abbreviation.
func zoneName(t time.Time) string {
	abbr, offset := t.Zone()
	if strings.Trim(abbr, "+-0123456789") != "" {
		return abbr
	}
	return "UTC" + hours(offset)
}

// detail says how t differs from local, the same instant in the local
// time zone: its day if another, its zone's abbreviation, and its
// offset, such as "Tomorrow · JST · +7 h". A zone whose abbreviation
// is only its UTC offset, such as +07, is named by it as UTC+7 if
// utcOffsets is set, and not at all otherwise. The zone is left out
// when it is its clock's label, as UTC is for the zone UTC.
func detail(t, local time.Time, label string, utcOffsets bool) string {
	var parts []string
	switch days := daysFrom(t, local); days {
	case 0:
	case 1:
		parts = append(parts, "Tomorrow")
	case -1:
		parts = append(parts, "Yesterday")
	default:
		parts = append(parts, t.Format("Mon 2 Jan"))
	}
	switch abbr, _ := t.Zone(); {
	case abbr == "":
	case abbr == label:
	case strings.Trim(abbr, "+-0123456789") != "":
		parts = append(parts, abbr)
	case utcOffsets:
		_, offset := t.Zone()
		if utc := "UTC" + hours(offset); utc != label {
			parts = append(parts, utc)
		}
	}
	return strings.Join(append(parts, offsetFrom(t, local, false)), " · ")
}

// shortDetail is detail without the zone, and the day as a shift, such
// as "+1 d · +13 h", or just "−6 h".
func shortDetail(t, local time.Time) string {
	offset := offsetFrom(t, local, true)
	if days := daysFrom(t, local); days != 0 {
		return fmt.Sprintf("%s%d d · %s", sign(days), abs(days), offset)
	}
	return offset
}

// daysFrom is how many days t's date is after local's: two at most
// with today's time zones, whose offsets span 26 hours.
func daysFrom(t, local time.Time) int {
	ty, tm, td := t.Date()
	ly, lm, ld := local.Date()
	return int(math.Round(time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC).Sub(time.Date(ly, lm, ld, 0, 0, 0, 0, time.UTC)).Hours() / 24))
}

// offsetFrom is how far t's time zone is ahead of local's, such as
// "+5:30 h" or "−3 h". The local zone itself is "Local time", and
// another one at the same offset "Same time", or "±0 h" in short.
func offsetFrom(t, local time.Time, short bool) string {
	_, to := t.Zone()
	_, lo := local.Zone()
	switch {
	case t.Location() == local.Location() && !short:
		return "Local time"
	case to == lo && short:
		return "±0 h"
	case to == lo:
		return "Same time"
	}
	return hours(to-lo) + " h"
}

// hours writes an offset of seconds in hours, with its sign, and its
// minutes if any: "+7", "−3", "+5:45".
func hours(seconds int) string {
	h, m := abs(seconds)/3600, abs(seconds)%3600/60
	if m == 0 {
		return fmt.Sprintf("%s%d", sign(seconds), h)
	}
	return fmt.Sprintf("%s%d:%02d", sign(seconds), h, m)
}

// sign is the sign of n, a minus sign for a negative one.
func sign(n int) string {
	if n < 0 {
		return "−"
	}
	return "+"
}

func abs(n int) int { return max(n, -n) }

// perRow is how many of n clocks go in a row in a panel of size: the
// layout whose clocks come nearest a landscape shape, preferring more
// per row on a tie. The last row is at most one short, so 4 clocks are
// never 3 and 1. A grid cell is about 16:9.
func perRow(n int, size sdk.Size) int {
	cols, rows := float64(max(size.Columns, 1)), float64(max(size.Rows, 1))
	best, bestOff := 1, math.Inf(1)
	for k := n; k >= 1; k-- {
		r := (n + k - 1) / k
		if n-(r-1)*k < k-1 {
			continue
		}
		aspect := cols * 16 / 9 / float64(k) / (rows / float64(r))
		if off := math.Abs(math.Log(aspect / 1.6)); off < bestOff {
			best, bestOff = k, off
		}
	}
	return best
}

// minBody is the least room the panel's view needs, in dp: room for
// four clocks in one cell, in the narrowest column.
var minBody = sdk.BodySize{Width: 168, Height: 114}
