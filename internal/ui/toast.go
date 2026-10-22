// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget/material"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// ToastFor is how long a toast stays up.
const ToastFor = 2800 * time.Millisecond

// Toaster shows one short message at a time at the bottom of the window.
// A new message replaces the one showing.
type Toaster struct {
	text    string
	pending bool      // shown by the next frame, which starts its clock
	until   time.Time // when the message goes
}

// Show puts text up for ToastFor from the next frame. The caller must
// invalidate the window if Show is not called from a frame.
func (t *Toaster) Show(text string) {
	t.text, t.pending = text, true
}

// Text is the message showing, or "".
func (t *Toaster) Text() string { return t.text }

// Layout draws the toast over gtx's constraints, centred 24 dp above
// the bottom edge, and asks for a frame when it is due to go.
func (t *Toaster) Layout(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	if t.pending {
		t.pending, t.until = false, gtx.Now.Add(ToastFor)
	}
	if t.text == "" {
		return layout.Dimensions{}
	}
	if !gtx.Now.Before(t.until) {
		t.text = ""
		return layout.Dimensions{}
	}
	gtx.Execute(op.InvalidateCmd{At: t.until})

	area := gtx.Constraints.Max
	padX, padY := gtx.Dp(18), gtx.Dp(12)
	lbl := gtx
	lbl.Constraints = layout.Constraints{Max: image.Pt(area.X*9/10-2*padX, area.Y)}
	msg := record(lbl, material.LabelStyle{
		Text: t.text, TextSize: theme.TextSmall, Color: th.Bg, Font: theme.Body, Shaper: th.Shaper,
	}.Layout)
	size := msg.dims.Size.Add(image.Pt(2*padX, 2*padY))
	at := image.Pt((area.X-size.X)/2, area.Y-gtx.Dp(theme.Space24)-size.Y)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.Fg, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(6)).Op(gtx.Ops))
	msg.at(gtx, image.Pt(padX, padY))
	return layout.Dimensions{Size: area}
}
