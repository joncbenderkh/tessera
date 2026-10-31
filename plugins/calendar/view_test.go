// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"image"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	"github.com/joncbenderkh/tessera/internal/ui"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
	"github.com/joncbenderkh/tessera/sdk"
)

// items returns the list in v's body as "leading|title|detail|action".
func items(w *tesserav1.Widget) []string {
	if l := w.GetList(); l != nil {
		var out []string
		for _, it := range l.GetItems() {
			out = append(out, strings.Join([]string{it.GetLeading(), it.GetTitle(), it.GetDetail(), it.GetActionId()}, "|"))
		}
		return out
	}
	var out []string
	for _, item := range w.GetStack().GetItems() {
		out = append(out, items(item.GetWidget())...)
	}
	return out
}

func TestView(t *testing.T) {
	loc := berlin(t)
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 10, day, hour, minute, 0, 0, loc) }
	now := at(14, 10, 15)
	a := agenda{title: "Next events", loaded: true, failed: []string{"example.com: HTTP 404 Not Found"}, events: []event{
		{key: "a", title: "Design review", start: at(14, 10, 0), end: at(14, 10, 30), location: "Room 4"},
		{key: "b", title: "Focus time", start: at(14, 11, 30), end: at(14, 13, 0), location: "Personal"},
		{key: "c", title: "Offsite", start: at(14, 0, 0), end: at(17, 0, 0), allDay: true},
		{key: "d", title: "Retro", start: at(14, 14, 0), end: at(14, 16, 30), notes: "Bring ideas", source: "Work"},
		{key: "f", title: "Call", start: at(14, 18, 0), end: at(14, 18, 0)},
		{key: "e", title: "Holiday", start: at(14, 0, 0), end: at(15, 0, 0), allDay: true},
	}}
	v := view(now, a, "d").Proto()
	if v.GetTitle() != "Next events" {
		t.Errorf("title %q", v.GetTitle())
	}
	gotItems := items(v.GetBody())
	wantItems := []string{
		"Now|Design review|until 10:30 · Room 4|a",
		"11:30|Focus time|90 min · Personal|b",
		"All day|Offsite|3 days|c",
		"14:00|Retro|2 h 30 min|d",
		"18:00|Call||f",
		"All day|Holiday||e",
		"|Hide details||hide",
	}
	if !slices.Equal(gotItems, wantItems) {
		t.Errorf("items\n got %q\nwant %q", gotItems, wantItems)
	}
	gotTexts := plugintest.Texts(v.GetBody())
	wantTexts := []string{
		"14", "WEDNESDAY · OCTOBER", "6 events",
		"example.com: HTTP 404 Not Found",
		"Retro", "Wednesday, October 14 · 14:00–16:30", "Bring ideas", "Work",
	}
	if !slices.Equal(gotTexts, wantTexts) {
		t.Errorf("texts\n got %q\nwant %q", gotTexts, wantTexts)
	}
}

func TestViewEmpty(t *testing.T) {
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		a       agenda
		loading bool
		empty   string
	}{
		{agenda{loaded: true}, false, "No more events today"},
		{agenda{}, true, "No more events today"},
		// Events from the sources read so far show while others load.
		{agenda{events: []event{{key: "a", title: "A", start: now, end: now.Add(time.Hour)}}}, false, "No more events today"},
	} {
		v := view(now, tc.a, "x").Proto()
		var list *tesserav1.Widget
		for _, item := range v.GetBody().GetStack().GetItems() {
			if item.GetWidget().GetList() != nil {
				list = item.GetWidget()
			}
		}
		if list.GetLoading() != tc.loading || list.GetList().GetEmptyText() != tc.empty || len(list.GetList().GetItems()) != len(tc.a.events) {
			t.Errorf("loaded %v: list %v", tc.a.loaded, list)
		}
	}
}

func TestWhen(t *testing.T) {
	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.UTC) }
	for _, tc := range []struct {
		e    event
		want string
	}{
		{event{start: at(14, 0), end: at(15, 0), allDay: true}, "Wednesday, October 14 · All day"},
		{event{start: at(14, 0), end: at(17, 0), allDay: true}, "October 14 – October 16"},
		{event{start: at(14, 9), end: at(14, 9)}, "Wednesday, October 14 · 09:00"},
		{event{start: at(14, 22), end: at(15, 2)}, "Oct 14 22:00 – Oct 15 02:00"},
	} {
		if got := when(tc.e, time.UTC); got != tc.want {
			t.Errorf("when(%v–%v) = %q, want %q", tc.e.start, tc.e.end, got, tc.want)
		}
	}
}

func TestLength(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                 "0 min",
		30 * time.Minute:  "30 min",
		119 * time.Minute: "119 min",
		2 * time.Hour:     "2 h",
		150 * time.Minute: "2 h 30 min",
	} {
		if got := length(d); got != want {
			t.Errorf("length(%v) = %q, want %q", d, got, want)
		}
	}
}

// The header says how many of the events the list holds.
func TestViewTooMany(t *testing.T) {
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	a := agenda{title: "T", loaded: true}
	for i := range maxItems + 5 {
		a.events = append(a.events, event{key: strconv.Itoa(i), title: "E", start: now.Add(time.Hour), end: now.Add(2 * time.Hour)})
	}
	v := view(now, a, "").Proto()
	if texts := plugintest.Texts(v.GetBody()); !slices.Contains(texts, "100 of 105 events") || len(items(v.GetBody())) != maxItems {
		t.Errorf("texts %q, %d items", texts, len(items(v.GetBody())))
	}
}

// The views fit the least body the plugin asks for, minBody, at the
// default text size.
// Its list scrolls, so it fits any room left; minBody keeps room for
// two events (57 dp each) besides.
func TestMinBody(t *testing.T) {
	// From the narrowest body a tile has, or the least width it asks for.
	for _, width := range []int{124, 168, 300, 432, 600, 900} {
		testMinBodyAt(t, max(width, minBody.Width))
	}
}

func testMinBodyAt(t *testing.T, width int) {
	t.Helper()
	for i, v := range minBodyViews() {
		if !ui.ViewFits(v.Proto(), image.Pt(width, minBody.Height), 1) {
			t.Errorf("view %d does not fit %d by %d dp", i, width, minBody.Height)
		}
	}
}

// minBodyViews are the views minBody is measured with.
func minBodyViews() []sdk.View {
	now := time.Date(2026, 10, 14, 10, 15, 0, 0, time.UTC)
	e := event{key: "a", title: "Design review", start: now.Add(time.Hour), end: now.Add(2 * time.Hour), location: "Room 4"}
	return []sdk.View{view(now, agenda{title: "Today", loaded: true, events: []event{e}}, ""), view(now, agenda{loaded: true}, "")}
}
