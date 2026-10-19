// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"image/color"

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
	// Pressed draws the button inverted, as a toggle that is on.
	Pressed bool
	// Icon makes it a square 32 dp icon button with larger text.
	Icon bool
}

// Layout draws the button with label as its text.
func (b *Button) Layout(gtx layout.Context, th *theme.Theme, label string) layout.Dimensions {
	dims := b.Clickable.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		fg, bg := th.Fg, color.NRGBA{}
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
		height := gtx.Dp(32)
		width := max(text.dims.Size.X+2*gtx.Dp(pad), height)
		if b.Icon {
			width = height
		}
		bounds := image.Rectangle{Max: image.Pt(width, height)}
		rr := gtx.Dp(theme.RadiusSmall)
		paint.FillShape(gtx.Ops, bg, clip.UniformRRect(bounds, rr).Op(gtx.Ops))
		strokeRRect(gtx, bounds, rr, gtx.Dp(1), th.Border)
		text.at(gtx, bounds.Max.Sub(text.dims.Size).Div(2))
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
