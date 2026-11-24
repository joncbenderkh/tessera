// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/gesture"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget/material"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// Tone is the colour of a status dot.
type Tone int

const (
	ToneOk Tone = iota
	ToneWarn
	ToneError
)

// Status is the header's plugin status line.
type Status struct {
	Text string
	Tone Tone
}

// StatusOf summarises the enabled panels: how many there are and how many
// are starting, stale (or live with a view over a limit), or failing.
// Any failing panel makes the tone an error, else any starting or stale
// one a warning.
func StatusOf(states []pluginhost.PanelState) Status {
	var starting, stale, failing int
	for _, s := range states {
		switch kindOf(s) {
		case kindStarting:
			starting++
		case kindStale:
			stale++
		case kindFailing:
			failing++
		}
	}
	parts := []string{count(len(states), "panel", "panels")}
	if starting > 0 {
		parts = append(parts, fmt.Sprintf("%d starting", starting))
	}
	if stale > 0 {
		parts = append(parts, fmt.Sprintf("%d stale", stale))
	}
	if failing > 0 {
		parts = append(parts, count(failing, "error", "errors"))
	}
	s := Status{Text: strings.Join(parts, " · ")}
	switch {
	case failing > 0:
		s.Tone = ToneError
	case starting > 0 || stale > 0:
		s.Tone = ToneWarn
	}
	return s
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// attention is the panels among states that are not live: starting,
// stale, or failing, in their order.
func attention(states []pluginhost.PanelState) []pluginhost.PanelState {
	var out []pluginhost.PanelState
	for _, s := range states {
		if kindOf(s) != kindLive {
			out = append(out, s)
		}
	}
	return out
}

// StatusList is the list the header's status opens: the panels that
// need attention, each with what is wrong and its Restart and Logs, over
// everything else, which a click outside it or Escape closes.
type StatusList struct {
	open    bool
	focus   bool // move the focus into the list on the next frame
	restart map[string]*Button
	logs    map[string]*Button
	outside gesture.Click
	inside  gesture.Click
	list    layout.List
	box     image.Rectangle // where it was drawn; for tests
}

// Toggle opens the list, or closes it; keyed moves the focus into it.
func (l *StatusList) Toggle(keyed bool) {
	l.open = !l.open
	l.focus = l.open && keyed
	if l.open {
		l.list.ScrollTo(0)
	}
}

// Close closes the list.
func (l *StatusList) Close() { l.open = false }

// IsOpen reports whether the list is showing.
func (l *StatusList) IsOpen() bool { return l.open }

// Layout draws the list, if it is open, below anchor, where the status
// was drawn, and returns the actions chosen: a panel's Restart or Logs.
// Logs closes it, as the logs open in a dialog; refocus, if set, takes
// the focus back when Escape closes it.
func (l *StatusList) Layout(gtx layout.Context, th *theme.Theme, states []pluginhost.PanelState, anchor image.Rectangle, refocus *Button) []Action {
	l.box = image.Rectangle{}
	if !l.open {
		return nil
	}
	if !gtx.Enabled() {
		// A dialog is open; the list's backdrop would take its clicks.
		l.open = false
		return nil
	}
	panels := attention(states)
	if l.restart == nil {
		l.restart, l.logs = map[string]*Button{}, map[string]*Button{}
	}
	var actions []Action
	for _, s := range panels {
		for _, b := range []struct {
			m    map[string]*Button
			kind ActionKind
		}{{l.restart, ActionRestart}, {l.logs, ActionLogs}} {
			if b.m[s.ID] == nil {
				b.m[s.ID] = new(Button)
			}
			if b.m[s.ID].Clicked(gtx) {
				actions = append(actions, Action{Kind: b.kind, Panel: s.ID})
				if b.kind == ActionLogs {
					l.open = false
				}
			}
		}
	}
	for {
		if _, ok := l.inside.Update(gtx.Source); !ok {
			break
		}
	}
	for {
		e, ok := l.outside.Update(gtx.Source)
		if !ok {
			break
		}
		if e.Kind == gesture.KindClick {
			l.open = false
		}
	}
	for {
		e, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := e.(key.Event); ok && e.State == key.Press {
			l.open = false
			if refocus != nil {
				gtx.Execute(key.FocusCmd{Tag: &refocus.Clickable})
			}
		}
	}
	if !l.open {
		return actions
	}

	pop := op.Record(gtx.Ops)
	defer func() { op.Defer(gtx.Ops, pop.Stop()) }()
	far := image.Rectangle{Min: image.Pt(-1<<20, -1<<20), Max: image.Pt(1<<20, 1<<20)}
	backdrop := clip.Rect(far).Push(gtx.Ops)
	l.outside.Add(gtx.Ops)
	backdrop.Pop()
	modalLayer(gtx, far)

	pad := gtx.Dp(theme.Space12)
	area := gtx.Constraints.Max
	width := max(min(gtx.Dp(360), area.X-2*gtx.Dp(theme.Space8)), 0)
	inner := max(width-2*pad, 0)
	igtx := gtx
	igtx.Constraints = layout.Constraints{Max: image.Pt(inner, 1<<20)}
	var rows []recorded
	if len(panels) == 0 {
		rows = append(rows, record(igtx, label(th, "Every panel is running.", theme.TextSmall, th.Muted, theme.Body).Layout))
	}
	for i, s := range panels {
		if i == 0 && l.focus {
			first := l.logs[s.ID]
			if kindOf(s) != kindStarting {
				first = l.restart[s.ID]
			}
			gtx.Execute(key.FocusCmd{Tag: &first.Clickable})
		}
		rows = append(rows, record(igtx, func(gtx layout.Context) layout.Dimensions {
			return l.row(gtx, th, s)
		}))
	}
	l.focus = false
	height := 0
	gap := 2*gtx.Dp(theme.Space12) + gtx.Dp(1) // a divider between rows
	for i, r := range rows {
		if i > 0 {
			height += gap
		}
		height += r.dims.Size.Y
	}

	box := image.Rectangle{Max: image.Pt(width, min(height+2*pad, area.Y))}
	at := image.Pt(anchor.Max.X-box.Dx(), anchor.Max.Y+gtx.Dp(4))
	at.X = max(min(at.X, area.X-box.Dx()), 0)
	at.Y = max(min(at.Y, area.Y-box.Dy()), 0)
	l.box = box.Add(at)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rr := gtx.Dp(theme.RadiusSmall)
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(box, rr).Op(gtx.Ops))
	strokeRRect(gtx, box, rr, gtx.Dp(1), th.Border)
	card := clip.Rect(box).Push(gtx.Ops)
	l.inside.Add(gtx.Ops)
	card.Pop()
	defer op.Offset(image.Pt(pad, pad)).Push(gtx.Ops).Pop()
	lgtx := gtx
	lgtx.Constraints = layout.Constraints{Min: image.Pt(inner, 0), Max: image.Pt(inner, max(box.Dy()-2*pad, 0))}
	l.list.Axis = layout.Vertical
	defer viewport(gtx, image.Rectangle{Max: lgtx.Constraints.Max})()
	l.list.Layout(lgtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		if i > 0 {
			line := gtx.Dp(theme.Space12)
			fillRect(gtx, image.Rect(0, line, inner, line+gtx.Dp(1)), th.Border)
			defer op.Offset(image.Pt(0, gap)).Push(gtx.Ops).Pop()
		}
		// Recorded above at this width; laid out again here, so its
		// buttons are live where they show.
		var dims layout.Dimensions
		if len(panels) == 0 {
			dims = label(th, "Every panel is running.", theme.TextSmall, th.Muted, theme.Body).Layout(gtx)
		} else {
			dims = l.row(gtx, th, panels[i])
		}
		if i > 0 {
			dims.Size.Y += gap
		}
		return dims
	})
	return actions
}

// row is one panel in the list: its title, its plugin and what is
// wrong, and its Restart (unless it is starting) and Logs.
func (l *StatusList) row(gtx layout.Context, th *theme.Theme, s pluginhost.PanelState) layout.Dimensions {
	width := gtx.Constraints.Max.X
	gtx.Constraints.Min = image.Point{} // the buttons as wide as their labels
	name := title(s)
	var state, detail string
	tone := th.Warn
	switch kindOf(s) {
	case kindStarting:
		state = "starting"
	case kindStale:
		state = "stale"
		if s.ViewErr != nil {
			detail = s.ViewErr.Error()
		}
	case kindFailing:
		state, tone = "error", th.Error
		detail = errorText(s)
	}
	y := 0
	head := recordLabel(gtx, th, name, theme.TextSmall, th.Fg, theme.Body)
	head.at(gtx, image.Pt(0, y))
	y += head.dims.Size.Y
	sub := recordLabel(gtx, th, s.Plugin+" · "+state, theme.TextMin, tone, theme.Mono)
	sub.at(gtx, image.Pt(0, y))
	y += sub.dims.Size.Y
	if detail != "" {
		msg := record(gtx, material.LabelStyle{
			Text: cut(detail, maxText), TextSize: theme.TextMin, Color: th.Muted, Font: theme.Mono,
			MaxLines: 2, Shaper: th.Shaper,
		}.Layout)
		msg.at(gtx, image.Pt(0, y))
		y += msg.dims.Size.Y
	}
	y += gtx.Dp(theme.Space8)
	x := 0
	if kindOf(s) != kindStarting {
		r := l.restart[s.ID]
		r.Description = "Restart " + name
		b := record(gtx, func(gtx layout.Context) layout.Dimensions { return r.Layout(gtx, th, "Restart") })
		b.at(gtx, image.Pt(x, y))
		x += b.dims.Size.X + gtx.Dp(theme.Space8)
	}
	lg := l.logs[s.ID]
	lg.Description = "Logs of " + name
	b := record(gtx, func(gtx layout.Context) layout.Dimensions { return lg.Layout(gtx, th, "Logs") })
	b.at(gtx, image.Pt(x, y))
	y += b.dims.Size.Y
	return layout.Dimensions{Size: image.Pt(width, y)}
}
