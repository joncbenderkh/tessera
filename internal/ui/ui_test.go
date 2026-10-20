// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"errors"
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

var testTheme = theme.New(true, theme.NewShaper(false))

func context(r *input.Router, size image.Point, now time.Time) layout.Context {
	return layout.Context{
		Ops:         new(op.Ops),
		Source:      r.Source(),
		Now:         now,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(size),
	}
}

func TestStatusOf(t *testing.T) {
	states := func(ss ...pluginhost.State) []pluginhost.PanelState {
		out := make([]pluginhost.PanelState, len(ss))
		for i, s := range ss {
			out[i].State = s
		}
		return out
	}
	stopping := func(err error) []pluginhost.PanelState {
		return []pluginhost.PanelState{{State: pluginhost.Stopping, Err: err}}
	}
	for _, tt := range []struct {
		states []pluginhost.PanelState
		want   Status
	}{
		{nil, Status{"0 panels", ToneOk}},
		{states(pluginhost.Live), Status{"1 panel", ToneOk}},
		{states(pluginhost.Live, pluginhost.Starting), Status{"2 panels · 1 starting", ToneWarn}},
		{states(pluginhost.Stale, pluginhost.Live), Status{"2 panels · 1 stale", ToneWarn}},
		{states(pluginhost.Starting, pluginhost.Crashed), Status{"2 panels · 1 starting · 1 error", ToneError}},
		{states(pluginhost.Restarting, pluginhost.Crashed), Status{"2 panels · 2 errors", ToneError}},
		{stopping(nil), Status{"1 panel · 1 starting", ToneWarn}},
		{[]pluginhost.PanelState{{State: pluginhost.Live, ViewErr: errors.New("view too deep")}}, Status{"1 panel · 1 stale", ToneWarn}},
		{stopping(errors.New("exited")), Status{"1 panel · 1 error", ToneError}},
	} {
		if got := StatusOf(tt.states); got != tt.want {
			t.Errorf("StatusOf(%v) = %+v, want %+v", tt.states, got, tt.want)
		}
	}
}

func TestFitHeader(t *testing.T) {
	for _, tt := range []struct {
		width            unit.Dp
		status, selector bool
	}{
		{1440, true, true},
		{1001, true, true},
		{1000, false, true},
		{561, false, true},
		{560, false, false},
		{320, false, false},
	} {
		f := fitHeader(tt.width)
		if f.status != tt.status || f.selector != tt.selector {
			t.Errorf("at %v dp: status %t, selector %t; want %t, %t", tt.width, f.status, f.selector, tt.status, tt.selector)
		}
	}
}

func TestHeaderLayout(t *testing.T) {
	var r input.Router
	for _, w := range []int{1440, 900, 520, 200} {
		var h Header
		gtx := context(&r, image.Pt(w, 900), time.Now())
		dims := h.Layout(gtx, testTheme, Status{Text: "3 panels"})
		if want := image.Pt(w, int(HeaderHeight)); dims.Size != want {
			t.Errorf("at %d: size %v, want %v", w, dims.Size, want)
		}
	}
}

func TestHeaderButtonsClick(t *testing.T) {
	size := image.Pt(1440, 900)
	for _, tt := range []struct {
		name   string
		button func(*Header) *Button
	}{
		{"theme", func(h *Header) *Button { return &h.Theme }},
		{"edit", func(h *Header) *Button { return &h.Edit }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var r input.Router
			var h Header
			gtx := context(&r, size, time.Now())
			h.Layout(gtx, testTheme, Status{Text: "3 panels"})
			r.Frame(gtx.Ops)
			// The theme button ends 28 dp from the right edge; Edit
			// ends 14 dp before it.
			x := float32(size.X - 28 - 16)
			if tt.name == "edit" {
				x = float32(size.X - 28 - 32 - 14 - 10)
			}
			p := f32.Pt(x, float32(HeaderHeight)/2)
			r.Queue(
				pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
				pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p},
			)
			if !tt.button(&h).Clicked(context(&r, size, time.Now())) {
				t.Fatal("a click on the button was not seen")
			}
		})
	}
}

func TestToaster(t *testing.T) {
	var r input.Router
	t0 := time.Now()
	frame := func(tt *Toaster, at time.Duration) string {
		tt.Layout(context(&r, image.Pt(800, 600), t0.Add(at)), testTheme)
		return tt.Text()
	}
	var toast Toaster
	if got := frame(&toast, 0); got != "" {
		t.Fatalf("an unused toaster shows %q", got)
	}
	toast.Show("one")
	if got := frame(&toast, time.Second); got != "one" {
		t.Fatalf("shows %q, want one", got)
	}
	if got := frame(&toast, time.Second+ToastFor-time.Millisecond); got != "one" {
		t.Fatalf("gone early: %q", got)
	}
	if got := frame(&toast, time.Second+ToastFor); got != "" {
		t.Fatalf("still showing %q after %v", got, ToastFor)
	}

	toast.Show("two")
	frame(&toast, 10*time.Second)
	toast.Show("three")
	if got := frame(&toast, 11*time.Second); got != "three" {
		t.Fatalf("shows %q, want the newest message", got)
	}
	// The newest message's clock starts when it is first drawn.
	if got := frame(&toast, 11*time.Second+ToastFor-time.Millisecond); got != "three" {
		t.Fatalf("the newest message went with the old one's clock: %q", got)
	}
}

func TestDialogCloses(t *testing.T) {
	size := image.Pt(800, 600)
	click := func(p f32.Point) []event.Event {
		return []event.Event{
			pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p},
		}
	}
	for _, tt := range []struct {
		name   string
		events []event.Event
		open   bool
	}{
		{"escape", []event.Event{key.Event{Name: key.NameEscape, State: key.Press}}, false},
		{"click outside", click(f32.Pt(5, 5)), false},
		{"click inside", click(f32.Pt(400, 300)), true},
		{"other key", []event.Event{key.Event{Name: "A", State: key.Press}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var r input.Router
			var d Dialog
			d.Open("Plugin details", "clock@1.2.0", "line one\nline two")
			gtx := context(&r, size, time.Now())
			d.Layout(gtx, testTheme)
			r.Frame(gtx.Ops)
			for _, e := range tt.events {
				r.Queue(e)
			}
			gtx = context(&r, size, time.Now())
			d.Layout(gtx, testTheme)
			if d.IsOpen() != tt.open {
				t.Fatalf("open %t, want %t", d.IsOpen(), tt.open)
			}
		})
	}
}

func TestDialogOpensLongBodyAtTheEnd(t *testing.T) {
	var r input.Router
	lines := make([]string, 500)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	var d Dialog
	d.Open("Logs", "", strings.Join(lines, "\n"))
	d.Layout(context(&r, image.Pt(800, 600), time.Now()), testTheme)
	if pos := d.list.Position; pos.BeforeEnd || pos.First < 400 {
		t.Fatalf("list at %+v, want the last lines in view", pos)
	}
}

func TestDialogKeepsCloseInView(t *testing.T) {
	var r input.Router
	size := image.Pt(360, 240)
	long := strings.Repeat("a long heading that wraps ", 40)
	var d Dialog
	d.Open(long, long, strings.Repeat("line\n", 50))
	gtx := context(&r, size, time.Now())
	d.Layout(gtx, testTheme)
	r.Frame(gtx.Ops)
	// Close sits at the card's bottom right, 24 dp in from its edges.
	inner := context(&r, image.Pt(size.X*9/10-48, size.Y*9/10-48), time.Now())
	if h := d.content(inner, testTheme).Size.Y; h > inner.Constraints.Max.Y {
		t.Fatalf("content is %d px tall in %d px", h, inner.Constraints.Max.Y)
	}
}

func TestDialogFitsShortBody(t *testing.T) {
	var r input.Router
	gtx := context(&r, image.Pt(800, 600), time.Now())
	var d Dialog
	d.Open("Logs", "", "")
	empty := d.content(gtx, testTheme).Size.Y
	d.Open("Logs", "", "a\nb")
	short := d.content(gtx, testTheme).Size.Y
	if short-empty > 100 {
		t.Fatalf("a two-line body adds %d px, want it sized to its lines", short-empty)
	}
}
