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

// HeaderHeight is the window header's height at normal text sizes.
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

	placed []image.Rectangle // the items' bounds in the last Layout
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

// Layout draws the header across the top of gtx's constraints. It is
// HeaderHeight tall, or taller if a large text scale needs it. When its
// items do not fit across, it drops, in turn, the layout selector, the
// plugin status, the app name (keeping the mark), and Edit's label
// (keeping an icon).
func (h *Header) Layout(gtx layout.Context, th *theme.Theme, status Status) layout.Dimensions {
	fit := fitHeader(widthDp(gtx))
	pad, gap, endGap := gtx.Dp(fit.pad), gtx.Dp(fit.gap), gtx.Dp(fit.endGap)
	width := gtx.Constraints.Max.X
	gtx.Constraints = layout.Constraints{Max: image.Pt(width, gtx.Constraints.Max.Y)}

	// Edit becomes a toggle when edit mode exists (#15).
	h.Theme.Icon = true
	editLabel := "Edit layout"
	if h.Edit.Pressed {
		editLabel = "Done editing"
	}
	// The Go fonts have ☼ but no ◐, so the half disc is drawn.
	glyph := ""
	h.Theme.Draw = halfDisc
	h.Theme.Description = "Switch to dark theme"
	if th.Dark {
		glyph, h.Theme.Draw = "☼", nil
		h.Theme.Description = "Switch to light theme"
	}

	// Each variant is laid out; only the ones placed are drawn.
	brandFull := record(gtx, func(gtx layout.Context) layout.Dimensions { return brand(gtx, th, true) })
	brandMark := record(gtx, func(gtx layout.Context) layout.Dimensions { return brand(gtx, th, false) })
	sel := record(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.UniformInset(theme.Space8).Layout(gtx, label(th, LayoutName, theme.TextSmall, th.Fg, theme.Body).Layout)
	})
	stat := record(gtx, func(gtx layout.Context) layout.Dimensions { return statusLine(gtx, th, status) })
	h.Edit.Icon, h.Edit.Draw, h.Edit.Description = false, nil, ""
	editText := record(gtx, func(gtx layout.Context) layout.Dimensions { return h.Edit.Layout(gtx, th, editLabel) })
	h.Edit.Icon, h.Edit.Draw, h.Edit.Description = true, gridIcon, editLabel
	editIcon := record(gtx, func(gtx layout.Context) layout.Dimensions { return h.Edit.Layout(gtx, th, "") })
	themeBtn := record(gtx, func(gtx layout.Context) layout.Dimensions { return h.Theme.Layout(gtx, th, glyph) })

	showSel, showStatus, showName, showEditText := fit.selector, fit.status, true, true
	var left, end []recorded
	divider := gtx.Dp(1)
	for {
		left = []recorded{brandMark}
		if showName {
			left[0] = brandFull
		}
		if showSel {
			left = append(left, sel)
		}
		end = nil
		if showStatus {
			end = append(end, stat)
		}
		if showEditText {
			end = append(end, editText)
		} else {
			end = append(end, editIcon)
		}
		end = append(end, themeBtn)
		need := 2*pad + gap + left[0].dims.Size.X
		if showSel {
			need += 2*gap + divider + sel.dims.Size.X
		}
		for i, r := range end {
			need += r.dims.Size.X
			if i > 0 {
				need += endGap
			}
		}
		switch {
		case need <= width:
		case showSel:
			showSel = false
			continue
		case showStatus:
			showStatus = false
			continue
		case showName:
			showName = false
			continue
		case showEditText:
			showEditText = false
			continue
		}
		break
	}

	height := gtx.Dp(HeaderHeight)
	for _, r := range append(left, end...) {
		height = max(height, r.dims.Size.Y+2*gtx.Dp(theme.Space12))
	}
	size := image.Pt(width, height)
	fillRect(gtx, image.Rectangle{Max: size}, th.Surface)
	fillRect(gtx, image.Rect(0, size.Y-gtx.Dp(1), size.X, size.Y), th.Border)
	middle := func(r recorded) int { return (size.Y - r.dims.Size.Y) / 2 }

	h.placed = h.placed[:0]
	place := func(r recorded, x int) {
		p := image.Pt(x, middle(r))
		r.at(gtx, p)
		h.placed = append(h.placed, image.Rectangle{Min: p, Max: p.Add(r.dims.Size)})
	}
	x := pad
	place(left[0], x)
	x += left[0].dims.Size.X + gap
	if showSel {
		d := gtx.Dp(19)
		fillRect(gtx, image.Rect(x, (size.Y-d)/2, x+divider, (size.Y+d)/2), th.Border)
		x += divider + gap
		place(sel, x)
	}
	ex := size.X - pad
	for i := len(end) - 1; i >= 0; i-- {
		ex -= end[i].dims.Size.X
		place(end[i], ex)
		ex -= endGap
	}
	return layout.Dimensions{Size: size}
}

// brand is the tessera mark, a 2×2 grid of squares, and, if name is
// set, the app name.
func brand(gtx layout.Context, th *theme.Theme, name bool) layout.Dimensions {
	text := recordLabel(gtx, th, "tessera", theme.TextBrand, th.Fg, font.Font{Typeface: theme.Body.Typeface, Weight: 250})
	cell, gap := gtx.Dp(5), gtx.Dp(3)
	mark := 2*cell + gap
	height := max(text.dims.Size.Y, mark)
	top := (height - mark) / 2
	for i := range 4 {
		p := image.Pt(i%2*(cell+gap), top+i/2*(cell+gap))
		paint.FillShape(gtx.Ops, th.Fg, clip.UniformRRect(image.Rectangle{Min: p, Max: p.Add(image.Pt(cell, cell))}, gtx.Dp(1)).Op(gtx.Ops))
	}
	if !name {
		return layout.Dimensions{Size: image.Pt(mark, height)}
	}
	text.at(gtx, image.Pt(mark+gtx.Dp(9), 0))
	return layout.Dimensions{Size: image.Pt(mark+gtx.Dp(9)+text.dims.Size.X, height)}
}

// gridIcon is Edit's compact icon: a 2×2 grid of outlined squares, as
// panels on a grid.
func gridIcon(gtx layout.Context, side int, c color.NRGBA) {
	cell := side * 3 / 8
	gap := side - 2*cell
	line := max(gtx.Dp(1), 1)
	for i := range 4 {
		p := image.Pt(i%2*(cell+gap), i/2*(cell+gap))
		strokeRRect(gtx, image.Rectangle{Min: p, Max: p.Add(image.Pt(cell, cell))}, gtx.Dp(1), line, c)
	}
}

// halfDisc is ◐: a circle whose left half is filled.
func halfDisc(gtx layout.Context, side int, c color.NRGBA) {
	d := side * 3 / 4
	r := image.Rectangle{Max: image.Pt(d, d)}.Add(image.Pt((side-d)/2, (side-d)/2))
	strokeRRect(gtx, r, d/2, max(gtx.Dp(1), 1), c)
	left := clip.Rect(image.Rect(r.Min.X, r.Min.Y, r.Min.X+d/2, r.Max.Y)).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, c, clip.Ellipse(r).Op(gtx.Ops))
	left.Pop()
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
