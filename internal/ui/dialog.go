// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// Dialog is a modal message: a title, a subtitle, an optional monospaced
// body such as a log, and a Close button. It closes on Close, Escape, or
// a click outside it. While it is open, the caller lays out the rest of
// the window with gtx.Disabled(), so nothing behind it takes input.
type Dialog struct {
	Title, Subtitle, Body string

	open     bool
	focus    bool // move the focus to Close on the next frame
	close    Button
	backdrop widget.Clickable
}

// Open shows the dialog with the given text.
func (d *Dialog) Open(title, subtitle, body string) {
	d.Title, d.Subtitle, d.Body = title, subtitle, body
	d.open, d.focus = true, true
}

// Close hides the dialog.
func (d *Dialog) Close() { d.open = false }

// IsOpen reports whether the dialog is showing.
func (d *Dialog) IsOpen() bool { return d.open }

// Layout draws the dialog, if open, over gtx's constraints.
func (d *Dialog) Layout(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	if !d.open {
		return layout.Dimensions{}
	}
	if d.close.Clicked(gtx) || d.backdrop.Clicked(gtx) {
		d.open = false
	}
	for {
		e, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := e.(key.Event); ok && e.State == key.Press {
			d.open = false
		}
	}
	if !d.open {
		gtx.Execute(op.InvalidateCmd{})
		return layout.Dimensions{}
	}
	if d.focus {
		d.focus = false
		gtx.Execute(key.FocusCmd{Tag: &d.close.Clickable})
	}

	area := gtx.Constraints.Max
	d.backdrop.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		fillRect(gtx, image.Rectangle{Max: area}, theme.Scrim)
		return layout.Dimensions{Size: area}
	})

	pad := gtx.Dp(theme.Space24)
	width := min(gtx.Dp(560), area.X*9/10)
	inner := gtx
	inner.Constraints = layout.Constraints{Max: image.Pt(width-2*pad, area.Y*9/10-2*pad)}
	content := record(inner, func(gtx layout.Context) layout.Dimensions { return d.content(gtx, th) })
	size := image.Pt(width, content.dims.Size.Y+2*pad)
	defer op.Offset(area.Sub(size).Div(2)).Push(gtx.Ops).Pop()
	card := image.Rectangle{Max: size}
	rr := gtx.Dp(theme.RadiusLarge)
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(card, rr).Op(gtx.Ops))
	strokeRRect(gtx, card, rr, gtx.Dp(1), th.Border)
	// The card takes the clicks on it, so they do not reach the backdrop.
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: d, Kinds: pointer.Press | pointer.Release}); !ok {
			break
		}
	}
	cardArea := clip.Rect(card).Push(gtx.Ops)
	event.Op(gtx.Ops, d)
	cardArea.Pop()
	content.at(gtx, image.Pt(pad, pad))
	return layout.Dimensions{Size: area}
}

// content is the title, subtitle, body, and buttons, top to bottom.
func (d *Dialog) content(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := label(th, d.Title, theme.TextDialog, th.Fg, font.Font{Typeface: theme.Body.Typeface, Weight: font.SemiBold})
			l.MaxLines = 0
			return layout.Inset{Bottom: 10}.Layout(gtx, l.Layout)
		}),
	}
	if d.Subtitle != "" {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			l := label(th, d.Subtitle, theme.TextSmall, th.Muted, theme.Body)
			l.MaxLines = 0
			return l.Layout(gtx)
		}))
	}
	if d.Body != "" {
		children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.body(gtx, th)
			})
		}))
	}
	children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.close.Layout(gtx, th, "Close")
			})
		})
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// body is the monospaced text on an inset panel, clipped to the space
// the dialog has.
func (d *Dialog) body(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	pad := gtx.Dp(15)
	inner := gtx
	inner.Constraints = layout.Constraints{Max: gtx.Constraints.Max.Sub(image.Pt(2*pad, 2*pad))}
	inner.Constraints.Min.X = inner.Constraints.Max.X
	text := record(inner, material.LabelStyle{
		Text: d.Body, TextSize: theme.TextMin, LineHeightScale: 1.8, Color: th.Fg,
		Font: theme.Mono, Shaper: th.Shaper, WrapPolicy: text.WrapGraphemes,
	}.Layout)
	size := text.dims.Size.Add(image.Pt(2*pad, 2*pad))
	size.Y = min(size.Y, gtx.Constraints.Max.Y)
	r := image.Rectangle{Max: size}
	paint.FillShape(gtx.Ops, th.Inset, clip.UniformRRect(r, gtx.Dp(theme.RadiusSmall)).Op(gtx.Ops))
	defer clip.Rect(r.Inset(pad)).Push(gtx.Ops).Pop()
	text.at(gtx, image.Pt(pad, pad))
	return layout.Dimensions{Size: size}
}
