// SPDX-License-Identifier: GPL-3.0-or-later

// Package theme holds the host's design tokens: the palette a theme
// draws with and the spacing, radius, and type scales of the mockups in
// docs/design.
package theme

import (
	"image/color"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/palette"
)

// Palette is one theme's colours; see package palette.
type Palette = palette.Palette

// Light and Dark are the built-in palettes.
var (
	Light = palette.Light
	Dark  = palette.Dark
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

// New returns the dark or light built-in theme.
func New(dark bool, shaper *text.Shaper) *Theme {
	p := Light
	if dark {
		p = Dark
	}
	return &Theme{Palette: p, Shaper: shaper, Dark: dark}
}
