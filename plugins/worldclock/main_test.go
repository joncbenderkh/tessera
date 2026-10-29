// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/plugins/internal/zones"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
	"github.com/joncbenderkh/tessera/sdk"
)

func TestMain(m *testing.M) { plugintest.Main(m, main) }

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestPlacesOf(t *testing.T) {
	labels := func(places []place) []string {
		var l []string
		for _, p := range places {
			name := p.loc.String()
			if p.loc == time.Local {
				name = "Local"
			}
			l = append(l, p.label+" "+name)
		}
		return l
	}
	places, err := placesOf(settings{})
	if err != nil || !slices.Equal(labels(places), []string{"Local Local"}) {
		t.Errorf("no clocks: %q, %v; want one local clock", labels(places), err)
	}
	places, err = placesOf(settings{Clocks: []*clockSettings{
		{Timezone: "asia/tokyo"},
		{Timezone: "America/Argentina/Buenos_Aires"},
		{Timezone: "UTC", Label: " Build\n servers "},
		{Timezone: "Etc/GMT0"},
	}})
	want := []string{"Tokyo Asia/Tokyo", "Buenos Aires America/Argentina/Buenos_Aires", "Build servers UTC", "GMT Etc/GMT0"}
	if err != nil || !slices.Equal(labels(places), want) {
		t.Errorf("four clocks: %q, %v; want %q", labels(places), err, want)
	}
	for _, tc := range []struct {
		clocks []*clockSettings
		want   string
	}{
		{[]*clockSettings{}, "want 1 to 4 clocks, not 0"},
		{make([]*clockSettings, 5), "want 1 to 4 clocks, not 5"},
		{[]*clockSettings{{}, {Timezone: "Mars/Olympus"}}, `clock 2: unknown time zone "Mars/Olympus"`},
		{[]*clockSettings{{}, nil}, "clock 2: want an object, not null"},
	} {
		if _, err := placesOf(settings{Clocks: tc.clocks}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%d clocks: %v, want %q", len(tc.clocks), err, tc.want)
		}
	}
}

func TestDetail(t *testing.T) {
	berlin := time.Date(2026, 10, 14, 23, 30, 0, 0, zone(t, "Europe/Berlin"))
	pagoPago := time.Date(2026, 10, 14, 23, 30, 0, 0, zone(t, "Pacific/Pago_Pago"))
	for _, tc := range []struct {
		local      time.Time
		zone       string
		label      string
		utcOffsets bool
		want       string
	}{
		{berlin, "Europe/Berlin", "", false, "CEST · Same time"}, // loaded anew: not the local zone itself
		{berlin, "Asia/Tokyo", "", false, "Tomorrow · JST · +7 h"},
		{berlin, "Asia/Kolkata", "", false, "Tomorrow · IST · +3:30 h"},
		{berlin, "America/New_York", "", false, "EDT · −6 h"},
		{berlin, "Asia/Bangkok", "", false, "Tomorrow · +5 h"},
		{berlin.Add(-23 * time.Hour), "Pacific/Honolulu", "", false, "Yesterday · HST · −12 h"},
		{pagoPago, "Pacific/Kiritimati", "", false, "Fri 16 Oct · +25 h"},
		{berlin, "Asia/Bangkok", "", true, "Tomorrow · UTC+7 · +5 h"},
		{berlin, "Asia/Tokyo", "", true, "Tomorrow · JST · +7 h"},
		{berlin, "Asia/Kathmandu", "", true, "Tomorrow · UTC+5:45 · +3:45 h"},
		{berlin, "Europe/Paris", "", false, "CEST · Same time"},
		{berlin, "Etc/GMT+5", "UTC−5", true, "−7 h"}, // its default label
		{berlin, "Etc/GMT+5", "Ship", true, "UTC−5 · −7 h"},
		{berlin, "UTC", "UTC", false, "−2 h"}, // its default label
		{berlin, "Etc/GMT", "GMT", false, "−2 h"},
		{berlin, "UTC", "Greenwich", false, "UTC · −2 h"},
	} {
		if got := detail(tc.local.In(zone(t, tc.zone)), tc.local, tc.label, tc.utcOffsets); got != tc.want {
			t.Errorf("%s at %v: %q, want %q", tc.zone, tc.local, got, tc.want)
		}
	}
}

func TestDefaultLabel(t *testing.T) {
	for name, want := range map[string]string{
		"Etc/GMT+5":                      "UTC−5",
		"Etc/GMT-14":                     "UTC+14",
		"Etc/GMT0":                       "GMT",
		"Etc/GMT":                        "GMT",
		"America/Argentina/Buenos_Aires": "Buenos Aires",
	} {
		if got := defaultLabel(name, zone(t, name)); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestZoneName(t *testing.T) {
	at := time.Date(2026, 10, 14, 23, 30, 0, 0, time.UTC)
	for name, want := range map[string]string{
		"Europe/Berlin":  "CEST",
		"Asia/Bangkok":   "UTC+7",
		"Asia/Kathmandu": "UTC+5:45",
		"Etc/GMT+5":      "UTC−5",
	} {
		if got := zoneName(at.In(zone(t, name))); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestDue(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 41, 58, 500e6, time.UTC)
	shown := at.Unix() / 60
	for _, tc := range []struct {
		now   time.Time
		show  bool
		sleep time.Duration
	}{
		{at, false, 1500 * time.Millisecond},                // to the next minute
		{at.Add(-50 * time.Second), false, wake},            // at most wake, as after a suspend
		{at.Add(2 * time.Second), true, wake},               // the minute turned
		{at.Add(-time.Hour), true, 1500 * time.Millisecond}, // set back
	} {
		show, sleep := due(tc.now, shown)
		if show != tc.show || sleep != tc.sleep {
			t.Errorf("due(%v) = %v, %v; want %v, %v", tc.now, show, sleep, tc.show, tc.sleep)
		}
	}
}

func TestShortDetail(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	local := time.Date(2026, 10, 14, 23, 30, 0, 0, berlin)
	for _, tc := range []struct {
		loc  *time.Location
		want string
	}{
		{zone(t, "Europe/Paris"), "±0 h"},
		{zone(t, "Asia/Tokyo"), "+1 d · +7 h"},
		{zone(t, "America/New_York"), "−6 h"},
	} {
		if got := shortDetail(local.In(tc.loc), local); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.loc, got, tc.want)
		}
	}
}

func TestPerRow(t *testing.T) {
	for _, tc := range []struct {
		n    int
		size sdk.Size
		want int
	}{
		{1, sdk.Size{Columns: 1, Rows: 1}, 1},
		{2, sdk.Size{Columns: 1, Rows: 1}, 2},
		{3, sdk.Size{Columns: 1, Rows: 1}, 2},
		{4, sdk.Size{Columns: 1, Rows: 1}, 2},
		{4, sdk.Size{}, 2},
		{4, sdk.Size{Columns: 2, Rows: 1}, 4},
		{4, sdk.Size{Columns: 1, Rows: 3}, 1},
	} {
		if got := perRow(tc.n, tc.size); got != tc.want {
			t.Errorf("perRow(%d, %v) = %d, want %d", tc.n, tc.size, got, tc.want)
		}
	}
}

func TestView(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	now := time.Date(2026, 10, 14, 23, 41, 8, 0, berlin)
	places := []place{{"Berlin", berlin}, {"Tokyo", zone(t, "Asia/Tokyo")}, {"New York", zone(t, "America/New_York")}}
	timeOf := func(clock *tesserav1.Stack) tesserav1.TextSize {
		return clock.GetItems()[1].GetWidget().GetText().GetSize()
	}
	clocks := func(body *tesserav1.Widget) (shape []int, first *tesserav1.Stack) {
		for _, row := range body.GetStack().GetItems() {
			shape = append(shape, len(row.GetWidget().GetStack().GetItems()))
		}
		return shape, body.GetStack().GetItems()[0].GetWidget().GetStack().GetItems()[0].GetWidget().GetStack()
	}

	// Three in one cell: two rows of two, the last with an empty place
	// that keeps the columns aligned, and compact, the local clock with
	// its time zone for an offset.
	body := view(now, places, sdk.Size{Columns: 1, Rows: 1}, false).Proto().GetBody()
	want := []string{
		"Berlin", "23:41", "CEST",
		"Tokyo", "06:41", "+1 d · +7 h",
		"New York", "17:41", "−6 h",
	}
	if got := plugintest.Texts(body); !slices.Equal(got, want) {
		t.Errorf("compact texts %q, want %q", got, want)
	}
	shape, first := clocks(body)
	if !slices.Equal(shape, []int{2, 2}) {
		t.Errorf("rows %v, want [2 2]", shape)
	}
	if gap := body.GetStack().GetGap(); gap != sdk.SpacingSmall || first.GetGap() != sdk.SpacingExtraSmall || timeOf(first) != sdk.SizeLarge {
		t.Errorf("compact: gaps %v and %v, time %v; want small, extra small, large", gap, first.GetGap(), timeOf(first))
	}

	// Two in two cells side by side, one each: in full.
	body = view(now, places[1:], sdk.Size{Columns: 2, Rows: 1}, false).Proto().GetBody()
	want = []string{"Tokyo", "06:41", "Tomorrow · JST · +7 h", "New York", "17:41", "EDT · −6 h"}
	if got := plugintest.Texts(body); !slices.Equal(got, want) {
		t.Errorf("full texts %q, want %q", got, want)
	}
	if shape, first := clocks(body); !slices.Equal(shape, []int{2}) || timeOf(first) != sdk.SizeDisplay {
		t.Errorf("two clocks in two cells: rows %v, time %v; want [2] at display size", shape, timeOf(first))
	}
}

// Clocks fit their panel at the default row height and text size: a
// one-cell panel at any column width a window gives, and a panel one
// row tall and up to four wide squeezed into the one column of a
// narrow window.
func TestViewFits(t *testing.T) {
	chatham := zone(t, "Pacific/Chatham") // +13:45 h from UTC in its summer
	now := time.Date(2026, 1, 14, 23, 59, 0, 0, zone(t, "Etc/GMT+1"))
	worst := func(label string) []place {
		return []place{{label, chatham}, {"Buenos Aires", chatham}, {label, chatham}, {"Buenos Aires", chatham}}
	}
	fits := func(places []place, size sdk.Size, tileW int) {
		t.Helper()
		body := ui.TileBodySize(image.Pt(tileW, 180), "World clock", 1)
		if got := ui.ViewSize(view(now, places, size, false).Proto(), body.X, 1); got.Y > body.Y {
			t.Errorf("%d clocks labelled %q, %v panel %d dp wide: %d dp tall, over the body's %d",
				len(places), places[0].label, size, tileW, got.Y, body.Y)
		}
	}
	// A column is 200 to 400 dp wide (internal/ui's minColumn, and the
	// widest a 4-column grid gets). A label that needs two lines takes
	// the room of one, so the widest default label fits from 220 dp,
	// and one as wide as Buenos Aires in any column.
	widest := widestLabel(t)
	for colW := 200; colW <= 400; colW += 4 {
		for n := 1; n <= maxClocks; n++ {
			fits(worst("Buenos Aires")[:n], sdk.Size{Columns: 1, Rows: 1}, colW)
			if colW >= 220 {
				fits(worst(widest)[:n], sdk.Size{Columns: 1, Rows: 1}, colW)
			}
		}
	}
	// The one column of a window 360 dp wide, its narrowest.
	for n := 2; n <= maxClocks; n++ {
		fits(worst(widest)[:n], sdk.Size{Columns: n, Rows: 1}, 332)
	}
}

// widestLabel is the default label of a zone that is widest on one line.
func widestLabel(t *testing.T) string {
	t.Helper()
	var widest string
	var widestW int
	for _, name := range zones.Names() {
		loc, zone, err := zones.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		label := defaultLabel(zone, loc)
		v := sdk.View{Title: "Label", Body: sdk.Text(label).Size(sdk.SizeSmall)}
		if w := ui.ViewSize(v.Proto(), 1<<20, 1).X; w > widestW {
			widest, widestW = label, w
		}
	}
	t.Logf("widest default label: %q, %d dp", widest, widestW)
	return widest
}

func TestWorldClock(t *testing.T) {
	p := plugintest.Launch(t, "worldclock")
	if got := p.Info.GetName(); got != "worldclock" {
		t.Errorf("name %q, want worldclock", got)
	}
	tokyo := map[string]any{"timezone": "asia/tokyo", "label": "Office"}
	for _, clocks := range []any{
		[]any{},
		[]any{tokyo, tokyo, tokyo, tokyo, tokyo},
		[]any{map[string]any{"timezone": "Mars/Olympus"}},
		[]any{map[string]any{"city": "Tokyo"}},
		[]any{nil},
		"Asia/Tokyo",
	} {
		if err := p.Configure(map[string]any{"clocks": clocks}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("clocks %v: %v, want INVALID_ARGUMENT", clocks, err)
		}
	}
	if err := p.Configure(map[string]any{"utc_offsets": "yes"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("utc_offsets yes: %v, want INVALID_ARGUMENT", err)
	}
	bangkok := map[string]any{"timezone": "Asia/Bangkok"}
	if err := p.Configure(map[string]any{"clocks": []any{bangkok}, "utc_offsets": true}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		texts := plugintest.Texts(v.GetBody())
		return len(texts) == 3 && texts[0] == "Bangkok" && strings.Contains(texts[2], "UTC+7 ")
	})
	if err := p.Configure(map[string]any{"clocks": []any{tokyo}}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		texts := plugintest.Texts(v.GetBody())
		return len(texts) == 3 && texts[0] == "Office" && strings.Contains(texts[2], "JST")
	})
}
