// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"slices"
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
// A new message waits for the ones before it to go.
type Toaster struct {
	toast           // the message showing, if text is not ""
	until time.Time // when it goes
	fresh bool      // it was replaced; its clock starts at the next frame
	next  []toast   // waiting, oldest first
}

// toast is a message, and the kind it is one of, if any.
type toast struct{ kind, text string }

// Show puts text up for ToastFor, from the next frame or once the
// messages before it have gone; a message waiting already is not
// repeated. The caller must invalidate the window if Show is not called
// from a frame.
func (t *Toaster) Show(text string) {
	if !slices.Contains(t.next, toast{text: text}) {
		t.next = append(t.next, toast{text: text})
	}
}

// Put shows text as the one message of its kind, which must not be "":
// it takes the place of the one showing or waiting, if there is one, and
// otherwise waits as Show's do.
func (t *Toaster) Put(kind, text string) {
	if t.text != "" && t.kind == kind {
		t.text, t.fresh = text, true
		return
	}
	for i := range t.next {
		if t.next[i].kind == kind {
			t.next[i].text = text
			return
		}
	}
	t.next = append(t.next, toast{kind, text})
}

// Drop takes away the message of kind, showing or waiting, as when what
// it reported is over.
func (t *Toaster) Drop(kind string) {
	if t.kind == kind {
		t.toast = toast{}
	}
	t.next = slices.DeleteFunc(t.next, func(n toast) bool { return n.kind == kind })
}

// Text is the message showing, or "".
func (t *Toaster) Text() string { return t.text }

// Layout draws the toast over gtx's constraints, centred 24 dp above
// the bottom edge, and asks for a frame when it is due to go.
func (t *Toaster) Layout(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	if t.fresh {
		t.fresh, t.until = false, gtx.Now.Add(ToastFor)
	}
	if t.text != "" && !gtx.Now.Before(t.until) {
		t.toast = toast{}
	}
	if t.text == "" {
		if len(t.next) == 0 {
			return layout.Dimensions{}
		}
		t.toast, t.until = t.next[0], gtx.Now.Add(ToastFor)
		t.next = t.next[1:]
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
