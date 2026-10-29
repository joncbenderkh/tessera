// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

func TestMain(m *testing.M) { plugintest.Main(m, main) }

func TestView(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 9, 41, 8, 0, berlin)
	got := plugintest.Texts(view(now, "Europe/Berlin").Proto().GetBody())
	want := []string{"09:41:08", "Wednesday, October 14", "Europe/Berlin · CEST"}
	if !slices.Equal(got, want) {
		t.Errorf("view = %q, want %q", got, want)
	}
}

func TestZoneLabel(t *testing.T) {
	utc := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		zone string
		t    time.Time
		want string
	}{
		{"UTC", utc, "UTC"},
		{"Local", utc, "Local · UTC"},
		{"Local", utc.In(time.FixedZone("", 3600)), "Local"},
		{"Asia/Bangkok", utc.In(time.FixedZone("+07", 7*3600)), "Asia/Bangkok · +07"},
	} {
		if got := zoneLabel(tc.t, tc.zone); got != tc.want {
			t.Errorf("zoneLabel(%v, %q) = %q, want %q", tc.t, tc.zone, got, tc.want)
		}
	}
}

func TestClock(t *testing.T) {
	p := plugintest.Launch(t, "clock")
	if got := p.Info.GetName(); got != "clock" {
		t.Errorf("name %q, want clock", got)
	}
	for _, zone := range []any{"Mars/Olympus", 3.0} {
		if err := p.Configure(map[string]any{"timezone": zone}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("timezone %v: %v, want INVALID_ARGUMENT", zone, err)
		}
	}
	if err := p.Configure(map[string]any{"timezone": "asia/tokyo"}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		texts := plugintest.Texts(v.GetBody())
		return len(texts) == 3 && texts[2] == "Asia/Tokyo · JST"
	})
}
