// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"errors"
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/op"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
)

// press clicks at at.
func press(r *input.Router, at image.Point) {
	for _, e := range click(at) {
		r.Queue(e)
	}
}

func center(r image.Rectangle) image.Point { return r.Min.Add(r.Max).Div(2) }

// The buttons drawn in ops, by description.
func buttons(ops *op.Ops) map[string]image.Rectangle {
	out := map[string]image.Rectangle{}
	for _, n := range foreignSemantics(ops) {
		if n.Desc.Class == semantic.Button && n.Desc.Description != "" {
			out[n.Desc.Description] = n.Desc.Bounds
		}
	}
	return out
}

func TestAttention(t *testing.T) {
	states := []pluginhost.PanelState{
		{ID: "a", State: pluginhost.Live},
		{ID: "b", State: pluginhost.Crashed},
		{ID: "c", State: pluginhost.Starting},
		{ID: "d", State: pluginhost.Stale},
		{ID: "e", State: pluginhost.Live, ViewErr: errors.New("view too deep")},
	}
	var got []string
	for _, s := range attention(states) {
		got = append(got, s.ID)
	}
	if want := []string{"b", "c", "d", "e"}; !slices.Equal(got, want) {
		t.Errorf("attention = %v, want %v", got, want)
	}
}

// The list shows the panels that are not live, each with Logs, and
// Restart unless it is starting; Restart keeps it open, Logs closes it,
// as do Escape and a click outside.
func TestStatusList(t *testing.T) {
	states := []pluginhost.PanelState{
		{ID: "a", Plugin: "clock", State: pluginhost.Live},
		{ID: "b", Plugin: "weather", State: pluginhost.Crashed, Err: errors.New("settings rejected")},
		{ID: "c", Plugin: "disk", State: pluginhost.Starting},
	}
	size := image.Pt(1440, 900)
	anchor := image.Rect(1000, 13, 1100, 45)
	var r input.Router
	var l StatusList
	var ops *op.Ops
	frame := func() []Action {
		gtx := context(&r, size, time.Now())
		a := l.Layout(gtx, testTheme, states, anchor, nil)
		r.Frame(gtx.Ops)
		ops = gtx.Ops
		return a
	}
	if frame(); l.box != (image.Rectangle{}) {
		t.Fatal("drawn while closed")
	}
	l.Toggle(false)
	frame()
	if l.box.Min.Y < anchor.Max.Y || l.box.Max.X > anchor.Max.X {
		t.Errorf("list at %v, want below and ending with the status at %v", l.box, anchor)
	}
	b := buttons(ops)
	for _, want := range []string{"Restart b", "Logs of b", "Logs of c"} {
		if r, ok := b[want]; !ok || !r.In(l.box) {
			t.Errorf("%s at %v, %t; want inside the list at %v", want, r, ok, l.box)
		}
	}
	for _, unwanted := range []string{"Restart a", "Logs of a", "Restart c"} {
		if _, ok := b[unwanted]; ok {
			t.Errorf("%s is listed", unwanted)
		}
	}

	press(&r, center(b["Restart b"]))
	if a := frame(); !slices.Equal(a, []Action{{Kind: ActionRestart, Panel: "b"}}) || !l.IsOpen() {
		t.Errorf("Restart: %v, open %t", a, l.IsOpen())
	}
	press(&r, center(buttons(ops)["Logs of c"]))
	if a := frame(); !slices.Equal(a, []Action{{Kind: ActionLogs, Panel: "c"}}) || l.IsOpen() {
		t.Errorf("Logs: %v, open %t", a, l.IsOpen())
	}

	l.Toggle(false)
	frame()
	press(&r, image.Pt(10, 800))
	if frame(); l.IsOpen() {
		t.Error("open after a click outside")
	}
	l.Toggle(false)
	frame()
	r.Queue(key.Event{Name: key.NameEscape, State: key.Press})
	if frame(); l.IsOpen() {
		t.Error("open after Escape")
	}

	// With every panel live it says so, and a dialog closes it.
	states = states[:1]
	l.Toggle(false)
	frame()
	if len(buttons(ops)) != 0 || l.box.Empty() {
		t.Errorf("all live: buttons %v, box %v", buttons(ops), l.box)
	}
	gtx := context(&r, size, time.Now())
	l.Layout(gtx.Disabled(), testTheme, states, anchor, nil)
	if l.IsOpen() {
		t.Error("open under a dialog")
	}
}

// The status is a button in a wide window and a narrow one, where its
// dot alone shows, named by its text.
func TestHeaderStatusClick(t *testing.T) {
	for _, w := range []int{1440, 900} {
		var r input.Router
		var h Header
		size := image.Pt(w, 900)
		gtx := context(&r, size, time.Now())
		h.Layout(gtx, testTheme, Status{Text: "3 panels · 1 error", Tone: ToneError})
		r.Frame(gtx.Ops)
		at := h.StatusAnchor()
		if at.Empty() {
			t.Fatalf("at %d: no status drawn", w)
		}
		d, ok := findSemantic(foreignSemantics(gtx.Ops), func(d input.SemanticDesc) bool {
			return d.Class == semantic.Button && d.Label == "3 panels · 1 error"
		})
		if !ok || d.Bounds != at {
			t.Errorf("at %d: status button %v, %t; want named by its text at %v", w, d.Bounds, ok, at)
		}
		if w <= int(Compact) && at.Dx() > 40 {
			t.Errorf("at %d: status %d wide, want its dot alone", w, at.Dx())
		}
		press(&r, center(at))
		if !h.Status.Clicked(context(&r, size, time.Now())) {
			t.Errorf("at %d: a click on the status was not seen", w)
		}
	}
}

// A list taller than the room below the status scrolls there, rather
// than covering the status that closes it.
func TestStatusListStaysBelowTheStatus(t *testing.T) {
	var states []pluginhost.PanelState
	for i := range 12 {
		states = append(states, pluginhost.PanelState{ID: fmt.Sprint("p", i), Plugin: "weather", State: pluginhost.Crashed, Err: errors.New("failed")})
	}
	for _, size := range []image.Point{image.Pt(1440, 600), image.Pt(360, 600)} {
		var r input.Router
		var l StatusList
		anchor := image.Rect(size.X-200, 13, size.X-100, 45)
		l.Toggle(false)
		gtx := context(&r, size, time.Now())
		l.Layout(gtx, testTheme, states, anchor, nil)
		if l.box.Overlaps(anchor) || l.box.Max.Y > size.Y {
			t.Errorf("at %v: list %v covers the status %v or leaves the window", size, l.box, anchor)
		}
	}
}

// A key that opens the list focuses its first button, Restart or, for a
// starting panel, Logs; Restart by key leaves the focus on its Logs, and
// Escape gives it back to the status.
func TestStatusListKeyboard(t *testing.T) {
	size := image.Pt(1440, 900)
	anchor := image.Rect(1000, 13, 1100, 45)
	for _, tc := range []struct {
		state pluginhost.State
		first string
	}{{pluginhost.Crashed, "restart"}, {pluginhost.Starting, "logs"}} {
		states := []pluginhost.PanelState{{ID: "b", Plugin: "weather", State: tc.state, Err: errors.New("failed")}}
		var r input.Router
		var l StatusList
		var status Button
		frame := func() []Action {
			gtx := context(&r, size, time.Now())
			a := l.Layout(gtx, testTheme, states, anchor, &status)
			status.Layout(gtx, testTheme, "status")
			r.Frame(gtx.Ops)
			return a
		}
		l.Toggle(true)
		frame()
		frame()
		focused := map[string]bool{"restart": l.restart["b"] != nil && r.Source().Focused(&l.restart["b"].Clickable), "logs": r.Source().Focused(&l.logs["b"].Clickable)}
		if !focused[tc.first] {
			t.Errorf("%v: focus %v, want %s", tc.state, focused, tc.first)
		}
		if tc.state == pluginhost.Crashed {
			r.Queue(key.Event{Name: key.NameSpace, State: key.Press}, key.Event{Name: key.NameSpace, State: key.Release})
			if a := frame(); !slices.Equal(a, []Action{{Kind: ActionRestart, Panel: "b"}}) {
				t.Errorf("Space on Restart: %v", a)
			}
			states[0].State = pluginhost.Starting // its Restart goes
			frame()
			if !r.Source().Focused(&l.logs["b"].Clickable) {
				t.Error("after Restart by key, the focus is not on Logs")
			}
		}
		r.Queue(key.Event{Name: key.NameEscape, State: key.Press})
		frame()
		if l.IsOpen() || !r.Source().Focused(&status.Clickable) {
			t.Errorf("%v: after Escape open %t, status focused %t", tc.state, l.IsOpen(), r.Source().Focused(&status.Clickable))
		}
	}
}

// Buttons go with the panels that no longer need attention.
func TestStatusListDropsButtons(t *testing.T) {
	var r input.Router
	var l StatusList
	l.Toggle(false)
	states := []pluginhost.PanelState{{ID: "b", State: pluginhost.Crashed}}
	l.Layout(context(&r, image.Pt(800, 600), time.Now()), testTheme, states, image.Rect(600, 10, 700, 40), nil)
	states[0].State = pluginhost.Live
	l.Layout(context(&r, image.Pt(800, 600), time.Now()), testTheme, states, image.Rect(600, 10, 700, 40), nil)
	if len(l.restart) != 0 || len(l.logs) != 0 {
		t.Errorf("buttons kept: %d restart, %d logs", len(l.restart), len(l.logs))
	}
}

// Tab reaches every row of a list longer than its view, which scrolls
// to follow the focus.
func TestStatusListTabReachesEveryRow(t *testing.T) {
	for _, dir := range []key.FocusDirection{key.FocusForward, key.FocusBackward} {
		for _, outside := range []bool{false, true} {
			testTabReachesEveryRow(t, dir, outside)
		}
	}
}

// testTabReachesEveryRow moves the focus in dir 40 times, with the
// status, if outside, before the list, as the window has it.
func testTabReachesEveryRow(t *testing.T, dir key.FocusDirection, outside bool) {
	var states []pluginhost.PanelState
	for i := range 12 {
		states = append(states, pluginhost.PanelState{ID: fmt.Sprint("p", i), Plugin: "weather", State: pluginhost.Crashed, Err: errors.New("failed")})
	}
	size := image.Pt(1440, 600)
	anchor := image.Rect(1000, 13, 1100, 45)
	var r input.Router
	var l StatusList
	var status Button
	frame := func() {
		gtx := context(&r, size, time.Now())
		if outside {
			status.Layout(gtx, testTheme, "status")
		}
		l.Layout(gtx, testTheme, states, anchor, &status)
		r.Frame(gtx.Ops)
	}
	l.Toggle(true)
	frame()
	frame()
	reached := map[string]bool{}
	for range 40 {
		for id, b := range l.logs {
			if r.Source().Focused(&b.Clickable) {
				reached[id] = true
			}
		}
		r.MoveFocus(dir)
		frame()
	}
	if len(reached) != len(states) {
		t.Errorf("%v, status outside %t: reached the Logs of %d rows of %d: %v", dir, outside, len(reached), len(states), reached)
	}
}

// A focused row whose panel recovers hands the focus to the row now in
// its place, or, with none left, to the status.
func TestStatusListFocusOnRecovery(t *testing.T) {
	size := image.Pt(1440, 900)
	anchor := image.Rect(1000, 13, 1100, 45)
	states := []pluginhost.PanelState{
		{ID: "a", State: pluginhost.Crashed},
		{ID: "b", State: pluginhost.Crashed},
	}
	var r input.Router
	var l StatusList
	var status Button
	frame := func() {
		gtx := context(&r, size, time.Now())
		l.Layout(gtx, testTheme, states, anchor, &status)
		status.Layout(gtx, testTheme, "status")
		r.Frame(gtx.Ops)
	}
	l.Toggle(true)
	frame()
	frame()
	states[0].State = pluginhost.Live
	frame()
	frame()
	if !r.Source().Focused(&l.restart["b"].Clickable) {
		t.Error("the focus did not go to the next row")
	}
	states[1].State = pluginhost.Live
	frame()
	frame()
	if !r.Source().Focused(&status.Clickable) || !l.IsOpen() {
		t.Errorf("with no rows left: status focused %t, open %t", r.Source().Focused(&status.Clickable), l.IsOpen())
	}
}

// A focused Restart whose panel starts by itself hands the focus to its
// row's Logs.
func TestStatusListFocusWhenRestartGoes(t *testing.T) {
	states := []pluginhost.PanelState{{ID: "a", State: pluginhost.Restarting}, {ID: "b", State: pluginhost.Crashed}}
	var r input.Router
	var l StatusList
	frame := func() {
		gtx := context(&r, image.Pt(1440, 900), time.Now())
		l.Layout(gtx, testTheme, states, image.Rect(1000, 13, 1100, 45), nil)
		r.Frame(gtx.Ops)
	}
	l.Toggle(true)
	frame()
	frame()
	states[0].State = pluginhost.Starting
	frame()
	frame()
	if !r.Source().Focused(&l.logs["a"].Clickable) {
		t.Error("the focus did not go to the row's Logs")
	}
}

// The list keeps a margin from the window's edge, and says when no
// panel is enabled.
func TestStatusListEdgesAndNone(t *testing.T) {
	var r input.Router
	var l StatusList
	l.Toggle(false)
	states := []pluginhost.PanelState{{ID: "b", State: pluginhost.Crashed}}
	gtx := context(&r, image.Pt(360, 600), time.Now())
	l.Layout(gtx, testTheme, states, image.Rect(200, 13, 260, 45), nil)
	if l.box.Min.X < 8 || l.box.Max.X > 360-8 {
		t.Errorf("list %v within 8 dp of a 360 dp window's edge", l.box)
	}
	gtx = context(&r, image.Pt(360, 600), time.Now())
	l.Layout(gtx, testTheme, nil, image.Rect(200, 13, 260, 45), nil)
	if _, ok := findSemantic(foreignSemantics(gtx.Ops), func(d input.SemanticDesc) bool { return d.Label == "No panels are enabled." }); !ok {
		t.Error("no word that no panel is enabled")
	}
}

// manyFailing is n failing panels, p0 to p(n-1).
func manyFailing(n int) []pluginhost.PanelState {
	var states []pluginhost.PanelState
	for i := range n {
		states = append(states, pluginhost.PanelState{ID: fmt.Sprint("p", i), Plugin: "weather", State: pluginhost.Crashed, Err: errors.New("failed")})
	}
	return states
}

// focusedRow is the id of the row whose button has the focus, or "".
func focusedRow(r *input.Router, l *StatusList) string {
	for id, b := range l.restart {
		if r.Source().Focused(&b.Clickable) {
			return id
		}
	}
	for id, b := range l.logs {
		if r.Source().Focused(&b.Clickable) {
			return id
		}
	}
	return ""
}

// A middle row's panel recovering hands the focus to the row in its
// place, and it stays there.
func TestStatusListRecoveryInTheMiddle(t *testing.T) {
	states := manyFailing(5)
	var r input.Router
	var l StatusList
	frame := func() {
		gtx := context(&r, image.Pt(1440, 900), time.Now())
		l.Layout(gtx, testTheme, states, image.Rect(1000, 13, 1100, 45), nil)
		r.Frame(gtx.Ops)
	}
	l.Toggle(true)
	frame()
	frame()
	r.MoveFocus(key.FocusForward) // p0's Logs
	frame()
	r.MoveFocus(key.FocusForward) // p1's Restart
	frame()
	if got := focusedRow(&r, &l); got != "p1" {
		t.Fatalf("focus on %q, want p1", got)
	}
	states[1].State = pluginhost.Live
	for range 3 {
		frame()
	}
	if got := focusedRow(&r, &l); got != "p2" {
		t.Errorf("after p1 recovered, focus on %q, want p2", got)
	}
}

// The wheel scrolls a long list; Tab from the status, before it, then
// goes to its first row, scrolled back into view.
func TestStatusListWheelThenTab(t *testing.T) {
	states := manyFailing(12)
	size := image.Pt(1440, 600)
	var r input.Router
	var l StatusList
	var status Button
	frame := func() {
		gtx := context(&r, size, time.Now())
		status.Layout(gtx, testTheme, "status")
		l.Layout(gtx, testTheme, states, image.Rect(1000, 13, 1100, 45), &status)
		r.Frame(gtx.Ops)
	}
	l.Toggle(false)
	frame()
	at := f32.Pt(float32(center(l.box).X), float32(center(l.box).Y))
	r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: at},
		pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: at, Scroll: f32.Pt(0, 400)})
	frame()
	if l.offset == 0 {
		t.Fatal("the wheel did not scroll the list")
	}
	gtx := context(&r, size, time.Now())
	gtx.Execute(key.FocusCmd{Tag: &status.Clickable})
	frame()
	r.MoveFocus(key.FocusForward)
	frame()
	frame()
	if got := focusedRow(&r, &l); got != "p0" || l.offset != 0 {
		t.Errorf("Tab into the scrolled list: focus on %q, offset %d; want p0, 0", got, l.offset)
	}
}

// The wheel scrolls over a row's buttons too; Tab within a row the wheel
// took out of view, and a view grown shorter, bring the focus back in.
func TestStatusListWheelAndFocus(t *testing.T) {
	states := manyFailing(12)
	size := image.Pt(1440, 600)
	var r input.Router
	var l StatusList
	frame := func() {
		gtx := context(&r, size, time.Now())
		l.Layout(gtx, testTheme, states, image.Rect(1000, 13, 1100, 45), nil)
		r.Frame(gtx.Ops)
	}
	wheel := func(at image.Point, dy float32) {
		p := f32.Pt(float32(at.X), float32(at.Y))
		r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: p},
			pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: p, Scroll: f32.Pt(0, dy)})
		frame()
	}
	l.Toggle(true) // the focus on p0's Restart
	frame()
	frame()
	gtx := context(&r, size, time.Now())
	l.Layout(gtx, testTheme, states, image.Rect(1000, 13, 1100, 45), nil)
	r.Frame(gtx.Ops)
	wheel(center(buttons(gtx.Ops)["Restart p0"]), 400)
	if l.offset == 0 {
		t.Fatal("the wheel over a button did not scroll")
	}
	r.MoveFocus(key.FocusForward) // p0's Logs, out of view
	frame()
	frame()
	if !r.Source().Focused(&l.logs["p0"].Clickable) || l.offset != 0 {
		t.Errorf("Tab within a scrolled-away row: offset %d, want 0", l.offset)
	}

	// The focus on a lower row, then the window shorter.
	for range 10 {
		r.MoveFocus(key.FocusForward)
		frame()
	}
	size.Y = 300
	frame()
	frame()
	f := l.followed
	if f.button == nil || f.top < l.offset || f.bottom > l.offset+f.view {
		t.Errorf("after the window shrank, the focused row %d-%d is out of view %d-%d", f.top, f.bottom, l.offset, l.offset+f.view)
	}
}

// In a row taller than the view, the focus keeps the buttons' line in
// view, moving from Restart to Logs too.
func TestStatusListTallRow(t *testing.T) {
	long := strings.Repeat("a long reason why the plugin failed ", 20)
	states := []pluginhost.PanelState{{ID: "a", Plugin: "weather", State: pluginhost.Crashed, Err: errors.New("failed")},
		{ID: "b", Plugin: "weather", State: pluginhost.Crashed, Err: errors.New(long)}}
	size := image.Pt(1440, 110)
	var r input.Router
	var l StatusList
	var ops *op.Ops
	frame := func() {
		gtx := context(&r, size, time.Now())
		l.Layout(gtx, testTheme, states, image.Rect(1000, 0, 1100, 4), nil)
		r.Frame(gtx.Ops)
		ops = gtx.Ops
	}
	l.Toggle(true)
	frame()
	frame()
	for i := range 3 { // a's Logs, then b's Restart and Logs
		r.MoveFocus(key.FocusForward)
		frame()
		frame()
		want := []string{"Logs of a", "Restart b", "Logs of b"}[i]
		if b, ok := buttons(ops)[want]; !ok || !b.In(l.box) {
			t.Errorf("%s focused at %v, out of the list %v", want, b, l.box)
		}
	}
}

// A focused button's ring stays inside the list's view: on its first
// button, scrolled into view from below, and on its last row when all
// fit.
func TestStatusListRingInView(t *testing.T) {
	for _, n := range []int{12, 2} {
		states := manyFailing(n)
		var r input.Router
		var l StatusList
		var ops *op.Ops
		frame := func() {
			gtx := context(&r, image.Pt(1440, 600), time.Now())
			l.Layout(gtx, testTheme, states, image.Rect(1000, 13, 1100, 45), nil)
			r.Frame(gtx.Ops)
			ops = gtx.Ops
		}
		l.Toggle(true)
		frame()
		frame()
		check := func(name string) {
			t.Helper()
			view := l.box.Inset(12) // the card's padding
			if b := buttons(ops)[name].Inset(-5); !b.In(view) {
				t.Errorf("%d rows: %s's ring %v outside the view %v", n, name, b, view)
			}
		}
		check("Restart p0")
		for range 2*n - 1 {
			r.MoveFocus(key.FocusForward)
			frame()
			frame()
		}
		check(fmt.Sprintf("Logs of p%d", n-1))
	}
}
