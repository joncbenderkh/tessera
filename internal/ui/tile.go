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
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
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
// hover, and the options menu, and a body for its state: a live panel's
// view, or why it has none.
type tile struct {
	content  viewState
	launch   *tesserav1.DescribeResponse // the launch content is for: each answers Describe anew
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
	// menuEnd is the bottom of the head's ⋯ button, which the plugin
	// popover stays below.
	menuEnd int
	// view is the grid's visible area, in the tile's coordinates, which
	// the popovers stay inside.
	view    image.Rectangle
	menuBox image.Rectangle // where the menu was drawn; for tests
	infoBox image.Rectangle // where the plugin popover was drawn; for tests
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
// actions the user chose and the input for the plugin.
func (t *tile) Layout(gtx layout.Context, th *theme.Theme, s pluginhost.PanelState, narrow bool) ([]ActionKind, []*tesserav1.HandleEventRequest) {
	var actions []ActionKind
	var events []*tesserav1.HandleEventRequest
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
	if kind == kindStarting || kind == kindFailing || s.View == nil || s.Info != t.launch {
		// A new launch starts afresh: its first view's ranges are sent.
		// Its Info tells it apart even with no frame drawn in between.
		t.content = viewState{}
		t.launch = s.Info
	}
	switch {
	case kind == kindStarting:
		label(th, "Starting "+s.Plugin+"…", theme.TextMin, th.Muted, theme.Mono).Layout(body)
	case kind == kindFailing:
		t.errorBody(body, th, s, narrow)
		t.buttons = t.buttons.Add(image.Pt(padX, headH))
	case s.View != nil:
		events = t.content.Layout(body, th, s.View)
	case s.ViewErr != nil:
		// A view over a limit, shown no more until a restart, or a polled
		// panel's failed Render before its first view, until one works.
		wrapped(th, s.ViewErr.Error(), theme.TextMin, th.Muted, theme.Mono).Layout(body)
	}
	st.Pop()
	return actions, events
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
	name := record(tgtx, titleLabel(th, title(s)).Layout)
	headH := headHeight(gtx, name.dims.Size.Y, menu.dims.Size.Y, narrow)
	t.headSize = image.Pt(width, headH)
	area := clip.Rect{Max: t.headSize}.Push(gtx.Ops)
	t.head.Add(gtx.Ops)
	area.Pop()
	hovered := t.head.Update(gtx.Source)

	name.at(gtx, image.Pt(pad, (headH-name.dims.Size.Y)/2))
	x := width - pad - menu.dims.Size.X
	menu.at(gtx, image.Pt(x, (headH-menu.dims.Size.Y)/2))
	t.menuEnd = (headH + menu.dims.Size.Y) / 2
	if kind == kindStale {
		x -= gap + tag.dims.Size.X
		tag.at(gtx, image.Pt(x, (headH-tag.dims.Size.Y)/2))
	}

	t.infoBox = image.Rectangle{}
	pop := op.Record(gtx.Ops)
	// ⋯ sits over the head's hover area, so hovering it counts too.
	if (hovered || t.menu.Hovered() || gtx.Focused(&t.menu.Clickable)) && !t.open {
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
	top = max(top, t.menuEnd) // large text makes the head taller
	padX, padY := gtx.Dp(10), gtx.Dp(theme.Space8)
	lgtx := gtx
	lgtx.Constraints = layout.Constraints{Max: image.Pt(max(t.view.Dx()-2*padX, 0), gtx.Constraints.Max.Y)}
	text := recordLabel(lgtx, th, pluginInfo(s), theme.TextMin, th.Fg, theme.Mono)
	box := image.Rectangle{Max: text.dims.Size.Add(image.Pt(2*padX, 2*padY))}
	at := t.inView(image.Pt(gtx.Constraints.Max.X-gtx.Dp(theme.Space12)-box.Dx(), top), box.Size())
	t.infoBox = box.Add(at)
	defer clip.Rect(t.view).Push(gtx.Ops).Pop()
	defer op.Offset(at).Push(gtx.Ops).Pop()
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
	pad := gtx.Dp(menuPad)
	igtx := gtx
	igtx.Constraints = layout.Constraints{Max: image.Pt(max(min(gtx.Dp(240), t.view.Dx()-2*pad), 0), gtx.Dp(400))}
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
	// Moved up if it would run past the visible area's bottom, so its
	// items stay reachable at the window's edge.
	at := t.inView(image.Pt(gtx.Constraints.Max.X-gtx.Dp(theme.Space8)-box.Dx(), top), box.Size())
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

// inView is where a popover of size, meant to sit at at, stays inside
// the visible area: moved up and left past its bottom and right edges,
// then down and right past its top and left ones if it is larger.
func (t *tile) inView(at, size image.Point) image.Point {
	return image.Pt(
		max(min(at.X, t.view.Max.X-size.X), t.view.Min.X),
		max(min(at.Y, t.view.Max.Y-size.Y), t.view.Min.Y),
	)
}

// titleLabel is a tile head's title.
func titleLabel(th *theme.Theme, text string) material.LabelStyle {
	return material.LabelStyle{
		Text: text, TextSize: theme.TextSmall, Color: th.Fg,
		Font: font.Font{Typeface: theme.Body.Typeface, Weight: 150}, MaxLines: 1, Shaper: th.Shaper,
	}
}

// headHeight is the height of a tile head whose title and menu button
// are nameH and menuH tall.
func headHeight(gtx layout.Context, nameH, menuH int, narrow bool) int {
	h := unit.Dp(44)
	if narrow {
		h = 32
	}
	gap := gtx.Dp(theme.Space8)
	return max(gtx.Dp(h), nameH+gap, menuH+gap)
}

// minTile is the smallest tile that holds its head and, below it, a
// failed panel's Restart and Logs buttons side by side, or its open
// menu, at gtx's text size, and the height that holds the buttons one
// above the other, as a narrower tile has them. No column is narrower,
// if the window allows, and no row shorter, so all of them stay in
// reach.
func minTile(gtx layout.Context, th *theme.Theme, narrow bool) (least image.Point, stacked int) {
	gtx.Constraints = layout.Constraints{Max: image.Pt(1<<20, 1<<20)}
	var restart, logs, menu Button
	menu.Icon, menu.Bare = true, true
	measure := func(b *Button, label string) image.Point {
		return record(gtx, func(gtx layout.Context) layout.Dimensions { return b.Layout(gtx, th, label) }).dims.Size
	}
	r, l, m := measure(&restart, "Restart"), measure(&logs, "Logs"), measure(&menu, "⋯")
	items := 2 * gtx.Dp(menuPad)
	for _, it := range menuItems {
		item := Button{Bare: true}
		items += measure(&item, it.label).Y
	}
	name := record(gtx, titleLabel(th, "Ag").Layout).dims.Size
	pad, space := gtx.Dp(theme.Space16), gtx.Dp(theme.Space8)
	head := headHeight(gtx, name.Y, m.Y, narrow)
	return image.Pt(2*pad+r.X+space+l.X, max(head+max(r.Y, l.Y)+pad, head+items)), max(head+r.Y+space+l.Y+pad, head+items)
}

// menuPad is the padding around the tile menu's items.
const menuPad unit.Dp = 6

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

	// The buttons sit in a row beside the symbol, or below it if they do
	// not fit there, or one above the other if they do not fit in a row.
	space := gtx.Dp(theme.Space8)
	x, logsAt := symbol+gap, image.Pt(restart.dims.Size.X+space, 0)
	buttons := image.Pt(logsAt.X+logs.dims.Size.X, max(restart.dims.Size.Y, logs.dims.Size.Y))
	if narrow || x+buttons.X > gtx.Constraints.Max.X {
		x = 0
	}
	if buttons.X > gtx.Constraints.Max.X {
		logsAt = image.Pt(0, restart.dims.Size.Y+space)
		buttons = image.Pt(max(restart.dims.Size.X, logs.dims.Size.X), logsAt.Y+logs.dims.Size.Y)
	}

	// The buttons come first: in a short tile the message gives up lines,
	// then the text is cut off above the buttons, so they stay reachable.
	// The full message is in the Logs.
	buttonsH := buttons.Y
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

	at := image.Pt(x, y)
	restart.at(gtx, at)
	logs.at(gtx, at.Add(logsAt))
	t.buttons = image.Rectangle{Min: at, Max: at.Add(buttons)}
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
