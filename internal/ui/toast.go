// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"slices"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget/material"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// ToastFor is how long a toast stays up.
const ToastFor = 2800 * time.Millisecond

// ActionFor is how long a toast with an action, such as Undo, stays up.
const ActionFor = 8 * time.Second

// Toaster shows one short message at a time at the bottom of the window.
// A new message waits for the ones before it to go, but no longer than
// ToastFor behind one with an action.
type Toaster struct {
	toast           // the message showing, if text is not ""
	until time.Time // when it goes
	fresh bool      // it was replaced; its clock starts at the next frame
	next  []toast   // waiting, oldest first
	act   Button    // the action of the toast showing
	// Inert keeps the action from taking input, as while a dialog is
	// open: Acted then reads none either.
	Inert bool
	// actBox is where the action was drawn; for tests.
	actBox image.Rectangle
}

// toast is a message, the kind it is one of, if any, and the label of
// its action and what it acts on, if it has one.
type toast struct{ kind, text, action, tag string }

// lasts is how long the toast stays up.
func (t toast) lasts() time.Duration {
	if t.action != "" {
		return ActionFor
	}
	return ToastFor
}

// Show puts text up for ToastFor, from the next frame or once the
// messages before it have gone; a message waiting already is not
// repeated. The caller must invalidate the window if Show is not called
// from a frame.
func (t *Toaster) Show(text string) {
	if !slices.Contains(t.next, toast{text: text}) {
		t.next = append(t.next, toast{text: text})
	}
}

// ShowAction is Show for a message with an action button labelled
// action, which stays up for ActionFor. Acted reports a click on it,
// with tag. It takes the place of a message with an action that is
// showing or waiting, so the latest action is the one offered, and
// otherwise goes ahead of the messages waiting, so it comes while what
// it would undo is fresh.
func (t *Toaster) ShowAction(text, action, tag string) {
	n := toast{text: text, action: action, tag: tag}
	t.next = slices.DeleteFunc(t.next, func(w toast) bool { return w.action != "" })
	if t.text != "" && t.action != "" {
		t.toast, t.fresh = n, true
		return
	}
	t.next = slices.Insert(t.next, 0, n)
}

// Acted reports the tag of the toast whose action was clicked, and takes
// that toast down. Call it before Layout in a frame.
func (t *Toaster) Acted(gtx layout.Context) (string, bool) {
	if t.text == "" || t.action == "" || !t.act.Clicked(gtx) {
		return "", false
	}
	tag := t.tag
	t.toast = toast{}
	return tag, true
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
	t.next = append(t.next, toast{kind: kind, text: text})
}

// Drop takes away the message of kind, showing or waiting, as when what
// it reported is over.
func (t *Toaster) Drop(kind string) {
	if t.kind == kind {
		t.toast = toast{}
	}
	t.next = slices.DeleteFunc(t.next, func(n toast) bool { return n.kind == kind })
}

// DropAction takes away the message with an action on tag, showing or
// waiting, as when what the action would undo is undone already.
func (t *Toaster) DropAction(tag string) {
	if t.action != "" && t.tag == tag {
		t.toast = toast{}
	}
	t.next = slices.DeleteFunc(t.next, func(n toast) bool { return n.action != "" && n.tag == tag })
}

// Text is the message showing, or "".
func (t *Toaster) Text() string { return t.text }

// Layout draws the toast over gtx's constraints, centred 24 dp above
// the bottom edge, and asks for a frame when it is due to go.
func (t *Toaster) Layout(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	if t.fresh {
		t.fresh, t.until = false, gtx.Now.Add(t.lasts())
	}
	if t.text != "" && !gtx.Now.Before(t.until) {
		t.toast = toast{}
	}
	if t.text == "" {
		if len(t.next) == 0 {
			return layout.Dimensions{}
		}
		t.toast = t.next[0]
		t.until = gtx.Now.Add(t.lasts())
		t.next = t.next[1:]
	}
	if t.action != "" && len(t.next) > 0 {
		t.until = minTime(t.until, gtx.Now.Add(ToastFor))
	}
	gtx.Execute(op.InvalidateCmd{At: t.until})

	area := gtx.Constraints.Max
	padX, padY := gtx.Dp(18), gtx.Dp(12)
	var act recorded
	gap := 0
	if t.action != "" {
		t.act.Inverse = true
		agtx := gtx
		if t.Inert {
			agtx = gtx.Disabled()
		}
		agtx.Constraints.Min = image.Point{}
		act = record(agtx, func(gtx layout.Context) layout.Dimensions { return t.act.Layout(gtx, th, t.action) })
		gap = gtx.Dp(theme.Space12)
	}
	lbl := gtx
	lbl.Constraints = layout.Constraints{Max: image.Pt(max(area.X*9/10-2*padX-gap-act.dims.Size.X, 0), area.Y)}
	msg := record(lbl, material.LabelStyle{
		Text: t.text, TextSize: theme.TextSmall, Color: th.Bg, Font: theme.Body, Shaper: th.Shaper,
	}.Layout)
	inner := image.Pt(msg.dims.Size.X+gap+act.dims.Size.X, max(msg.dims.Size.Y, act.dims.Size.Y))
	size := inner.Add(image.Pt(2*padX, 2*padY))
	at := image.Pt((area.X-size.X)/2, area.Y-gtx.Dp(theme.Space24)-size.Y)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	box := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(6))
	paint.FillShape(gtx.Ops, th.Fg, box.Op(gtx.Ops))
	// The toast takes the presses on it, so they do not reach what is
	// below, such as a panel's head.
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: t, Kinds: pointer.Press | pointer.Release}); !ok {
			break
		}
	}
	hit := box.Push(gtx.Ops)
	event.Op(gtx.Ops, t)
	hit.Pop()
	msg.at(gtx, image.Pt(padX, padY+(inner.Y-msg.dims.Size.Y)/2))
	t.actBox = image.Rectangle{}
	if t.action != "" {
		p := image.Pt(padX+msg.dims.Size.X+gap, padY+(inner.Y-act.dims.Size.Y)/2)
		act.at(gtx, p)
		t.actBox = image.Rectangle{Min: p, Max: p.Add(act.dims.Size)}.Add(at)
	}
	return layout.Dimensions{Size: area}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
