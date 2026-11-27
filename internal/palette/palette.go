// SPDX-License-Identifier: GPL-3.0-or-later

// Package palette holds the window's colour tokens: the built-in dark
// and light palettes, the colour syntax a config may give a token in,
// and the contrast between two colours. It draws nothing, so the config
// package can check themes without the GUI.
package palette

import (
	"fmt"
	"image/color"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Palette is one theme's colours, named as in the mockups' CSS.
type Palette struct {
	Bg, Surface, Fg, Muted, Border, Accent color.NRGBA
	Ok, Warn, Error                        color.NRGBA
	Hover, Inset                           color.NRGBA
}

// tokens are the palette's token names, in the order the config
// documents them.
var tokens = []string{"bg", "surface", "fg", "muted", "border", "accent", "ok", "warn", "error", "hover", "inset"}

// Tokens lists the palette's token names, in the order the config
// documents them.
func Tokens() []string { return slices.Clone(tokens) }

// Token is the colour named name, one of Tokens, or nil.
func (p *Palette) Token(name string) *color.NRGBA {
	switch name {
	case "bg":
		return &p.Bg
	case "surface":
		return &p.Surface
	case "fg":
		return &p.Fg
	case "muted":
		return &p.Muted
	case "border":
		return &p.Border
	case "accent":
		return &p.Accent
	case "ok":
		return &p.Ok
	case "warn":
		return &p.Warn
	case "error":
		return &p.Error
	case "hover":
		return &p.Hover
	case "inset":
		return &p.Inset
	}
	return nil
}

// Light and Dark copy the mockups' :root and [data-theme="dark"] values.
var (
	Light = Palette{
		Bg:      OKLCH(.98, .005, 250),
		Surface: OKLCH(1, 0, 0),
		Fg:      OKLCH(.22, .02, 240),
		Muted:   OKLCH(.50, .018, 240),
		Border:  OKLCH(.90, .008, 240),
		Accent:  OKLCH(.58, .16, 145),
		Ok:      OKLCH(.45, .12, 150),
		Warn:    OKLCH(.47, .11, 65),
		Error:   OKLCH(.49, .17, 25),
		Hover:   OKLCH(.94, .005, 250),
		Inset:   OKLCH(.96, .005, 250),
	}
	Dark = Palette{
		Bg:      OKLCH(.16, .009, 250),
		Surface: OKLCH(.20, .009, 250),
		Fg:      OKLCH(.92, .006, 250),
		Muted:   OKLCH(.68, .014, 250),
		Border:  OKLCH(.30, .012, 250),
		Accent:  OKLCH(.72, .14, 150),
		Ok:      OKLCH(.66, .12, 150),
		Warn:    OKLCH(.76, .13, 78),
		Error:   OKLCH(.67, .17, 25),
		Hover:   OKLCH(.27, .012, 250),
		Inset:   OKLCH(.18, .009, 250),
	}
)

var (
	hexColour   = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	oklchColour = regexp.MustCompile(`^oklch\(\s*(\S+)\s+(\S+)\s+(\S+)\s*\)$`)
	decimal     = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)$`)
)

// Parse reads a colour written as CSS hex, #rgb or #rrggbb, or as
// oklch(L C H): lightness from 0 to 1 or 0% to 100%, chroma from 0
// to 1, and hue in degrees. Colours outside sRGB are clamped into it.
func Parse(s string) (color.NRGBA, error) {
	if hexColour.MatchString(s) {
		h := s[1:]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		v, _ := strconv.ParseUint(h, 16, 32)
		return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}, nil
	}
	m := oklchColour.FindStringSubmatch(s)
	if m == nil {
		return color.NRGBA{}, fmt.Errorf("%q is not a colour: want #rgb, #rrggbb, or oklch(L C H)", s)
	}
	lText, percent := strings.CutSuffix(m[1], "%")
	l, err := number(lText)
	if err == nil && percent {
		l /= 100
	}
	if err != nil || l < 0 || l > 1 {
		return color.NRGBA{}, fmt.Errorf("%q: lightness must be from 0 to 1, or 0%% to 100%%", s)
	}
	// Chroma past 0.4 or so is outside sRGB already; the bound keeps the
	// conversion's arithmetic finite.
	c, err := number(m[2])
	if err != nil || c < 0 || c > 1 {
		return color.NRGBA{}, fmt.Errorf("%q: chroma must be from 0 to 1", s)
	}
	h, err := number(m[3])
	if err != nil {
		return color.NRGBA{}, fmt.Errorf("%q: hue must be a number of degrees", s)
	}
	// Taken into one turn first, so a huge hue does not overflow in
	// radians.
	return OKLCH(l, c, math.Mod(h, 360)), nil
}

// number reads a plain decimal, which rules out ParseFloat's Inf, NaN,
// hex, and exponents.
func number(s string) (float64, error) {
	if !decimal.MatchString(s) {
		return 0, fmt.Errorf("%q is not a decimal number", s)
	}
	return strconv.ParseFloat(s, 64)
}

// OKLCH converts a CSS oklch() colour (lightness 0–1, chroma, hue in
// degrees) to sRGB, clamping colours outside the gamut.
func OKLCH(l, c, h float64) color.NRGBA {
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

// Contrast is WCAG 2's contrast ratio between two opaque colours, from
// 1 (none) to 21 (black on white).
func Contrast(a, b color.NRGBA) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// luminance is WCAG 2's relative luminance of an sRGB colour.
func luminance(c color.NRGBA) float64 {
	return 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
}

func linear(v uint8) float64 {
	x := float64(v) / 255
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

// minContrast is the least contrast each colour drawn as text needs
// against the backgrounds text sits on: WCAG AA's 4.5:1 for body text,
// and its 3:1 for the accent, which mostly draws focus rings and
// highlights.
var minContrast = []struct {
	token string
	min   float64
}{{"fg", 4.5}, {"muted", 4.5}, {"ok", 4.5}, {"warn", 4.5}, {"error", 4.5}, {"accent", 3}}

// Shortfall is a text colour with too little contrast against a
// background.
type Shortfall struct {
	Text, On   string // token names
	Ratio, Min float64
}

func (s Shortfall) String() string {
	return fmt.Sprintf("%s on %s has contrast %.2f:1, below %g:1", s.Text, s.On, s.Ratio, s.Min)
}

// Shortfalls lists the text colours whose contrast against bg or
// surface is too low to read easily.
func (p *Palette) Shortfalls() []Shortfall {
	var out []Shortfall
	for _, m := range minContrast {
		for _, on := range []string{"bg", "surface"} {
			// Rounded as shown, so a ratio reported as 4.50 is not below 4.5.
			r := math.Floor(Contrast(*p.Token(m.token), *p.Token(on))*100) / 100
			if r < m.min {
				out = append(out, Shortfall{Text: m.token, On: on, Ratio: r, Min: m.min})
			}
		}
	}
	return out
}
