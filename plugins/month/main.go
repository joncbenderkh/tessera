// SPDX-License-Identifier: GPL-3.0-or-later

// Command month is the month calendar panel: the current month as a
// grid of weeks, with today highlighted.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/plugins/internal/zones"
	"github.com/joncbenderkh/tessera/sdk"
)

const schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "first_weekday": {
      "type": "string",
      "description": "The day each week starts on: monday or sunday, in any case; empty for monday.",
      "pattern": "^ *(([Mm][Oo][Nn]|[Ss][Uu][Nn])[Dd][Aa][Yy])? *$",
      "default": "monday"
    },
    "timezone": {
      "type": "string",
      "description": "An IANA time zone, such as Europe/Berlin, or Local for the system's.",
      "default": "Local"
    }
  },
  "additionalProperties": false
}`

type settings struct {
	FirstWeekday string `json:"first_weekday"`
	Timezone     string `json:"timezone"`
}

// month is the panel's state. Configure and the ticker run
// concurrently, so mu guards it. The ticker starts on the first
// Configure, so no view shows a week the settings do not ask for.
type month struct {
	views chan<- sdk.View
	start sync.Once

	mu    sync.Mutex
	loc   *time.Location // nil for the system's time zone
	first time.Weekday
	shown string // the date of the latest view, as 2006-01-02
}

func main() {
	views := make(chan sdk.View)
	m := &month{views: views}
	sdk.Serve(sdk.Plugin{
		Name:         "month",
		Version:      tessera.Version,
		Description:  "The current month, with today highlighted.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 1, Rows: 2},
		Views:        views,
		Configure:    m.configure,
	})
}

func (m *month) configure(_ context.Context, cfg sdk.Config) error {
	var s settings
	if err := cfg.Decode(&s); err != nil {
		return err
	}
	first, err := weekdayOf(s.FirstWeekday)
	if err != nil {
		return sdk.InvalidSettings("%v", err)
	}
	loc, zone, err := zones.Load(s.Timezone)
	if err != nil {
		return sdk.InvalidSettings("%v", err)
	}
	if zone == "Local" {
		loc = nil
	}
	m.mu.Lock()
	m.loc, m.first = loc, first
	m.show(time.Now().In(m.location()))
	m.mu.Unlock()
	m.start.Do(func() { go m.tick() })
	return nil
}

// weekdayOf is the first_weekday setting name, in any case and with
// spaces around it: Monday if it is unset. It trims spaces alone, not
// the other white space a JSON Schema \s and Go's disagree on, so the
// schema's pattern accepts the same names.
func weekdayOf(name string) (time.Weekday, error) {
	switch strings.ToLower(strings.Trim(name, " ")) {
	case "", "monday":
		return time.Monday, nil
	case "sunday":
		return time.Sunday, nil
	}
	return 0, fmt.Errorf("setting \"first_weekday\": want monday or sunday, not %q", name)
}

// location is the time zone the panel shows today in. m.mu is held.
func (m *month) location() *time.Location {
	if m.loc != nil {
		return m.loc
	}
	return systemZone("/etc/localtime")
}

// systemZone is the system's time zone as it is set now, in the file
// localtime, which time.Local, read once when the process starts,
// misses a change of. A zone set by the TZ variable cannot change.
func systemZone(localtime string) *time.Location {
	if _, set := os.LookupEnv("TZ"); set {
		return time.Local
	}
	data, err := os.ReadFile(localtime)
	if err != nil {
		return time.Local
	}
	loc, err := time.LoadLocationFromTZData("Local", data)
	if err != nil {
		return time.Local
	}
	return loc
}

// wake is the longest the ticker sleeps. A sleep does not count the
// time the computer is suspended, and the wall clock or the time zone
// may be changed, so one to midnight could leave the day behind.
const wake = time.Minute

// tick shows the month whenever the date changes.
func (m *month) tick() {
	for {
		m.mu.Lock()
		now := time.Now().In(m.location())
		show, sleep := due(now, m.shown)
		if show {
			m.show(now)
		}
		m.mu.Unlock()
		time.Sleep(sleep)
	}
}

// due says whether the date of now is not the one shown, and how long
// to sleep before looking again: until midnight, at most wake. Where a
// change of offset skips midnight, as in America/Santiago, time.Date
// may put it before now; the ticker then looks again after wake.
func due(now time.Time, shown string) (show bool, sleep time.Duration) {
	y, mo, d := now.Date()
	sleep = wake
	if midnight := time.Date(y, mo, d+1, 0, 0, 0, 0, now.Location()); midnight.After(now) {
		sleep = min(midnight.Sub(now), wake)
	}
	return now.Format(time.DateOnly) != shown, sleep
}

// show sends the view of now. m.mu is held.
func (m *month) show(now time.Time) {
	m.shown = now.Format(time.DateOnly)
	m.views <- view(now, m.first)
}

// view shows the month of today, its weeks starting on first, with
// today in the accent colour. Days of the months before and after are
// left blank.
func view(today time.Time, first time.Weekday) sdk.View {
	// No gaps: the cells are padded, and the weights space them.
	head := sdk.Row().Gap(sdk.SpacingNone)
	for i := range 7 {
		day := time.Weekday((int(first) + i) % 7)
		head.Add(sdk.Weighted(1, cell(day.String()[:1]).Tone(sdk.ToneMuted)))
	}
	body := sdk.Column(
		sdk.Text(today.Format("January 2006")).Size(sdk.SizeLarge),
		head,
	).Gap(sdk.SpacingExtraSmall)
	for _, week := range weeks(today.Year(), today.Month(), first) {
		row := sdk.Row().Gap(sdk.SpacingNone)
		for _, d := range week {
			switch {
			case d == 0:
				row.Add(sdk.Weighted(1, sdk.Space()))
			case d == today.Day():
				row.Add(sdk.Weighted(1, cell(fmt.Sprint(d)).Tone(sdk.ToneAccent)))
			default:
				row.Add(sdk.Weighted(1, cell(fmt.Sprint(d))))
			}
		}
		body.Add(row)
	}
	return sdk.View{Title: "Month", Body: body}
}

// cell is a day or a weekday's initial, right-aligned in two
// characters, since a text cannot be centred, so the columns line up.
func cell(s string) *sdk.TextWidget {
	return sdk.Text(fmt.Sprintf("%2s", s)).Monospace()
}

// weeks lays out the days of month mo of year y as weeks of 7 days
// starting on first: 4 to 6 of them, with 0 for each day of the month
// before or after.
func weeks(y int, mo time.Month, first time.Weekday) [][7]int {
	lead := (int(time.Date(y, mo, 1, 0, 0, 0, 0, time.UTC).Weekday()) - int(first) + 7) % 7
	days := time.Date(y, mo+1, 0, 0, 0, 0, 0, time.UTC).Day()
	ws := make([][7]int, (lead+days+6)/7)
	for d := 1; d <= days; d++ {
		ws[(lead+d-1)/7][(lead+d-1)%7] = d
	}
	return ws
}
