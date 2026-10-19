// SPDX-License-Identifier: GPL-3.0-or-later

// Package ui draws the host-owned parts of the window: the header, and
// the toast and dialog primitives for transient host UI.
package ui

import (
	"image"

	"gioui.org/f32"
	"image/color"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// recorded is a widget laid out but not yet placed.
type recorded struct {
	call op.CallOp
	dims layout.Dimensions
}

func record(gtx layout.Context, w layout.Widget) recorded {
	m := op.Record(gtx.Ops)
	dims := w(gtx)
	return recorded{m.Stop(), dims}
}

// at draws r with its top-left corner at p.
func (r recorded) at(gtx layout.Context, p image.Point) {
	defer op.Offset(p).Push(gtx.Ops).Pop()
	r.call.Add(gtx.Ops)
}

// label is a single-line label in the given face, size, and colour.
func label(th *theme.Theme, text string, size unit.Sp, c color.NRGBA, f font.Font) material.LabelStyle {
	return material.LabelStyle{
		Text: text, TextSize: size, Color: c, Font: f,
		MaxLines: 1, Shaper: th.Shaper,
	}
}

func recordLabel(gtx layout.Context, th *theme.Theme, text string, size unit.Sp, c color.NRGBA, f font.Font) recorded {
	return record(gtx, label(th, text, size, c, f).Layout)
}

// strokeRRect outlines r, inside its edge, width px wide. The stroke is
// centred on its path, so the path runs half a width inside r.
func strokeRRect(gtx layout.Context, r image.Rectangle, radius, width int, c color.NRGBA) {
	half := float32(width) / 2
	defer op.Affine(f32.AffineId().Offset(f32.Pt(half, half))).Push(gtx.Ops).Pop()
	r.Max = r.Max.Sub(image.Pt(width, width))
	paint.FillShape(gtx.Ops, c, clip.Stroke{
		Path:  clip.UniformRRect(r, max(radius-width/2, 0)).Path(gtx.Ops),
		Width: float32(width),
	}.Op())
}

// fillRect paints r.
func fillRect(gtx layout.Context, r image.Rectangle, c color.NRGBA) {
	paint.FillShape(gtx.Ops, c, clip.Rect(r).Op())
}

// widthDp is the width the constraints allow, in dp.
func widthDp(gtx layout.Context) unit.Dp {
	return unit.Dp(float32(gtx.Constraints.Max.X) / gtx.Metric.PxPerDp)
}
