// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ics is a feed holding events, each given as its property lines.
func ics(events ...string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nX-WR-CALNAME:Work\r\n")
	for _, e := range events {
		b.WriteString("BEGIN:VEVENT\r\n")
		for line := range strings.Lines(strings.TrimSpace(e)) {
			b.WriteString(strings.TrimSpace(line) + "\r\n")
		}
		b.WriteString("END:VEVENT\r\n")
	}
	b.WriteString("END:VCALENDAR\r\n")
	return b.String()
}

func mustParse(t *testing.T, data string) feed {
	t.Helper()
	f, err := parse(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// summary lists events as "start–end title", in loc.
func summary(events []event, loc *time.Location) []string {
	var out []string
	for _, e := range events {
		out = append(out, fmt.Sprintf("%s–%s %s", e.start.In(loc).Format("01-02 15:04"), e.end.In(loc).Format("01-02 15:04"), e.title))
	}
	return out
}

func TestOccurrences(t *testing.T) {
	loc := berlin(t)
	f := mustParse(t, ics(`
		UID:zoned
		SUMMARY:Zoned
		DTSTART;TZID=America/New_York:20261014T080000
		DTEND;TZID=America/New_York:20261014T090000`, `
		UID:utc
		SUMMARY:UTC
		DTSTART:20261014T080000Z
		DURATION:PT45M`, `
		UID:floating
		SUMMARY:Floating
		DTSTART:20261014T100000
		DTEND:20261014T103000`, `
		UID:allday
		SUMMARY:All day
		DTSTART;VALUE=DATE:20261015
		DTEND;VALUE=DATE:20261017`, `
		UID:inclusive
		SUMMARY:Inclusive
		DTSTART;VALUE=DATE:20261016
		DTEND;VALUE=DATE:20261016`, `
		UID:windows
		SUMMARY:Windows zone
		DTSTART;TZID=Eastern Standard Time:20261014T120000
		DTEND;TZID=Eastern Standard Time:20261014T130000`, `
		UID:unknown
		SUMMARY:Unknown zone
		DTSTART;TZID=Mars Standard Time:20261014T120000
		DTEND;TZID=Mars Standard Time:20261014T130000`, `
		UID:prefixed
		SUMMARY:Prefixed zone
		DTSTART;TZID=/mozilla.org/20050126_1/Asia/Tokyo:20261014T230000
		DTEND;TZID=/mozilla.org/20050126_1/Asia/Tokyo:20261014T233000`, `
		UID:cancelled
		SUMMARY:Cancelled
		STATUS:CANCELLED
		DTSTART:20261014T110000Z`, `
		UID:past
		SUMMARY:Past
		DTSTART:20261013T110000Z
		DTEND:20261013T120000Z`, `
		UID:later
		SUMMARY:Later
		DTSTART:20261020T110000Z`, `
		UID:nostart
		SUMMARY:No start`))
	if f.name != "Work" {
		t.Errorf("name %q, want Work", f.name)
	}
	from := time.Date(2026, 10, 14, 0, 0, 0, 0, loc)
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 3), loc), loc)
	want := []string{
		"10-14 10:00–10-14 10:30 Floating",
		"10-14 10:00–10-14 10:45 UTC",
		"10-14 12:00–10-14 13:00 Unknown zone",
		"10-14 14:00–10-14 15:00 Zoned",
		"10-14 16:00–10-14 16:30 Prefixed zone",
		"10-14 18:00–10-14 19:00 Windows zone",
		"10-15 00:00–10-17 00:00 All day",
		"10-16 00:00–10-17 00:00 Inclusive",
	}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

func TestRecurrence(t *testing.T) {
	loc := berlin(t)
	f := mustParse(t, ics(`
		UID:standup
		SUMMARY:Standup
		DTSTART;TZID=Europe/Berlin:20261019T090000
		DTEND;TZID=Europe/Berlin:20261019T091500
		RRULE:FREQ=DAILY;COUNT=8
		EXDATE;TZID=Europe/Berlin:20261020T090000,20261021T090000`, `
		UID:standup
		SUMMARY:Standup (moved)
		RECURRENCE-ID;TZID=Europe/Berlin:20261022T090000
		DTSTART;TZID=Europe/Berlin:20261022T140000
		DTEND;TZID=Europe/Berlin:20261022T141500`, `
		UID:standup
		SUMMARY:Standup
		STATUS:CANCELLED
		RECURRENCE-ID:20261023T070000Z
		DTSTART:20261023T070000Z`, `
		UID:birthday
		SUMMARY:Birthday
		DTSTART;VALUE=DATE:19801025
		RRULE:FREQ=YEARLY`, `
		UID:busy
		SUMMARY:Every minute
		DTSTART:20261019T000000Z
		RRULE:FREQ=MINUTELY`, `
		UID:extra
		SUMMARY:Extra
		DTSTART:20261019T150000Z
		DTEND:20261019T160000Z
		RDATE:20261024T150000Z`))
	from := time.Date(2026, 10, 19, 0, 0, 0, 0, loc)
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 9), loc), loc)
	want := []string{
		// A rule more frequent than hourly is ignored, leaving the start.
		"10-19 02:00–10-19 02:00 Every minute",
		"10-19 09:00–10-19 09:15 Standup",
		"10-19 17:00–10-19 18:00 Extra",
		"10-22 14:00–10-22 14:15 Standup (moved)",
		"10-24 09:00–10-24 09:15 Standup",
		"10-24 17:00–10-24 18:00 Extra",
		"10-25 00:00–10-26 00:00 Birthday",
		// The clocks went back on the 25th; the standup stays at 9.
		"10-25 09:00–10-25 09:15 Standup",
		"10-26 09:00–10-26 09:15 Standup",
	}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

func TestText(t *testing.T) {
	f := mustParse(t, ics(`
		UID:a
		SUMMARY:Lunch\, then a walk, outside
		LOCATION:Room 4\; floor 2
		DESCRIPTION:First line\nSecond \\ line
		DTSTART:20261014T110000Z`))
	events := f.events(t.Context(), time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), time.UTC)
	if len(events) != 1 {
		t.Fatalf("%d events, want 1", len(events))
	}
	e := events[0]
	if e.title != "Lunch, then a walk, outside" || e.location != "Room 4; floor 2" || e.notes != "First line\nSecond \\ line" {
		t.Errorf("title %q, location %q, notes %q", e.title, e.location, e.notes)
	}
	if e.key != "a@20261014T110000Z" {
		t.Errorf("key %q", e.key)
	}
}

func TestParseErrors(t *testing.T) {
	for _, data := range []string{
		"",
		"<html>Sign in</html>",
		"BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART;TZID=\"a\"b:20261014T110000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	} {
		if _, err := parse(strings.NewReader(data)); err == nil || !strings.HasPrefix(err.Error(), "not an iCalendar feed") {
			t.Errorf("parse(%q) = %v", data, err)
		}
	}
	// A feed cut off is not an empty one.
	if _, err := parse(strings.NewReader("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\n")); !errors.Is(err, errNotICS) {
		t.Errorf("cut off: %v", err)
	}
}

// A UTC time may be written in lowercase, and an EXDATE given as a
// time excludes the day of an all-day series.
func TestLenientTimes(t *testing.T) {
	loc := berlin(t)
	f := mustParse(t, ics(`
		UID:z
		SUMMARY:Lower z
		DTSTART:20261028t080000z`, `
		UID:d
		SUMMARY:Days
		DTSTART;VALUE=DATE:20261028
		RRULE:FREQ=DAILY;COUNT=3
		EXDATE:20261029T000000Z`))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, loc)
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 3), loc), loc)
	want := []string{"10-28 00:00–10-29 00:00 Days", "10-28 09:00–10-28 09:00 Lower z", "10-30 00:00–10-31 00:00 Days"}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

// UNTIL without a zone is in the start's zone, and a date excluded from
// a timed series removes that day's occurrence.
func TestUntilAndDateExclusions(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	f := mustParse(t, ics(`
		UID:weekly
		SUMMARY:Weekly
		DTSTART;VALUE=DATE:20261102
		RRULE:FREQ=WEEKLY;UNTIL=20261116`, `
		UID:daily
		SUMMARY:Daily
		DTSTART:20261102T090000
		DTEND:20261102T093000
		RRULE:FREQ=DAILY;UNTIL=20261105T090000
		EXDATE;VALUE=DATE:20261103`))
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 30), loc), loc)
	want := []string{
		"11-02 00:00–11-03 00:00 Weekly",
		"11-02 09:00–11-02 09:30 Daily",
		"11-04 09:00–11-04 09:30 Daily",
		"11-05 09:00–11-05 09:30 Daily",
		"11-09 00:00–11-10 00:00 Weekly",
		"11-16 00:00–11-17 00:00 Weekly",
	}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

// DTSTART is the first occurrence and counts toward COUNT even when the
// rule does not match it, and an occurrence moved onto another's time
// keeps its own key.
func TestCountAndMovedKeys(t *testing.T) {
	f := mustParse(t, ics(`
		UID:fri
		SUMMARY:Friday
		DTSTART:20261014T090000Z
		RRULE:FREQ=WEEKLY;BYDAY=FR;COUNT=2`, `
		UID:daily
		SUMMARY:Daily
		DTSTART:20261019T100000Z
		RRULE:FREQ=DAILY;COUNT=3`, `
		UID:daily
		SUMMARY:Daily (moved)
		RECURRENCE-ID:20261020T100000Z
		DTSTART:20261021T100000Z`))
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	events := f.events(t.Context(), from, from.AddDate(0, 1, 0), time.UTC)
	var got []string
	for _, e := range events {
		got = append(got, e.title+" "+e.key)
	}
	want := []string{
		"Friday fri@20261014T090000Z",
		"Friday fri@20261016T090000Z",
		"Daily daily@20261019T100000Z",
		"Daily daily@20261021T100000Z",
		"Daily (moved) daily@20261020T100000Z",
	}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

func TestLongText(t *testing.T) {
	long := strings.Repeat("x", 1000)
	f := mustParse(t, ics("UID:a\nSUMMARY:"+long+"\nLOCATION:"+long+"\nDESCRIPTION:"+strings.Repeat("y", 1<<20)+"\nDTSTART:20261014T090000Z\nRRULE:FREQ=HOURLY"))
	events := f.events(t.Context(), time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), time.UTC)
	if len(events) != 15 {
		t.Fatalf("%d events, want 15", len(events))
	}
	for _, e := range events {
		if len([]rune(e.title)) > maxText+1 || len([]rune(e.location)) > maxText+1 || len([]rune(e.notes)) > maxNotes+1 {
			t.Fatalf("title %d runes, location %d runes, notes %d runes", len([]rune(e.title)), len([]rune(e.location)), len([]rune(e.notes)))
		}
	}
}

// Rule parts are read in any case, empty parts and extensions are
// skipped, and an event whose rule the panel does not follow keeps its
// start and RDATEs.
func TestRuleSpelling(t *testing.T) {
	f := mustParse(t, ics(`
		UID:a
		SUMMARY:Lower
		DTSTART:20261021T100000Z
		RRULE:freq=weekly;byday=we;X-FOO=1;`, `
		UID:b
		SUMMARY:Hebrew
		DTSTART:20261028T110000Z
		RRULE:RSCALE=HEBREW;FREQ=YEARLY;SKIP=FORWARD
		RDATE:20261029T110000Z`, `
		UID:c
		SUMMARY:SetPos
		DTSTART:20261028T120000Z
		RRULE:FREQ=DAILY;BYSETPOS=1`))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 8), time.UTC), time.UTC)
	want := []string{
		"10-28 10:00–10-28 10:00 Lower", "10-28 11:00–10-28 11:00 Hebrew", "10-28 12:00–10-28 12:00 SetPos",
		"10-29 11:00–10-29 11:00 Hebrew", "11-04 10:00–11-04 10:00 Lower",
	}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

// An hourly rule whose BYHOUR its interval never reaches is ignored,
// leaving the start; the recurrence library would loop forever looking
// for one.
func TestUnreachableHours(t *testing.T) {
	f := mustParse(t, ics(`
		UID:never
		SUMMARY:Never
		DTSTART:20261028T020000Z
		RRULE:FREQ=HOURLY;INTERVAL=2;BYHOUR=1`, `
		UID:odd
		SUMMARY:Odd
		DTSTART:20261028T010000Z
		RRULE:FREQ=HOURLY;INTERVAL=10;BYHOUR=3;COUNT=1`))
	done := make(chan []event, 1)
	go func() {
		from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
		done <- f.events(t.Context(), from, from.AddDate(0, 0, 9), time.UTC)
	}()
	select {
	case events := <-done:
		got := summary(events, time.UTC)
		want := []string{"10-28 01:00–10-28 01:00 Odd", "10-28 02:00–10-28 02:00 Never"}
		if !slices.Equal(got, want) {
			t.Errorf("occurrences %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("occurrences did not return")
	}
}

// An RDATE period adds an occurrence with its own length, and a long UID
// makes a short key.
func TestPeriodsAndLongUIDs(t *testing.T) {
	uid := strings.Repeat("u", 5000)
	f := mustParse(t, ics(`
		UID:`+uid+`
		SUMMARY:Talk
		DTSTART:20261028T090000Z
		DTEND:20261028T100000Z
		RDATE;VALUE=PERIOD:20261101T090000Z/20261101T110000Z,20261102T090000Z/PT30M`))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	events := f.events(t.Context(), from, from.AddDate(0, 0, 9), time.UTC)
	got := summary(events, time.UTC)
	want := []string{"10-28 09:00–10-28 10:00 Talk", "11-01 09:00–11-01 11:00 Talk", "11-02 09:00–11-02 09:30 Talk"}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
	for _, e := range events {
		if len(e.key) > 100 {
			t.Errorf("key of %d bytes", len(e.key))
		}
	}
}

// An excluded date is a day in the series' own zone, and an UNTIL date
// on a series with start times includes that day.
func TestDatesInSeriesZone(t *testing.T) {
	f := mustParse(t, ics(`
		UID:la
		SUMMARY:LA
		DTSTART;TZID=America/Los_Angeles:20261026T230000
		RRULE:FREQ=DAILY;COUNT=5
		EXDATE;VALUE=DATE:20261029`, `
		UID:until
		SUMMARY:Until
		DTSTART;TZID=Europe/Berlin:20261028T100000
		RRULE:FREQ=DAILY;UNTIL=20261030`))
	from := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 9), berlin(t)), time.UTC)
	want := []string{
		"10-27 06:00–10-27 06:00 LA",
		"10-28 06:00–10-28 06:00 LA",
		"10-28 09:00–10-28 09:00 Until",
		"10-29 06:00–10-29 06:00 LA",
		"10-29 09:00–10-29 09:00 Until",
		"10-30 09:00–10-30 09:00 Until",
		"10-31 06:00–10-31 06:00 LA",
	}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

// Events without a UID that differ are kept apart.
func TestNoUID(t *testing.T) {
	f := mustParse(t, ics(`
		SUMMARY:Standup
		DTSTART:20261029T090000Z`, `
		SUMMARY:Standup
		LOCATION:Room B
		DTSTART:20261029T090000Z`))
	from := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	events := f.events(t.Context(), from, from.AddDate(0, 0, 1), time.UTC)
	if len(events) != 2 || events[0].key == events[1].key {
		t.Errorf("events %+v", events)
	}
}

// A recurring event that began long ago still shows: the limit on
// occurrences counts from the time shown, not from the event's start.
func TestOldSeries(t *testing.T) {
	f := mustParse(t, ics(`
		UID:standup
		SUMMARY:Standup
		DTSTART:20100104T090000Z
		DTEND:20100104T091500Z
		RRULE:FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR`, `
		UID:hourly
		SUMMARY:Hourly
		DTSTART:19700101T000000Z
		RRULE:FREQ=HOURLY;INTERVAL=6`))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 1), time.UTC), time.UTC)
	want := []string{
		// The one at midnight is over: it lasts no time.
		"10-28 06:00–10-28 06:00 Hourly",
		"10-28 09:00–10-28 09:15 Standup",
		"10-28 12:00–10-28 12:00 Hourly",
		"10-28 18:00–10-28 18:00 Hourly",
	}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

// A series from centuries ago still repeats, as birthdays of unknown
// year do from 1604 in some feeds.
func TestAncientSeries(t *testing.T) {
	f := mustParse(t, ics(`
		UID:birthday
		SUMMARY:Birthday
		DTSTART;VALUE=DATE:16041028
		RRULE:FREQ=YEARLY`, `
		UID:daily
		SUMMARY:Daily
		DTSTART:17000101T090000Z
		RRULE:FREQ=DAILY`))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 1), time.UTC), time.UTC)
	want := []string{
		"10-28 00:00–10-29 00:00 Birthday",
		"10-28 09:00–10-28 09:00 Daily",
	}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

// A moved occurrence with a rule of its own, as a change "and all
// following" writes it, is that one occurrence; a UID-less event keeps
// its key when only its timestamps change; and a rule with several
// minutes an hour is left out.
func TestOverrideRuleKeysAndFrequentRules(t *testing.T) {
	series := `
		UID:a
		SUMMARY:Old
		DTSTART:20261027T100000Z
		RRULE:FREQ=DAILY;COUNT=3`
	override := `
		UID:a
		SUMMARY:New
		RECURRENCE-ID:20261028T100000Z
		DTSTART:20261028T100000Z
		RRULE:FREQ=DAILY`
	frequent := `
		UID:f
		SUMMARY:Frequent
		DTSTART:20261027T080000Z
		RRULE:FREQ=DAILY;BYHOUR=8,9,10,11,12,13,14;BYMINUTE=0,15,30,45`
	from := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	f := mustParse(t, ics(series, override, frequent))
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 5), time.UTC), time.UTC)
	want := []string{"10-27 08:00–10-27 08:00 Frequent", "10-27 10:00–10-27 10:00 Old", "10-28 10:00–10-28 10:00 New", "10-29 10:00–10-29 10:00 Old"}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
	key := func(stamp string) string {
		f := mustParse(t, ics("SUMMARY:Lunch\nDTSTAMP:"+stamp+"\nDTSTART:20261027T120000Z"))
		return f.events(t.Context(), from, from.AddDate(0, 0, 1), time.UTC)[0].key
	}
	if a, b := key("20261027T080000Z"), key("20261027T090000Z"); a != b {
		t.Errorf("keys %q and %q differ", a, b)
	}
}

// Once ctx ends, recurring events are no longer followed.
func TestOccurrencesCancelled(t *testing.T) {
	f := mustParse(t, ics("UID:a\nSUMMARY:A\nDTSTART:20261027T100000Z\nRRULE:FREQ=DAILY"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	from := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	if events := f.events(ctx, from, from.AddDate(0, 0, 3), time.UTC); len(events) != 0 {
		t.Errorf("%d events after cancelling", len(events))
	}
}

// A duration in days or weeks is in calendar days, which a clock change
// does not shorten.
func TestNominalDuration(t *testing.T) {
	loc := berlin(t)
	f := mustParse(t, ics(`
		UID:a
		SUMMARY:Day
		DTSTART;TZID=Europe/Berlin:20261024T120000
		DURATION:P1DT2H`, `
		UID:b
		SUMMARY:Week
		DTSTART;TZID=Europe/Berlin:20261020T120000
		DURATION:P1W`))
	from := time.Date(2026, 10, 24, 0, 0, 0, 0, loc)
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 1), loc), loc)
	want := []string{"10-20 12:00–10-27 12:00 Week", "10-24 12:00–10-25 14:00 Day"}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

// events returns f's occurrences, whether or not all were followed.
func (f feed) events(ctx context.Context, from, to time.Time, loc *time.Location) []event {
	events, _ := f.occurrences(ctx, from, to, loc)
	return events
}

// Rules that can never match are ignored at once when their shape
// shows it, leaving the start, and the rest are given up on when the
// budget runs out.
func TestHopelessRules(t *testing.T) {
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	f := mustParse(t, ics("UID:s\nSUMMARY:SetPos\nDTSTART:20261028T130000Z\nRRULE:FREQ=HOURLY;BYSETPOS=2"))
	start := time.Now()
	if events, complete := f.occurrences(t.Context(), from, from.AddDate(0, 0, 1), time.UTC); len(events) != 1 || !complete || time.Since(start) > time.Second {
		t.Errorf("%d events, complete %v, in %v", len(events), complete, time.Since(start))
	}
	var lines []string
	for i := range 20 {
		lines = append(lines, fmt.Sprintf("UID:n%d\nSUMMARY:Never\nDTSTART:20261028T130000Z\nRRULE:FREQ=HOURLY;BYMONTH=2;BYMONTHDAY=31", i))
	}
	lines = append(lines, "UID:ok\nSUMMARY:Lunch\nDTSTART:20261028T120000Z\nDTEND:20261028T130000Z")
	f = mustParse(t, ics(lines...))
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start = time.Now()
	events, complete := f.occurrences(ctx, from, from.AddDate(0, 0, 1), time.UTC)
	if complete || len(events) != 1 || events[0].title != "Lunch" || time.Since(start) > time.Second {
		t.Errorf("%d events, complete %v, in %v", len(events), complete, time.Since(start))
	}
}

// An event without a UID, keyed by what it says, has a UTF-8 key even
// when the feed is not UTF-8: the key is an action ID.
func TestNoUIDKeyUTF8(t *testing.T) {
	f := mustParse(t, ics("SUMMARY:\xe9t\xe9\nDTSTART:20261028T100000Z"))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	events := f.events(t.Context(), from, from.AddDate(0, 0, 1), time.UTC)
	if len(events) != 1 || !utf8.ValidString(events[0].key) {
		t.Errorf("events %+v", events)
	}
}

// A rule with several times a day is followed unless it gives more of
// them than there are hours.
func TestTimesADay(t *testing.T) {
	f := mustParse(t, ics(`
		UID:a
		SUMMARY:Twice
		DTSTART:20261028T090000Z
		RRULE:FREQ=DAILY;BYHOUR=9;BYMINUTE=0,30;COUNT=3`))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 3), time.UTC), time.UTC)
	want := []string{"10-28 09:00–10-28 09:00 Twice", "10-28 09:30–10-28 09:30 Twice", "10-29 09:00–10-29 09:00 Twice"}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences\n got %q\nwant %q", got, want)
	}
}

// A series that ended before from is not followed, but one whose last
// occurrence is still under way is.
func TestEndedSeries(t *testing.T) {
	f := mustParse(t, ics(`
		UID:old
		SUMMARY:Old
		DTSTART:19000101T090000Z
		RRULE:FREQ=HOURLY;UNTIL=20200101T000000Z`, `
		UID:trip
		SUMMARY:Trip
		DTSTART;VALUE=DATE:20261001
		DTEND;VALUE=DATE:20261030
		RRULE:FREQ=MONTHLY;UNTIL=20261001`))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	start := time.Now()
	got := summary(f.events(t.Context(), from, from.AddDate(0, 0, 1), time.UTC), time.UTC)
	if want := []string{"10-01 00:00–10-30 00:00 Trip"}; !slices.Equal(got, want) || time.Since(start) > 100*time.Millisecond {
		t.Errorf("occurrences %q in %v, want %q", got, time.Since(start), want)
	}
}

// An EXDATE may exclude the start of an event without a rule.
func TestExcludedStart(t *testing.T) {
	f := mustParse(t, ics("UID:x\nSUMMARY:X\nDTSTART:20261028T100000Z\nDTEND:20261028T110000Z\nEXDATE:20261028T100000Z"))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	if events := f.events(t.Context(), from, from.AddDate(0, 0, 1), time.UTC); len(events) != 0 {
		t.Errorf("occurrences %q", summary(events, time.UTC))
	}
}

// A time the clocks go back past is the first of the two, and one they
// skip is read with the offset before the gap (RFC 5545 3.3.5, erratum
// 4271), in a series as in a single event, and an EXDATE excludes it.
func TestClockChanges(t *testing.T) {
	loc := berlin(t)
	f := mustParse(t, ics(`
		UID:a
		SUMMARY:Single
		DTSTART;TZID=Europe/Berlin:20261025T023000
		DURATION:PT10M`, `
		UID:b
		SUMMARY:Daily
		DTSTART;TZID=Europe/Berlin:20261024T023000
		DURATION:PT10M
		RRULE:FREQ=DAILY;COUNT=4
		EXDATE;TZID=Europe/Berlin:20261026T023000`, `
		UID:c
		SUMMARY:Spring
		DTSTART;TZID=Europe/Berlin:20260328T023000
		DURATION:PT10M
		RRULE:FREQ=DAILY;COUNT=3`))
	var got []string
	for _, from := range []time.Time{time.Date(2026, 10, 24, 0, 0, 0, 0, loc), time.Date(2026, 3, 28, 0, 0, 0, 0, loc)} {
		for _, e := range f.events(t.Context(), from, from.AddDate(0, 0, 4), loc) {
			got = append(got, e.start.UTC().Format("01-02 15:04 ")+e.title)
		}
	}
	want := []string{
		"10-24 00:30 Daily", "10-25 00:30 Daily", "10-25 00:30 Single", "10-27 01:30 Daily",
		"03-28 01:30 Spring", "03-29 01:30 Spring", "03-30 00:30 Spring",
	}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences (UTC)\n got %q\nwant %q", got, want)
	}
	// Go reads these gaps with the offset after them.
	f = mustParse(t, ics(`
		UID:ny
		SUMMARY:New York
		DTSTART;TZID=America/New_York:20260307T023000
		DURATION:PT10M
		RRULE:FREQ=DAILY;COUNT=3`, `
		UID:sp
		SUMMARY:São Paulo
		DTSTART;TZID=America/Sao_Paulo:20181104T003000
		DURATION:PT10M`))
	got = nil
	for _, from := range []time.Time{time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC), time.Date(2018, 11, 3, 0, 0, 0, 0, time.UTC)} {
		for _, e := range f.events(t.Context(), from, from.AddDate(0, 0, 3), time.UTC) {
			got = append(got, e.start.UTC().Format("01-02 15:04 ")+e.title)
		}
	}
	want = []string{"03-07 07:30 New York", "03-08 07:30 New York", "03-09 06:30 New York", "11-04 03:30 São Paulo"}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences (UTC)\n got %q\nwant %q", got, want)
	}
	// A start the clocks skip still sets the time of day, and an RDATE
	// in UTC keeps its instant when the clock shows its time twice.
	f = mustParse(t, ics(`
		UID:g
		SUMMARY:Gap
		DTSTART;TZID=America/New_York:20260308T023000
		DURATION:PT10M
		RRULE:FREQ=DAILY;COUNT=3`, `
		UID:r
		SUMMARY:RDATE
		DTSTART;TZID=America/New_York:20261031T013000
		DURATION:PT10M
		RDATE:20261101T053000Z,20261101T063000Z`))
	got = nil
	for _, from := range []time.Time{time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)} {
		for _, e := range f.events(t.Context(), from, from.AddDate(0, 0, 3), time.UTC) {
			got = append(got, e.start.UTC().Format("01-02 15:04")+e.end.UTC().Format("–15:04 ")+e.title)
		}
	}
	want = []string{
		"03-08 07:30–07:40 Gap", "03-09 06:30–06:40 Gap", "03-10 06:30–06:40 Gap",
		"10-31 05:30–05:40 RDATE", "11-01 05:30–05:40 RDATE", "11-01 06:30–06:40 RDATE",
	}
	if !slices.Equal(got, want) {
		t.Errorf("occurrences (UTC)\n got %q\nwant %q", got, want)
	}
	// A day whose midnight the clocks skip starts when it begins, after
	// the gap, and ends at the next midnight.
	santiago, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatal(err)
	}
	f = mustParse(t, ics("UID:d\nSUMMARY:Day\nDTSTART;VALUE=DATE:20260906"))
	from := time.Date(2026, 9, 5, 12, 0, 0, 0, santiago)
	got = summary(f.events(t.Context(), from, from.AddDate(0, 0, 3), santiago), santiago)
	if want := []string{"09-06 01:00–09-07 00:00 Day"}; !slices.Equal(got, want) {
		t.Errorf("occurrences %q, want %q", got, want)
	}
}

// A line that is not a property continues the one before it, as when
// a feed wraps a line without the space that marks it.
func TestMendedLines(t *testing.T) {
	f := mustParse(t, ics(`
		UID:a
		SUMMARY:Review
		DESCRIPTION:Bring the
		draft, please
		DTSTART:20261028T100000Z`))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	events := f.events(t.Context(), from, from.AddDate(0, 0, 1), time.UTC)
	if len(events) != 1 || events[0].notes != "Bring thedraft, please" {
		t.Errorf("events %+v", events)
	}
	// A feed the decoder reads as it is keeps its lines, even one whose
	// name is not a property's.
	f = mustParse(t, ics(`
		UID:b
		SUMMARY:B
		DTSTART:20261028T100000Z
		X_FOO:bar`))
	if events := f.events(t.Context(), from, from.AddDate(0, 0, 1), time.UTC); len(events) != 1 {
		t.Errorf("events %+v", events)
	}
}

// Events without a UID that differ only in a parameter are told apart.
func TestNoUIDParams(t *testing.T) {
	f := mustParse(t, ics(
		"SUMMARY:A\nDTSTART:20261028T090000Z\nDTEND;TZID=Europe/Berlin:20261028T120000",
		"SUMMARY:A\nDTSTART:20261028T090000Z\nDTEND;TZID=UTC:20261028T120000"))
	from := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	events := f.events(t.Context(), from, from.AddDate(0, 0, 1), time.UTC)
	if len(events) != 2 || events[0].key == events[1].key {
		t.Errorf("events %+v", events)
	}
}

// An UNTIL in UTC bounds the series by its instant, even in the hour
// the clocks show twice.
func TestUntilInRepeatedHour(t *testing.T) {
	loc := berlin(t)
	f := mustParse(t, ics(`
		UID:u
		SUMMARY:U
		DTSTART;TZID=Europe/Berlin:20261024T023000
		DURATION:PT10M
		RRULE:FREQ=DAILY;UNTIL=20261025T011500Z`))
	from := time.Date(2026, 10, 24, 0, 0, 0, 0, loc)
	var got []string
	for _, e := range f.events(t.Context(), from, from.AddDate(0, 0, 3), loc) {
		got = append(got, e.start.UTC().Format("01-02 15:04"))
	}
	if want := []string{"10-24 00:30", "10-25 00:30"}; !slices.Equal(got, want) {
		t.Errorf("occurrences (UTC) %q, want %q", got, want)
	}
}
