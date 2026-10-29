// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/joncbenderkh/tessera/sdk"
)

// hideAction is the action ID of the item that hides an event's
// details. Event keys hold an @, so none is the same.
const hideAction = "hide"

const (
	// maxItems is how many events the list holds.
	maxItems = 100
	// maxNotes is how many characters of an event's notes the panel
	// keeps.
	maxNotes = 500
)

// agenda is what the panel shows besides the date: its title, the
// events, the sources that failed to refresh, and whether every source
// has been read once, for good or ill.
type agenda struct {
	title  string
	events []event
	failed []string
	loaded bool
}

// view shows the agenda at now, in now's time zone, with the details
// of the event whose key is selected, if it is in the agenda.
func view(now time.Time, a agenda, selected string) sdk.View {
	loc := now.Location()
	body := sdk.Column(sdk.Row(
		sdk.Weighted(1, sdk.Column(
			sdk.Text(strconv.Itoa(now.Day())).Size(sdk.SizeDisplay),
			sdk.Text(strings.ToUpper(now.Format("Monday · January"))).Size(sdk.SizeSmall).Tone(sdk.ToneMuted),
		)),
		sdk.Text(listed(len(a.events))).Size(sdk.SizeSmall).Tone(sdk.ToneMuted).Monospace(),
	)).Gap(sdk.SpacingMedium)
	for _, f := range a.failed {
		body.Add(sdk.Text(f).Size(sdk.SizeSmall).Tone(sdk.ToneWarn))
	}
	list := sdk.List().ID("events").Empty("No more events today")
	// Until every source has been read, no events may only mean that
	// the one holding them has not answered yet.
	if !a.loaded && len(a.events) == 0 {
		list.Loading()
	}
	var details sdk.Widget
	for i, e := range a.events {
		if i < maxItems {
			list.Add(item(e, now))
		}
		if e.key == selected && selected != "" {
			details = eventDetails(e, loc)
		}
	}
	body.Add(sdk.Weighted(1, list))
	if details != nil {
		body.Add(details)
	}
	return sdk.View{Title: a.title, Body: body}
}

// item is e's entry in the list at now.
func item(e event, now time.Time) *sdk.ListItemSpec {
	loc := now.Location()
	start, end := e.start.In(loc), e.end.In(loc)
	var lead string
	var detail []string
	switch {
	case e.allDay:
		lead = "All day"
		if days := dayNumber(end) - dayNumber(start); days > 1 {
			detail = append(detail, count(days, "day"))
		}
	case !start.After(now):
		lead = "Now"
		detail = append(detail, "until "+clock(end, now))
	default:
		lead = start.Format("15:04")
		if end.After(start) {
			detail = append(detail, length(end.Sub(start)))
		}
	}
	if e.location != "" {
		detail = append(detail, e.location)
	}
	return sdk.Item(e.title).Leading(lead).Detail(strings.Join(nonEmpty(detail), " · ")).Action(e.key)
}

// eventDetails shows all the panel knows of e.
func eventDetails(e event, loc *time.Location) sdk.Widget {
	col := sdk.Column(
		sdk.Text(e.title),
		sdk.Text(when(e, loc)).Size(sdk.SizeSmall).Tone(sdk.ToneMuted),
	).Gap(sdk.SpacingSmall)
	if e.location != "" {
		col.Add(sdk.Text(e.location).Size(sdk.SizeSmall))
	}
	if e.notes != "" {
		col.Add(sdk.Text(e.notes).Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
	}
	if e.source != "" {
		col.Add(sdk.Text(e.source).Size(sdk.SizeSmall).Tone(sdk.ToneMuted).Monospace())
	}
	// An item rather than activating the event again: the host keeps the
	// item last activated highlighted, which this one takes away with it.
	return col.Add(sdk.List(sdk.Item("Hide details").Action(hideAction)))
}

// when says when e takes place, in loc.
func when(e event, loc *time.Location) string {
	start, end := e.start.In(loc), e.end.In(loc)
	const day = "Monday, January 2"
	switch {
	case e.allDay:
		last := end.AddDate(0, 0, -1)
		if !last.After(start) {
			return start.Format(day) + " · All day"
		}
		return start.Format("January 2") + " – " + last.Format("January 2")
	case !end.After(start):
		return start.Format(day) + " · " + start.Format("15:04")
	case sameDay(start, end):
		return start.Format(day) + " · " + start.Format("15:04") + "–" + end.Format("15:04")
	default:
		return start.Format("Jan 2 15:04") + " – " + end.Format("Jan 2 15:04")
	}
}

// clock is t's time of day, with its date if that is not now's.
func clock(t, now time.Time) string {
	if sameDay(t, now) {
		return t.Format("15:04")
	}
	return t.Format("Mon 15:04")
}

// length says how long d is, in minutes up to two hours.
func length(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	switch {
	case m < 120:
		return strconv.Itoa(m) + " min"
	case m%60 == 0:
		return strconv.Itoa(m/60) + " h"
	default:
		return fmt.Sprintf("%d h %d min", m/60, m%60)
	}
}

// listed says how many events there are, and how many the list holds
// if not all.
func listed(n int) string {
	if n > maxItems {
		return strconv.Itoa(maxItems) + " of " + count(n, "event")
	}
	return count(n, "event")
}

// count is n of noun, in the plural unless n is 1.
func count(n int, noun string) string {
	if n != 1 {
		noun += "s"
	}
	return strconv.Itoa(n) + " " + noun
}

func sameDay(a, b time.Time) bool { return dayNumber(a) == dayNumber(b) }

func nonEmpty(parts []string) []string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
