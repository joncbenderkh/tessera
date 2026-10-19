// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"image/color"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// Window widths, in dp, at or below which the chrome tightens: Compact
// drops the plugin status, Narrow the layout selector.
const (
	Compact unit.Dp = 1000
	Narrow  unit.Dp = 560
)

// HeaderHeight is the height of the window header.
const HeaderHeight unit.Dp = 58

// LayoutName is the layout selector's one entry until the config has
// layout profiles.
const LayoutName = "Default"

// Header is the window header: the app name, the layout selector, the
// plugin status, and the Edit layout and theme toggles. The caller reads
// the toggles' clicks and sets Edit.Pressed.
type Header struct {
	Edit  Button
	Theme Button
}

// headerFit is what the header shows, and its spacing, at a window
// width.
type headerFit struct {
	status, selector bool
	pad, gap, endGap unit.Dp
}

func fitHeader(width unit.Dp) headerFit {
	f := headerFit{status: true, selector: true, pad: 28, gap: 24, endGap: 14}
	if width <= Compact {
		f.status, f.endGap = false, 7
	}
	if width <= Narrow {
		f.selector, f.pad, f.gap = false, 12, 8
	}
	return f
}

// Layout draws the header across the top of gtx's constraints.
func (h *Header) Layout(gtx layout.Context, th *theme.Theme, status Status) layout.Dimensions {
	fit := fitHeader(widthDp(gtx))
	pad, gap, endGap := fit.pad, fit.gap, fit.endGap
	size := image.Pt(gtx.Constraints.Max.X, gtx.Dp(HeaderHeight))
	fillRect(gtx, image.Rectangle{Max: size}, th.Surface)
	fillRect(gtx, image.Rect(0, size.Y-gtx.Dp(1), size.X, size.Y), th.Border)

	gtx.Constraints = layout.Exact(size)
	gtx.Constraints.Min = image.Point{}
	middle := func(r recorded) int { return (size.Y - r.dims.Size.Y) / 2 }

	x := gtx.Dp(pad)
	brand := record(gtx, func(gtx layout.Context) layout.Dimensions { return h.brand(gtx, th) })
	brand.at(gtx, image.Pt(x, middle(brand)))
	x += brand.dims.Size.X + gtx.Dp(gap)
	if fit.selector {
		divider := gtx.Dp(19)
		fillRect(gtx, image.Rect(x, (size.Y-divider)/2, x+gtx.Dp(1), (size.Y+divider)/2), th.Border)
		x += gtx.Dp(1) + gtx.Dp(gap)
		sel := record(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(theme.Space8).Layout(gtx, label(th, LayoutName, theme.TextSmall, th.Fg, theme.Body).Layout)
		})
		sel.at(gtx, image.Pt(x, middle(sel)))
	}

	var end []recorded
	if fit.status {
		end = append(end, record(gtx, func(gtx layout.Context) layout.Dimensions { return statusLine(gtx, th, status) }))
	}
	h.Theme.Icon = true
	glyph := "◐"
	if th.Dark {
		glyph = "☼"
	}
	end = append(end,
		record(gtx, func(gtx layout.Context) layout.Dimensions { return h.Edit.Layout(gtx, th, "Edit layout") }),
		record(gtx, func(gtx layout.Context) layout.Dimensions { return h.Theme.Layout(gtx, th, glyph) }),
	)
	ex := size.X - gtx.Dp(pad)
	for i := len(end) - 1; i >= 0; i-- {
		ex -= end[i].dims.Size.X
		end[i].at(gtx, image.Pt(ex, middle(end[i])))
		ex -= gtx.Dp(endGap)
	}
	return layout.Dimensions{Size: size}
}

// brand is the tessera mark, a 2×2 grid of squares, and the app name.
func (h *Header) brand(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	name := recordLabel(gtx, th, "tessera", theme.TextBrand, th.Fg, font.Font{Typeface: theme.Body.Typeface, Weight: 250})
	cell, gap := gtx.Dp(5), gtx.Dp(3)
	mark := 2*cell + gap
	top := (name.dims.Size.Y - mark) / 2
	for i := range 4 {
		p := image.Pt(i%2*(cell+gap), top+i/2*(cell+gap))
		paint.FillShape(gtx.Ops, th.Fg, clip.UniformRRect(image.Rectangle{Min: p, Max: p.Add(image.Pt(cell, cell))}, gtx.Dp(1)).Op(gtx.Ops))
	}
	name.at(gtx, image.Pt(mark+gtx.Dp(9), 0))
	return layout.Dimensions{Size: image.Pt(mark+gtx.Dp(9)+name.dims.Size.X, name.dims.Size.Y)}
}

// statusLine is a tone dot and the status text.
func statusLine(gtx layout.Context, th *theme.Theme, s Status) layout.Dimensions {
	text := recordLabel(gtx, th, s.Text, theme.TextMin, th.Muted, theme.Body)
	dot := gtx.Dp(6)
	top := (text.dims.Size.Y - dot) / 2
	r := image.Rect(0, top, dot, top+dot)
	paint.FillShape(gtx.Ops, toneColor(th, s.Tone), clip.Ellipse(r).Op(gtx.Ops))
	text.at(gtx, image.Pt(dot+gtx.Dp(7), 0))
	return layout.Dimensions{Size: image.Pt(dot+gtx.Dp(7)+text.dims.Size.X, text.dims.Size.Y)}
}

func toneColor(th *theme.Theme, t Tone) color.NRGBA {
	switch t {
	case ToneWarn:
		return th.Warn
	case ToneError:
		return th.Error
	}
	return th.Ok
}
