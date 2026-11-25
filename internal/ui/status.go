// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"image"
	"slices"
	"strings"

	"gioui.org/gesture"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
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
	box     image.Rectangle // where it was drawn; for tests
	// Every row is drawn, cut to the card, so that the keys reach every
	// button; offset is how far the rows are scrolled up, by the wheel
	// or to show the row the focus moves to.
	wheel  gesture.Scroll
	offset int
	// shown is the panels listed in the last frame, in order, and
	// focused the row whose button had the focus then, or -1.
	shown   []string
	focused int
}

// Toggle opens the list, or closes it; keyed moves the focus into it.
func (l *StatusList) Toggle(keyed bool) {
	l.open = !l.open
	l.focus = l.open && keyed
	l.offset, l.focused = 0, -1
}

// Close closes the list.
func (l *StatusList) Close() { l.open, l.focused = false, -1 }

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
	// Only the panels listed keep their buttons. A row that goes with
	// the focus hands it to the row now in its place, or to the status.
	listed := func(id string) bool {
		return slices.ContainsFunc(panels, func(s pluginhost.PanelState) bool { return s.ID == id })
	}
	gone := -1
	for i, id := range l.shown {
		if !listed(id) && (l.hasFocus(gtx, l.restart[id]) || l.hasFocus(gtx, l.logs[id])) {
			gone = i
		}
	}
	for _, m := range []map[string]*Button{l.restart, l.logs} {
		for id := range m {
			if !listed(id) {
				delete(m, id)
			}
		}
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
				switch {
				case b.kind == ActionLogs:
					l.open = false
				case gtx.Focused(&b.m[s.ID].Clickable):
					// Restart goes while the panel starts: the focus
					// stays in its row.
					gtx.Execute(key.FocusCmd{Tag: &l.logs[s.ID].Clickable})
				}
			}
		}
	}
	l.shown = l.shown[:0]
	for _, s := range panels {
		l.shown = append(l.shown, s.ID)
		// A focused Restart goes when its panel starts, as by itself
		// after a backoff: the focus stays in its row.
		if kindOf(s) == kindStarting && l.hasFocus(gtx, l.restart[s.ID]) {
			gtx.Execute(key.FocusCmd{Tag: &l.logs[s.ID].Clickable})
		}
	}
	if gone >= 0 {
		switch {
		case len(panels) > 0:
			gtx.Execute(key.FocusCmd{Tag: &l.firstButton(panels[min(gone, len(panels)-1)]).Clickable})
		case refocus != nil:
			gtx.Execute(key.FocusCmd{Tag: &refocus.Clickable})
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
	none := "Every panel is running."
	if len(states) == 0 {
		none = "No panels are enabled."
	}
	var rows []recorded
	if len(panels) == 0 {
		rows = append(rows, record(igtx, label(th, none, theme.TextSmall, th.Muted, theme.Body).Layout))
	}
	for i, s := range panels {
		if i == 0 && l.focus {
			gtx.Execute(key.FocusCmd{Tag: &l.firstButton(s).Clickable})
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

	// Below the status, as tall as the room there, scrolling if it is
	// taller, so it never covers the status that closes it.
	at := image.Pt(0, anchor.Max.Y+gtx.Dp(4))
	below := area.Y - at.Y - gtx.Dp(theme.Space8)
	box := image.Rectangle{Max: image.Pt(width, max(min(height+2*pad, below), 0))}
	margin := gtx.Dp(theme.Space8)
	at.X = max(min(anchor.Max.X-box.Dx(), area.X-box.Dx()-margin), min(margin, max(area.X-box.Dx(), 0)))

	view := max(box.Dy()-2*pad, 0)
	tops := make([]int, len(rows))
	y := 0
	for i, r := range rows {
		if i > 0 {
			y += gap
		}
		tops[i] = y
		y += r.dims.Size.Y
	}
	// The wheel scrolls; a row the focus moves to scrolls into view.
	l.offset += l.wheel.Update(gtx.Metric, gtx.Source, gtx.Now, gesture.Vertical,
		pointer.ScrollRange{}, pointer.ScrollRange{Min: -l.offset, Max: max(height-view-l.offset, 0)})
	focused := slices.IndexFunc(panels, func(s pluginhost.PanelState) bool {
		return l.hasFocus(gtx, l.restart[s.ID]) || l.hasFocus(gtx, l.logs[s.ID])
	})
	if focused >= 0 && focused != l.focused {
		top, bottom := tops[focused], tops[focused]+rows[focused].dims.Size.Y
		switch {
		case top < l.offset:
			l.offset = top
		case bottom > l.offset+view:
			l.offset = bottom - view
		}
	}
	l.focused = focused
	l.offset = max(min(l.offset, height-view), 0)

	l.box = box.Add(at)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rr := gtx.Dp(theme.RadiusSmall)
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(box, rr).Op(gtx.Ops))
	strokeRRect(gtx, box, rr, gtx.Dp(1), th.Border)
	card := clip.Rect(box).Push(gtx.Ops)
	l.inside.Add(gtx.Ops)
	l.wheel.Add(gtx.Ops)
	card.Pop()
	defer op.Offset(image.Pt(pad, pad)).Push(gtx.Ops).Pop()
	shown := image.Rectangle{Max: image.Pt(inner, view)}
	defer viewport(gtx, shown)()
	defer clip.Rect(shown).Push(gtx.Ops).Pop()
	defer op.Offset(image.Pt(0, -l.offset)).Push(gtx.Ops).Pop()
	for i, r := range rows {
		if i > 0 {
			line := tops[i] - gap + gtx.Dp(theme.Space12)
			fillRect(gtx, image.Rect(0, line, inner, line+gtx.Dp(1)), th.Border)
		}
		r.at(gtx, image.Pt(0, tops[i]))
	}
	return actions
}

// hasFocus reports whether b, if any, has the focus.
func (l *StatusList) hasFocus(gtx layout.Context, b *Button) bool {
	return b != nil && gtx.Focused(&b.Clickable)
}

// firstButton is the first button in s's row: its Restart, or its Logs
// if it is starting.
func (l *StatusList) firstButton(s pluginhost.PanelState) *Button {
	if kindOf(s) != kindStarting {
		return l.restart[s.ID]
	}
	return l.logs[s.ID]
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
