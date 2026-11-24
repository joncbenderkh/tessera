// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"errors"
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
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
