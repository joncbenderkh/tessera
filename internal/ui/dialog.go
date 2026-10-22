// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"strings"

	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget/material"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// Dialog is a modal message: a title, a subtitle, an optional monospaced
// body such as a log, and a Close button. It closes on Close, Escape, or
// a click outside it. While it is open, the caller lays out the rest of
// the window with gtx.Disabled(), so nothing behind it takes input.
type Dialog struct {
	Title, Subtitle, Body string

	lines    []string // Body's lines
	list     layout.List
	open     bool
	focus    bool // move the focus to Close on the next frame
	close    Button
	backdrop gesture.Click // not a button: Tab must not reach it
}

// Open shows the dialog with the given text.
func (d *Dialog) Open(title, subtitle, body string) {
	d.Title, d.Subtitle, d.Body = title, subtitle, body
	d.lines = nil
	if body != "" {
		d.lines = strings.Split(body, "\n")
	}
	// Opened at the end, where a log's newest lines are.
	d.list = layout.List{Axis: layout.Vertical, Position: layout.Position{First: len(d.lines)}}
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
	if d.close.Clicked(gtx) {
		d.open = false
	}
	for {
		e, ok := d.backdrop.Update(gtx.Source)
		if !ok {
			break
		}
		if e.Kind == gesture.KindClick {
			d.open = false
		}
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
	fillRect(gtx, image.Rectangle{Max: area}, theme.Scrim)
	backdrop := clip.Rect{Max: area}.Push(gtx.Ops)
	d.backdrop.Add(gtx.Ops)
	backdrop.Pop()

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
// Close's row is laid out first and always shown; the headings get the
// height left, clipped if they need more, and the body what remains
// after them, scrolling if it needs more.
func (d *Dialog) content(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	buttons := record(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.close.Layout(gtx, th, "Close")
			})
		})
	})
	avail := max(gtx.Constraints.Max.Y-buttons.dims.Size.Y, 0)
	headings := record(gtx, func(gtx layout.Context) layout.Dimensions {
		title := label(th, d.Title, theme.TextDialog, th.Fg, font.Font{Typeface: theme.Body.Typeface, Weight: font.Bold})
		title.MaxLines = 2
		children := []layout.FlexChild{layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 10}.Layout(gtx, title.Layout)
		})}
		if d.Subtitle != "" {
			sub := label(th, d.Subtitle, theme.TextSmall, th.Muted, theme.Body)
			sub.MaxLines = 3
			children = append(children, layout.Rigid(sub.Layout))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
	headings.dims.Size.Y = min(headings.dims.Size.Y, avail)

	var size image.Point
	place := func(r recorded) {
		r.at(gtx, image.Pt(0, size.Y))
		size.X = max(size.X, r.dims.Size.X)
		size.Y += r.dims.Size.Y
	}
	clipped := clip.Rect{Max: headings.dims.Size}.Push(gtx.Ops)
	place(headings)
	clipped.Pop()
	if d.Body != "" {
		body := gtx
		body.Constraints.Max.Y = avail - headings.dims.Size.Y
		place(record(body, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return d.body(gtx, th)
			})
		}))
	}
	place(buttons)
	return layout.Dimensions{Size: size}
}

// body is the monospaced text on an inset panel. A body taller than
// the dialog scrolls.
func (d *Dialog) body(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	pad := gtx.Dp(15)
	inner := gtx
	inner.Constraints = layout.Constraints{Max: gtx.Constraints.Max.Sub(image.Pt(2*pad, 2*pad))}
	inner.Constraints.Min.X = inner.Constraints.Max.X
	list := record(inner, func(gtx layout.Context) layout.Dimensions {
		return d.list.Layout(gtx, len(d.lines), func(gtx layout.Context, i int) layout.Dimensions {
			return material.LabelStyle{
				Text: d.lines[i], TextSize: theme.TextMin, LineHeightScale: 1.8, Color: th.Fg,
				Font: theme.Mono, Shaper: th.Shaper, WrapPolicy: text.WrapGraphemes,
			}.Layout(gtx)
		})
	})
	size := list.dims.Size.Add(image.Pt(2*pad, 2*pad))
	r := image.Rectangle{Max: size}
	paint.FillShape(gtx.Ops, th.Inset, clip.UniformRRect(r, gtx.Dp(theme.RadiusSmall)).Op(gtx.Ops))
	list.at(gtx, image.Pt(pad, pad))
	return layout.Dimensions{Size: size}
}
