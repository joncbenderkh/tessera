// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"image/color"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// Button is the mockups' button: a bordered label that fills on hover,
// shows a focus ring, and activates on click, Return, or Space.
type Button struct {
	widget.Clickable
	// Toggle makes it a toggle button, and Pressed draws it inverted, as
	// a toggle that is on.
	Toggle, Pressed bool
	// Icon makes it a square 32 dp icon button with larger text.
	Icon bool
	// Bare drops the border, as the tile menu and its items have, and
	// sets the text at the start. A bare icon button is 26 dp and Muted.
	Bare bool
	// Description is what the button does, for screen readers; an icon
	// button's glyph does not say.
	Description string
	// Draw, if set, draws the icon in place of the label, in a square
	// of the given side.
	Draw func(gtx layout.Context, side int, c color.NRGBA)
}

// Layout draws the button with label as its text.
func (b *Button) Layout(gtx layout.Context, th *theme.Theme, label string) layout.Dimensions {
	minWidth := gtx.Constraints.Min.X
	dims := b.Clickable.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		fg, bg := th.Fg, color.NRGBA{}
		if b.Bare && b.Icon {
			fg = th.Muted
		}
		switch {
		case b.Pressed:
			fg, bg = th.Bg, th.Fg
		case b.Hovered():
			bg = th.Hover
		}
		size, pad := theme.TextBase, unit.Dp(11)
		if b.Icon {
			size, pad = theme.TextIcon, 0
		}
		gtx.Constraints.Min = image.Point{}
		text := recordLabel(gtx, th, label, size, fg, theme.Body)
		if b.Draw != nil {
			side := gtx.Sp(size)
			text = record(gtx, func(gtx layout.Context) layout.Dimensions {
				b.Draw(gtx, side, fg)
				return layout.Dimensions{Size: image.Pt(side, side)}
			})
		}
		// 32 dp, or more if a large text scale needs it.
		side := unit.Dp(32)
		if b.Bare && b.Icon {
			side = 26
		}
		height := max(gtx.Dp(side), text.dims.Size.Y+gtx.Dp(theme.Space8))
		width := max(text.dims.Size.X+2*gtx.Dp(pad), height, minWidth)
		if b.Icon {
			width = height
		}
		bounds := image.Rectangle{Max: image.Pt(width, height)}
		semantic.Button.Add(gtx.Ops)
		if b.Toggle {
			semantic.SelectedOp(b.Pressed).Add(gtx.Ops)
		}
		if b.Description != "" {
			semantic.DescriptionOp(b.Description).Add(gtx.Ops)
		}
		rr := gtx.Dp(theme.RadiusSmall)
		paint.FillShape(gtx.Ops, bg, clip.UniformRRect(bounds, rr).Op(gtx.Ops))
		at := bounds.Max.Sub(text.dims.Size).Div(2)
		if b.Bare && !b.Icon {
			at.X = gtx.Dp(pad)
		} else if !b.Bare {
			strokeRRect(gtx, bounds, rr, gtx.Dp(1), th.Border)
		}
		text.at(gtx, at)
		return layout.Dimensions{Size: bounds.Max}
	})
	if gtx.Focused(&b.Clickable) {
		// A 2 dp ring 3 dp outside the border, as CSS outline-offset.
		ring, off := gtx.Dp(2), gtx.Dp(3)
		bounds := image.Rectangle{Max: dims.Size}.Inset(-off - ring)
		strokeRRect(gtx, bounds, gtx.Dp(theme.RadiusSmall)+off+ring, ring, th.Accent)
	}
	return dims
}
