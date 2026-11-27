// SPDX-License-Identifier: GPL-3.0-or-later

package palette

import (
	"image/color"
	"math"
	"slices"
	"strings"
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
		got := OKLCH(tt.l, tt.c, tt.h)
		if !near(got, tt.want) {
			t.Errorf("oklch(%v %v %v) = %v, want %v", tt.l, tt.c, tt.h, got, tt.want)
		}
	}
}

func near(a, b color.NRGBA) bool {
	d := func(x, y uint8) bool { return max(x, y)-min(x, y) <= 1 }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && a.A == b.A
}

func TestParse(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want color.NRGBA
	}{
		{"#fff", color.NRGBA{255, 255, 255, 255}},
		{"#1E1e2e", color.NRGBA{0x1e, 0x1e, 0x2e, 255}},
		{"#a1b", color.NRGBA{0xaa, 0x11, 0xbb, 255}},
		{"oklch(1 0 0)", color.NRGBA{255, 255, 255, 255}},
		{"oklch(62.7955% .257683 29.2339)", color.NRGBA{255, 0, 0, 255}},
		{"oklch( 0.519752  0.176858 142.495 )", color.NRGBA{0, 128, 0, 255}},
		{"oklch(.452014 .313214 -95.948)", color.NRGBA{0, 0, 255, 255}},
		{"oklch(.452014 .313214 3864.052)", color.NRGBA{0, 0, 255, 255}}, // ten turns more
		{"oklch(0.5 1 0)", OKLCH(.5, 1, 0)},
	} {
		got, err := Parse(tt.in)
		if err != nil || !near(got, tt.want) {
			t.Errorf("Parse(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{
		"", "fff", "#ffff", "#fffffff", "#ggg", "#ffffff80", "red",
		"oklch(1 0)", "oklch(1 0 0 0)", "oklch(1 0 0 / 1)", "OKLCH(1 0 0)", "oklch(1,0,0)",
		"oklch(1.1 0 0)", "oklch(101% 0 0)", "oklch(-0.1 0 0)", "oklch(1 -0.1 0)",
		"oklch(1 0 0deg)", "oklch(0.5 1.01 0)", "oklch(0.5 1" + strings.Repeat("0", 400) + " 0)", "oklch(NaN 0 0)", "oklch(1 Inf 0)", "oklch(1 0 1e2)", "oklch(1 0x1 0)", "oklch(1 0 0) ",
	} {
		if c, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %v, want an error", in, c)
		}
	}
}

// A hue too large to turn into radians is still a hue.
func TestParseHugeHue(t *testing.T) {
	huge, err := Parse("oklch(0.5 0.1 1" + strings.Repeat("0", 307) + ")")
	if err != nil {
		t.Fatal(err)
	}
	if want := OKLCH(.5, .1, math.Mod(1e307, 360)); huge != want {
		t.Errorf("a huge hue gave %v, want %v", huge, want)
	}
}

func TestContrast(t *testing.T) {
	black, white := color.NRGBA{A: 255}, color.NRGBA{255, 255, 255, 255}
	if r := Contrast(black, white); r < 20.99 || r > 21.01 {
		t.Errorf("black on white: %v, want 21", r)
	}
	if r := Contrast(white, white); r != 1 {
		t.Errorf("white on white: %v, want 1", r)
	}
}

func TestBuiltinsReadable(t *testing.T) {
	for name, p := range map[string]Palette{"light": Light, "dark": Dark} {
		if s := p.Shortfalls(); len(s) > 0 {
			t.Errorf("%s: %v", name, s)
		}
	}
}

func TestShortfalls(t *testing.T) {
	p := Dark
	p.Muted = p.Surface
	p.Accent = OKLCH(.4, .14, 150)
	got := p.Shortfalls()
	var names []string
	for _, s := range got {
		names = append(names, s.Text+"/"+s.On)
	}
	if want := []string{"muted/bg", "muted/surface", "accent/bg", "accent/surface"}; !slices.Equal(names, want) {
		t.Fatalf("shortfalls %v, want %v", got, want)
	}
	if s := got[1].String(); s != "muted on surface has contrast 1.00:1, below 4.5:1" {
		t.Errorf("String() = %q", s)
	}
}

func TestTokens(t *testing.T) {
	var p Palette
	seen := map[*color.NRGBA]bool{}
	for _, name := range Tokens() {
		c := p.Token(name)
		if c == nil || seen[c] {
			t.Errorf("token %q: %p", name, c)
		}
		seen[c] = true
	}
	if p.Token("nope") != nil {
		t.Error("unknown token found")
	}
}
