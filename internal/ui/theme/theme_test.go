// SPDX-License-Identifier: GPL-3.0-or-later

package theme

import (
	"image/color"
	"testing"
)

func TestOKLCH(t *testing.T) {
	for _, tt := range []struct {
		l, c, h float64
		want    color.NRGBA
	}{
		{1, 0, 0, color.NRGBA{255, 255, 255, 255}},
		{0, 0, 0, color.NRGBA{0, 0, 0, 255}},
		{.627955, .257683, 29.2339, color.NRGBA{255, 0, 0, 255}}, // CSS red
		{.519752, .176858, 142.495, color.NRGBA{0, 128, 0, 255}}, // CSS green
		{.452014, .313214, 264.052, color.NRGBA{0, 0, 255, 255}}, // CSS blue
	} {
		got := oklch(tt.l, tt.c, tt.h)
		if !near(got, tt.want) {
			t.Errorf("oklch(%v %v %v) = %v, want %v", tt.l, tt.c, tt.h, got, tt.want)
		}
	}
}

func near(a, b color.NRGBA) bool {
	d := func(x, y uint8) bool { return max(x, y)-min(x, y) <= 1 }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && a.A == b.A
}
