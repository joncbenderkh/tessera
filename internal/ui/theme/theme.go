// SPDX-License-Identifier: GPL-3.0-or-later

// Package theme holds the host's design tokens: the dark and light
// palettes and the spacing, radius, and type scales of the mockups in
// docs/design.
package theme

import (
	"image/color"
	"math"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/text"
	"gioui.org/unit"
)

// Palette is one theme's colours, named as in the mockups' CSS.
type Palette struct {
	Bg, Surface, Fg, Muted, Border, Accent color.NRGBA
	Ok, Warn, Error                        color.NRGBA
	Hover, Inset                           color.NRGBA
}

// Light and Dark copy the mockups' :root and [data-theme="dark"] values.
var (
	Light = Palette{
		Bg:      oklch(.98, .005, 250),
		Surface: oklch(1, 0, 0),
		Fg:      oklch(.22, .02, 240),
		Muted:   oklch(.50, .018, 240),
		Border:  oklch(.90, .008, 240),
		Accent:  oklch(.58, .16, 145),
		Ok:      oklch(.45, .12, 150),
		Warn:    oklch(.47, .11, 65),
		Error:   oklch(.49, .17, 25),
		Hover:   oklch(.94, .005, 250),
		Inset:   oklch(.96, .005, 250),
	}
	Dark = Palette{
		Bg:      oklch(.16, .009, 250),
		Surface: oklch(.20, .009, 250),
		Fg:      oklch(.92, .006, 250),
		Muted:   oklch(.68, .014, 250),
		Border:  oklch(.30, .012, 250),
		Accent:  oklch(.72, .14, 150),
		Ok:      oklch(.66, .12, 150),
		Warn:    oklch(.76, .13, 78),
		Error:   oklch(.67, .17, 25),
		Hover:   oklch(.27, .012, 250),
		Inset:   oklch(.18, .009, 250),
	}
)

// Scrim dims the window behind a dialog.
var Scrim = color.NRGBA{A: 153}

// Spacing, radius, and type scales, in dp and sp.
const (
	Space4  unit.Dp = 4
	Space8  unit.Dp = 8
	Space12 unit.Dp = 12
	Space16 unit.Dp = 16
	Space24 unit.Dp = 24

	RadiusSmall unit.Dp = 5
	RadiusLarge unit.Dp = 8

	// TextMin is the text floor; no host text is smaller.
	TextMin   unit.Sp = 11
	TextSmall unit.Sp = 12
	// TextBase is body text: buttons and the window's default.
	TextBase  unit.Sp = 13
	TextBody  unit.Sp = 14
	TextValue unit.Sp = 36
	// TextStat is the stat size for compact tiles.
	TextStat unit.Sp = 24
	// TextDialog and TextBrand are the dialog title and the app name.
	TextDialog unit.Sp = 17
	TextBrand  unit.Sp = 18
	// TextIcon is the glyph size of icon buttons.
	TextIcon unit.Sp = 16
)

// Faces, as the mockups' --font-body and --font-mono lists.
var (
	Body = font.Font{Typeface: "Inter, Segoe UI, sans-serif"}
	Mono = font.Font{Typeface: "JetBrains Mono, IBM Plex Mono, Menlo, monospace"}
)

// Theme is a palette with the shaper that sets its text.
type Theme struct {
	Palette
	Shaper *text.Shaper
	Dark   bool
}

// NewShaper returns a shaper that falls back to the Go fonts. With
// systemFonts false it uses nothing else, so text measures the same on
// every machine, as tests need.
func NewShaper(systemFonts bool) *text.Shaper {
	opts := []text.ShaperOption{text.WithCollection(gofont.Collection())}
	if !systemFonts {
		opts = append(opts, text.NoSystemFonts())
	}
	return text.NewShaper(opts...)
}

// New returns the dark or light theme.
func New(dark bool, shaper *text.Shaper) *Theme {
	p := Light
	if dark {
		p = Dark
	}
	return &Theme{Palette: p, Shaper: shaper, Dark: dark}
}

// oklch converts a CSS oklch() colour (lightness 0–1, chroma, hue in
// degrees) to sRGB, clamping colours outside the gamut.
func oklch(l, c, h float64) color.NRGBA {
	a := c * math.Cos(h*math.Pi/180)
	b := c * math.Sin(h*math.Pi/180)
	lc := cube(l + 0.3963377774*a + 0.2158037573*b)
	mc := cube(l - 0.1055613458*a - 0.0638541728*b)
	sc := cube(l - 0.0894841775*a - 1.2914855480*b)
	return color.NRGBA{
		R: encode(4.0767416621*lc - 3.3077115913*mc + 0.2309699292*sc),
		G: encode(-1.2684380046*lc + 2.6097574011*mc - 0.3413193965*sc),
		B: encode(-0.0041960863*lc - 0.7034186147*mc + 1.7076147010*sc),
		A: 255,
	}
}

func cube(x float64) float64 { return x * x * x }

// encode applies the sRGB transfer function to a linear channel.
func encode(x float64) uint8 {
	if x <= 0.0031308 {
		x *= 12.92
	} else {
		x = 1.055*math.Pow(x, 1/2.4) - 0.055
	}
	return uint8(math.Round(255 * math.Max(0, math.Min(1, x))))
}
