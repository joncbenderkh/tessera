// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"go/build"
	"image"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	"github.com/joncbenderkh/tessera/internal/ui"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
	"github.com/joncbenderkh/tessera/sdk"
)

func TestMain(m *testing.M) { plugintest.Main(m, main) }

func TestWeeks(t *testing.T) {
	// From Monday 1 June 2026 to Sunday 1 November 2026, a month starts
	// on each weekday.
	starts := map[time.Weekday]time.Month{
		time.Monday: time.June, time.Tuesday: time.September, time.Wednesday: time.April,
		time.Thursday: time.October, time.Friday: time.May, time.Saturday: time.August,
		time.Sunday: time.November,
	}
	for weekday, mo := range starts {
		for _, first := range []time.Weekday{time.Monday, time.Sunday} {
			ws := weeks(2026, mo, first)
			lead := (int(weekday) - int(first) + 7) % 7
			if got := slices.Index(ws[0][:], 1); got != lead {
				t.Errorf("%v 2026 from %v: day 1 in column %d, want %d", mo, first, got, lead)
			}
			days := time.Date(2026, mo+1, 0, 0, 0, 0, 0, time.UTC).Day()
			if want := (lead + days + 6) / 7; len(ws) != want {
				t.Errorf("%v 2026 from %v: %d weeks, want %d", mo, first, len(ws), want)
			}
			var all []int
			for _, w := range ws {
				all = append(all, w[:]...)
			}
			all = slices.DeleteFunc(all, func(d int) bool { return d == 0 })
			if len(all) != days || !slices.IsSorted(all) || all[0] != 1 {
				t.Errorf("%v 2026 from %v: days %v, want 1 to %d", mo, first, all, days)
			}
		}
	}
}

func TestWeeksShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		y     int
		mo    time.Month
		first time.Weekday
		want  [][7]int
	}{
		{"common February from its first weekday", 2027, time.February, time.Monday, [][7]int{
			{1, 2, 3, 4, 5, 6, 7},
			{8, 9, 10, 11, 12, 13, 14},
			{15, 16, 17, 18, 19, 20, 21},
			{22, 23, 24, 25, 26, 27, 28},
		}},
		{"leap February", 2028, time.February, time.Sunday, [][7]int{
			{0, 0, 1, 2, 3, 4, 5},
			{6, 7, 8, 9, 10, 11, 12},
			{13, 14, 15, 16, 17, 18, 19},
			{20, 21, 22, 23, 24, 25, 26},
			{27, 28, 29, 0, 0, 0, 0},
		}},
		{"six weeks", 2026, time.August, time.Monday, [][7]int{
			{0, 0, 0, 0, 0, 1, 2},
			{3, 4, 5, 6, 7, 8, 9},
			{10, 11, 12, 13, 14, 15, 16},
			{17, 18, 19, 20, 21, 22, 23},
			{24, 25, 26, 27, 28, 29, 30},
			{31, 0, 0, 0, 0, 0, 0},
		}},
	} {
		if got := weeks(tc.y, tc.mo, tc.first); !slices.Equal(got, tc.want) {
			t.Errorf("%s: weeks = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// accented is the texts in the accent tone in a view's body.
func accented(v sdk.View) []string {
	var got []string
	for _, w := range plugintest.Widgets(v.Proto().GetBody()) {
		if w.GetText().GetTone() == sdk.ToneAccent {
			got = append(got, w.GetText().GetValue())
		}
	}
	return got
}

func TestView(t *testing.T) {
	v := view(time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC), time.Monday)
	texts := plugintest.Texts(v.Proto().GetBody())
	if want := []string{"August 2026", " M", " T", " W", " T", " F", " S", " S", " 1", " 2", " 3"}; !slices.Equal(texts[:len(want)], want) {
		t.Errorf("view starts %q, want %q", texts[:len(want)], want)
	}
	if got, want := texts[len(texts)-1], "31"; got != want {
		t.Errorf("view ends %q, want %q", got, want)
	}
	if got := len(texts); got != 1+7+31 {
		t.Errorf("%d texts, want the heading, 7 weekdays, and 31 days", got)
	}
	sunday := plugintest.Texts(view(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Sunday).Proto().GetBody())
	if want := []string{" S", " M", " T", " W", " T", " F", " S"}; !slices.Equal(sunday[1:8], want) {
		t.Errorf("weekdays from Sunday %q, want %q", sunday[1:8], want)
	}
}

func TestViewToday(t *testing.T) {
	for _, today := range []time.Time{
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 28, 23, 59, 0, 0, time.UTC),
		time.Date(2028, 2, 29, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 31, 9, 0, 0, 0, time.UTC),
	} {
		for _, first := range []time.Weekday{time.Monday, time.Sunday} {
			want := []string{fmt.Sprintf("%2d", today.Day())}
			if got := accented(view(today, first)); !slices.Equal(got, want) {
				t.Errorf("%v from %v: accented %q, want %q", today, first, got, want)
			}
		}
	}
}

func TestDue(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		now   time.Time
		shown string
		show  bool
		sleep time.Duration
	}{
		{time.Date(2026, 10, 31, 12, 0, 0, 0, berlin), "", true, wake},
		{time.Date(2026, 10, 31, 12, 0, 0, 0, berlin), "2026-10-31", false, wake},
		{time.Date(2026, 10, 31, 23, 59, 30, 0, berlin), "2026-10-31", false, 30 * time.Second},
		{time.Date(2026, 11, 1, 0, 0, 0, 0, berlin), "2026-10-31", true, wake},
		{time.Date(2026, 10, 30, 23, 0, 0, 0, berlin), "2026-10-31", true, wake}, // set back
		// Midnight is skipped: 6 September starts at 01:00.
		{time.Date(2026, 9, 5, 23, 30, 0, 0, zone(t, "America/Santiago")), "2026-09-05", false, wake},
	} {
		show, sleep := due(tc.now, tc.shown)
		if show != tc.show || sleep != tc.sleep {
			t.Errorf("due(%v, %q) = %v, %v; want %v, %v", tc.now, tc.shown, show, sleep, tc.show, tc.sleep)
		}
	}
}

func TestSystemZone(t *testing.T) {
	// A zone file as /etc/localtime holds, from the toolchain's database.
	db, err := zip.OpenReader(filepath.Join(build.Default.GOROOT, "lib", "time", "zoneinfo.zip"))
	if err != nil {
		t.Skip(err)
	}
	defer db.Close()
	data, err := fs.ReadFile(db, "Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	localtime := filepath.Join(t.TempDir(), "localtime")
	if err := os.WriteFile(localtime, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", "")
	os.Unsetenv("TZ")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, offset := at.In(systemZone(localtime)).Zone(); offset != 9*3600 {
		t.Errorf("systemZone(Asia/Tokyo's file) is %d s from UTC, want 9 h", offset)
	}
	if got := systemZone(filepath.Join(t.TempDir(), "missing")); got != time.Local {
		t.Errorf("systemZone(a missing file) = %v, want time.Local", got)
	}
	t.Setenv("TZ", "Asia/Tokyo")
	if got := systemZone(localtime); got != time.Local {
		t.Errorf("systemZone with TZ set = %v, want time.Local", got)
	}
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// The schema's pattern for first_weekday accepts what weekdayOf does.
func TestSchemaWeekday(t *testing.T) {
	var sch struct {
		Properties struct {
			FirstWeekday struct {
				Pattern string `json:"pattern"`
			} `json:"first_weekday"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &sch); err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(sch.Properties.FirstWeekday.Pattern)
	for _, name := range []string{"", "   ", "monday", "sunday", "Monday", " SUNDAY ", "\vmonday", "\u00a0monday", "\ufeffmonday", "saturday", "mon", "mondays", "0"} {
		_, err := weekdayOf(name)
		if got, want := pattern.MatchString(name), err == nil; got != want {
			t.Errorf("pattern matches %q: %v; weekdayOf accepts it: %v", name, got, want)
		}
	}
}

func TestWeekdayOf(t *testing.T) {
	for name, want := range map[string]time.Weekday{"": time.Monday, "monday": time.Monday, " Sunday ": time.Sunday} {
		if got, err := weekdayOf(name); err != nil || got != want {
			t.Errorf("weekdayOf(%q) = %v, %v; want %v", name, got, err, want)
		}
	}
	for _, name := range []string{"saturday", "mon", "0"} {
		if _, err := weekdayOf(name); err == nil {
			t.Errorf("weekdayOf(%q) succeeded", name)
		}
	}
}

// A month of 6 weeks fits the default size, 1 column by 2 rows, at
// the least row height config allows and the default text size, in a
// column from 200 dp (internal/ui's minColumn) to 400 dp; wider ones
// only add room. No cell wraps.
func TestViewFits(t *testing.T) {
	const rowHeight, gridGap = 150, 14
	v := view(time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), time.Monday)
	unwrapped := ui.ViewSize(v.Proto(), 1<<20, 1).Y
	for colW := 200; colW <= 400; colW += 4 {
		body := ui.TileBodySize(image.Pt(colW, 2*rowHeight+gridGap), v.Title, 1)
		if got := ui.ViewSize(v.Proto(), body.X, 1); got.Y > body.Y || got.Y > unwrapped {
			t.Errorf("%d dp wide: %d dp tall, over the body's %d or the unwrapped %d", colW, got.Y, body.Y, unwrapped)
		}
	}
}

func TestMonth(t *testing.T) {
	p := plugintest.Launch(t, "month")
	if got := p.Info.GetName(); got != "month" {
		t.Errorf("name %q, want month", got)
	}
	for _, s := range []map[string]any{
		{"first_weekday": "saturday"},
		{"first_weekday": 1},
		{"timezone": "Mars/Olympus"},
		{"week_start": "monday"},
	} {
		if err := p.Configure(s); status.Code(err) != codes.InvalidArgument {
			t.Errorf("settings %v: %v, want INVALID_ARGUMENT", s, err)
		}
	}
	if err := p.Configure(map[string]any{"first_weekday": "sunday", "timezone": "pacific/kiritimati"}); err != nil {
		t.Fatal(err)
	}
	kiritimati, err := time.LoadLocation("Pacific/Kiritimati")
	if err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		texts := plugintest.Texts(v.GetBody())
		// Unless midnight in Kiritimati passes in between.
		return len(texts) > 8 && texts[0] == time.Now().In(kiritimati).Format("January 2006") && texts[1] == " S"
	})
}
