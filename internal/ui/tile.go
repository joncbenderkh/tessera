// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"time"

	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// Menu items, in the order the tile menu lists them.
var menuItems = [...]struct {
	label string
	kind  ActionKind
}{
	{"Configure…", ActionConfigure},
	{"Disable panel", ActionDisable},
}

// tile is one panel's frame: its head, with the title, the plugin on
// hover, and the options menu, and a body for its state. A live panel's
// body is empty until views are rendered (#11).
type tile struct {
	head     gesture.Hover
	menu     Button
	items    [len(menuItems)]Button
	open     bool // the menu is showing
	focus    bool // move the focus into the menu on the next frame
	outside  gesture.Click
	inside   gesture.Click // the menu's own area, so clicks there stay
	restart  Button
	logs     Button
	headSize image.Point
	// view is the grid's visible area, in the tile's coordinates, which
	// the popovers stay inside.
	view    image.Rectangle
	menuBox image.Rectangle // where the menu was drawn; for tests
	buttons image.Rectangle // where Restart and Logs were drawn; for tests
}

// panelKind sorts a panel's state as the header's status does.
type panelKind int

const (
	kindLive panelKind = iota
	kindStarting
	kindStale
	kindFailing
)

func kindOf(s pluginhost.PanelState) panelKind {
	switch s.State {
	case pluginhost.Starting:
		return kindStarting
	case pluginhost.Stale:
		return kindStale
	case pluginhost.Live:
		// A view over a limit: no more are read until a restart.
		if s.ViewErr != nil {
			return kindStale
		}
	case pluginhost.Stopping:
		// Without an error, a restart the user asked for.
		if s.Err != nil {
			return kindFailing
		}
		return kindStarting
	case pluginhost.Restarting, pluginhost.Crashed:
		return kindFailing
	}
	return kindLive
}

// title is the panel's title: its view's, or else its id.
func title(s pluginhost.PanelState) string {
	if t := s.View.GetTitle(); t != "" {
		return t
	}
	return s.ID
}

// pluginInfo is the plugin's name, version, and view version, shown on
// hover over the tile's head.
func pluginInfo(s pluginhost.PanelState) string {
	if s.Info == nil {
		return s.Plugin
	}
	return fmt.Sprintf("%s · v%s · view/v%d", s.Info.GetName(), s.Info.GetVersion(), s.Info.GetViewVersion())
}

// Layout draws the tile over exactly gtx's constraints and returns the
// actions the user chose.
func (t *tile) Layout(gtx layout.Context, th *theme.Theme, s pluginhost.PanelState, narrow bool) []ActionKind {
	var actions []ActionKind
	if !gtx.Enabled() {
		// A dialog is open. The menu's backdrop would draw over it and
		// take its clicks, and a disabled gtx never sees them close it.
		t.open = false
	}
	if t.menu.Clicked(gtx) {
		t.open = !t.open
		t.focus = t.open
	}
	for i := range t.items {
		if t.items[i].Clicked(gtx) {
			actions = append(actions, menuItems[i].kind)
			t.open = false
		}
	}
	if t.open {
		for {
			if _, ok := t.inside.Update(gtx.Source); !ok {
				break
			}
		}
		for {
			e, ok := t.outside.Update(gtx.Source)
			if !ok {
				break
			}
			if e.Kind == gesture.KindClick {
				t.open = false
			}
		}
		for {
			e, ok := gtx.Event(key.Filter{Name: key.NameEscape})
			if !ok {
				break
			}
			if e, ok := e.(key.Event); ok && e.State == key.Press {
				t.open = false
				gtx.Execute(key.FocusCmd{Tag: &t.menu.Clickable})
			}
		}
	}
	if t.restart.Clicked(gtx) {
		actions = append(actions, ActionRestart)
	}
	if t.logs.Clicked(gtx) {
		actions = append(actions, ActionLogs)
	}

	size := gtx.Constraints.Max
	kind := kindOf(s)
	frame := image.Rectangle{Max: size}
	rr := gtx.Dp(theme.RadiusLarge)
	border := th.Border
	if kind == kindFailing {
		border = mix(th.Error, th.Border, 0.35)
	}
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(frame, rr).Op(gtx.Ops))
	strokeRRect(gtx, frame, rr, gtx.Dp(1), border)
	defer clip.UniformRRect(frame, rr).Push(gtx.Ops).Pop()

	headH := t.layoutHead(gtx, th, s, kind, narrow)
	body := gtx
	padX := gtx.Dp(theme.Space16)
	body.Constraints = layout.Constraints{Max: image.Pt(max(size.X-2*padX, 0), max(size.Y-headH-padX, 0))}
	st := op.Offset(image.Pt(padX, headH)).Push(gtx.Ops)
	switch kind {
	case kindStarting:
		label(th, "Starting "+s.Plugin+"…", theme.TextMin, th.Muted, theme.Mono).Layout(body)
	case kindFailing:
		t.errorBody(body, th, s, narrow)
		t.buttons = t.buttons.Add(image.Pt(padX, headH))
	}
	st.Pop()
	return actions
}

// layoutHead draws the head and returns its height. The plugin popover
// and the menu are deferred, so they draw over the tiles after this one;
// deferring drops the grid's clip, so they clip to t.view themselves.
func (t *tile) layoutHead(gtx layout.Context, th *theme.Theme, s pluginhost.PanelState, kind panelKind, narrow bool) int {
	width := gtx.Constraints.Max.X
	pad := gtx.Dp(theme.Space16)
	inner := gtx
	inner.Constraints = layout.Constraints{Max: image.Pt(max(width-2*pad, 0), gtx.Constraints.Max.Y)}
	t.menu.Icon, t.menu.Bare = true, true
	t.menu.Description = title(s) + " panel options"
	menu := record(inner, func(gtx layout.Context) layout.Dimensions { return t.menu.Layout(gtx, th, "⋯") })
	var tag recorded
	if kind == kindStale {
		tag = recordLabel(inner, th, "stale", theme.TextMin, th.Warn, theme.Mono)
	}
	gap := gtx.Dp(theme.Space8)
	titleW := max(inner.Constraints.Max.X-menu.dims.Size.X-tag.dims.Size.X-2*gap, 0)
	tgtx := inner
	tgtx.Constraints = layout.Constraints{Max: image.Pt(titleW, gtx.Constraints.Max.Y)}
	name := record(tgtx, material.LabelStyle{
		Text: title(s), TextSize: theme.TextSmall, Color: th.Fg,
		Font: font.Font{Typeface: theme.Body.Typeface, Weight: 150}, MaxLines: 1, Shaper: th.Shaper,
	}.Layout)

	h := unit.Dp(44)
	if narrow {
		h = 32
	}
	headH := max(gtx.Dp(h), name.dims.Size.Y+gap, menu.dims.Size.Y+gap)
	t.headSize = image.Pt(width, headH)
	area := clip.Rect{Max: t.headSize}.Push(gtx.Ops)
	t.head.Add(gtx.Ops)
	area.Pop()
	hovered := t.head.Update(gtx.Source)

	name.at(gtx, image.Pt(pad, (headH-name.dims.Size.Y)/2))
	x := width - pad - menu.dims.Size.X
	menu.at(gtx, image.Pt(x, (headH-menu.dims.Size.Y)/2))
	if kind == kindStale {
		x -= gap + tag.dims.Size.X
		tag.at(gtx, image.Pt(x, (headH-tag.dims.Size.Y)/2))
	}

	pop := op.Record(gtx.Ops)
	if (hovered || gtx.Focused(&t.menu.Clickable)) && !t.open {
		t.infoPopover(gtx, th, s, narrow)
	}
	if t.open {
		t.menuPopover(gtx, th, narrow)
	}
	op.Defer(gtx.Ops, pop.Stop())
	return headH
}

// infoPopover shows the plugin below the head's end. It takes no input,
// as CSS pointer-events: none.
func (t *tile) infoPopover(gtx layout.Context, th *theme.Theme, s pluginhost.PanelState, narrow bool) {
	top := gtx.Dp(36)
	if narrow {
		top = gtx.Dp(28)
	}
	padX, padY := gtx.Dp(10), gtx.Dp(theme.Space8)
	text := recordLabel(gtx, th, pluginInfo(s), theme.TextMin, th.Fg, theme.Mono)
	box := image.Rectangle{Max: text.dims.Size.Add(image.Pt(2*padX, 2*padY))}
	defer clip.Rect(t.view).Push(gtx.Ops).Pop()
	defer op.Offset(image.Pt(gtx.Constraints.Max.X-gtx.Dp(theme.Space12)-box.Dx(), t.inView(top, box.Dy()))).Push(gtx.Ops).Pop()
	rr := gtx.Dp(theme.RadiusSmall)
	paint.FillShape(gtx.Ops, th.Inset, clip.UniformRRect(box, rr).Op(gtx.Ops))
	strokeRRect(gtx, box, rr, gtx.Dp(1), th.Border)
	text.at(gtx, image.Pt(padX, padY))
}

// menuPopover shows the menu below the head's end, over a backdrop that
// closes it on a click anywhere else.
func (t *tile) menuPopover(gtx layout.Context, th *theme.Theme, narrow bool) {
	far := image.Rectangle{Min: image.Pt(-1<<20, -1<<20), Max: image.Pt(1<<20, 1<<20)}
	backdrop := clip.Rect(far).Push(gtx.Ops)
	t.outside.Add(gtx.Ops)
	backdrop.Pop()

	if t.focus {
		t.focus = false
		gtx.Execute(key.FocusCmd{Tag: &t.items[0].Clickable})
	}
	pad := gtx.Dp(6)
	igtx := gtx
	igtx.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(240), gtx.Dp(400))}
	widest := 0
	for i := range t.items {
		t.items[i].Bare = true
		widest = max(widest, record(igtx, func(gtx layout.Context) layout.Dimensions {
			return t.items[i].Layout(gtx, th, menuItems[i].label)
		}).dims.Size.X)
	}
	igtx.Constraints.Min.X = widest
	var items []recorded
	height := 0
	for i := range t.items {
		r := record(igtx, func(gtx layout.Context) layout.Dimensions {
			return t.items[i].Layout(gtx, th, menuItems[i].label)
		})
		items = append(items, r)
		height += r.dims.Size.Y
	}
	box := image.Rectangle{Max: image.Pt(widest+2*pad, height+2*pad)}
	top := t.headSize.Y
	if narrow {
		top = gtx.Dp(32)
	}
	// Moved up if it would run past the visible area's bottom, so its
	// items stay reachable at the window's edge.
	at := image.Pt(gtx.Constraints.Max.X-gtx.Dp(theme.Space8)-box.Dx(), t.inView(top, box.Dy()))
	t.menuBox = box.Add(at)
	defer clip.Rect(t.view).Push(gtx.Ops).Pop()
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rr := gtx.Dp(theme.RadiusSmall)
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(box, rr).Op(gtx.Ops))
	strokeRRect(gtx, box, rr, gtx.Dp(1), th.Border)
	// The menu takes the clicks on it, so they do not reach the backdrop.
	card := clip.Rect(box).Push(gtx.Ops)
	t.inside.Add(gtx.Ops)
	card.Pop()
	y := pad
	for _, r := range items {
		r.at(gtx, image.Pt(pad, y))
		y += r.dims.Size.Y
	}
}

// inView is the y at which a popover h tall, meant to sit at y, stays
// inside the visible area: moved up past its bottom, then down past its
// top if it is taller than the area.
func (t *tile) inView(y, h int) int {
	return max(min(y, t.view.Max.Y-h), t.view.Min.Y)
}

// errorBody is a failed panel's body: what went wrong, when it restarts,
// and the Restart and Logs buttons.
func (t *tile) errorBody(gtx layout.Context, th *theme.Theme, s pluginhost.PanelState, narrow bool) {
	symbol := gtx.Dp(32)
	gap := gtx.Dp(15)
	copyGtx := gtx
	copyGtx.Constraints = layout.Constraints{Max: image.Pt(max(gtx.Constraints.Max.X-symbol-gap, 0), gtx.Constraints.Max.Y)}
	next, line := retryText(s, gtx.Now)
	if !next.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: next})
	}
	detail := record(copyGtx, material.LabelStyle{
		Text: line, TextSize: theme.TextMin, Color: th.Muted, Font: theme.Mono,
		MaxLines: 2, Shaper: th.Shaper,
	}.Layout)
	restart := record(gtx, func(gtx layout.Context) layout.Dimensions { return t.restart.Layout(gtx, th, "Restart") })
	logs := record(gtx, func(gtx layout.Context) layout.Dimensions { return t.logs.Layout(gtx, th, "Logs") })

	// The buttons come first: in a short tile the message gives up lines,
	// then the text is cut off above the buttons, so they stay reachable.
	// The full message is in the Logs.
	buttonsH := max(restart.dims.Size.Y, logs.dims.Size.Y)
	lineGap, buttonGap := gtx.Dp(7), gtx.Dp(theme.Space12)
	room := gtx.Constraints.Max.Y - buttonsH - buttonGap
	var msg recorded
	for lines := 3; lines > 0; lines-- {
		msg = record(copyGtx, material.LabelStyle{
			Text: errorText(s), TextSize: theme.TextSmall, Color: th.Fg, Font: theme.Body,
			MaxLines: lines, Shaper: th.Shaper,
		}.Layout)
		if msg.dims.Size.Y+lineGap+detail.dims.Size.Y <= room {
			break
		}
	}
	y := max(symbol, msg.dims.Size.Y+lineGap+detail.dims.Size.Y) + buttonGap
	y = max(min(y, gtx.Constraints.Max.Y-buttonsH), 0)

	text := clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, max(y-buttonGap, 0))}.Push(gtx.Ops)
	box := image.Rectangle{Max: image.Pt(symbol, symbol)}
	rr := gtx.Dp(6)
	strokeRRect(gtx, box, rr, gtx.Dp(1), th.Error)
	mark := recordLabel(gtx, th, "!", theme.TextBrand, th.Error, theme.Body)
	mark.at(gtx, box.Max.Sub(mark.dims.Size).Div(2))
	msg.at(gtx, image.Pt(symbol+gap, 0))
	detail.at(gtx, image.Pt(symbol+gap, msg.dims.Size.Y+lineGap))
	text.Pop()

	// Beside the symbol, if they fit there.
	x := symbol + gap
	lx := restart.dims.Size.X + gtx.Dp(theme.Space8)
	if narrow || x+lx+logs.dims.Size.X > gtx.Constraints.Max.X {
		x = 0
	}
	lx += x
	restart.at(gtx, image.Pt(x, y))
	logs.at(gtx, image.Pt(lx, y))
	t.buttons = image.Rect(x, y, lx+logs.dims.Size.X, y+buttonsH)
}

// errorText says why the panel failed.
func errorText(s pluginhost.PanelState) string {
	switch {
	case s.Err != nil:
		return s.Err.Error()
	case s.ExitStatus != "":
		return "Plugin exited: " + s.ExitStatus
	}
	return "Plugin failed"
}

// retryText says what happens next to a failed panel, and when that
// text next changes, if it does.
func retryText(s pluginhost.PanelState, now time.Time) (time.Time, string) {
	switch s.State {
	case pluginhost.Restarting:
		left := s.RestartAt.Sub(now)
		secs := int(math.Ceil(left.Seconds()))
		if secs < 1 {
			return time.Time{}, fmt.Sprintf("Restarting — attempt %d/%d", s.Failures+1, s.MaxAttempts)
		}
		next := s.RestartAt.Add(-time.Duration(secs-1) * time.Second)
		return next, fmt.Sprintf("Restarting in %ds — attempt %d/%d", secs, s.Failures+1, s.MaxAttempts)
	case pluginhost.Crashed:
		if s.Failures >= s.MaxAttempts {
			return time.Time{}, fmt.Sprintf("Gave up after %d attempts", s.Failures)
		}
		return time.Time{}, "Not restarting: this needs a fix first"
	}
	return time.Time{}, "Stopping…"
}

// mix is a with weight w over b, as CSS color-mix, but in sRGB.
func mix(a, b color.NRGBA, w float64) color.NRGBA {
	ch := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*w + float64(y)*(1-w))) }
	return color.NRGBA{R: ch(a.R, b.R), G: ch(a.G, b.G), B: ch(a.B, b.B), A: ch(a.A, b.A)}
}
