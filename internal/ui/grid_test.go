// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"cmp"
	"errors"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
)

func panel(id string, x, y, w, h int) config.Panel {
	return config.Panel{ID: id, Plugin: id, X: x, Y: y, W: w, H: h}
}

func disabled(p config.Panel) config.Panel {
	p.Enabled = new(bool)
	return p
}

// dashboard is the mockup's layout, with Greeting across the top.
func dashboard() []config.Panel {
	return []config.Panel{
		panel("greeting", 0, 0, 4, 1),
		panel("clock", 0, 1, 1, 1),
		panel("compute", 1, 1, 2, 1),
		panel("disk", 3, 1, 1, 1),
		panel("processes", 0, 2, 2, 2),
		panel("calendar", 2, 2, 1, 2),
		panel("weather", 3, 2, 1, 1),
		panel("network", 3, 3, 1, 1),
	}
}

func cell(id string, x, y, w, h int) Cell { return Cell{ID: id, X: x, Y: y, W: w, H: h} }

func TestArrange(t *testing.T) {
	withoutGreeting := dashboard()
	withoutGreeting[0] = disabled(withoutGreeting[0])
	shuffled := dashboard()
	slices.Reverse(shuffled)
	column := []config.Panel{
		panel("a", 0, 0, 1, 1), panel("b", 0, 1, 1, 1), panel("c", 0, 2, 1, 1),
		panel("d", 1, 0, 1, 2), panel("e", 1, 2, 1, 1),
	}
	for _, tt := range []struct {
		name       string
		panels     []config.Panel
		configured int // the config's columns, if not 4
		columns    int
		want       []Cell
	}{
		{"mockup", dashboard(), 0, 4, []Cell{
			cell("greeting", 0, 0, 4, 1), cell("clock", 0, 1, 1, 1), cell("compute", 1, 1, 2, 1),
			cell("disk", 3, 1, 1, 1), cell("processes", 0, 2, 2, 2), cell("calendar", 2, 2, 1, 2),
			cell("weather", 3, 2, 1, 1), cell("network", 3, 3, 1, 1),
		}},
		{"config order does not matter", shuffled, 0, 4, []Cell{
			cell("greeting", 0, 0, 4, 1), cell("clock", 0, 1, 1, 1), cell("compute", 1, 1, 2, 1),
			cell("disk", 3, 1, 1, 1), cell("processes", 0, 2, 2, 2), cell("calendar", 2, 2, 1, 2),
			cell("weather", 3, 2, 1, 1), cell("network", 3, 3, 1, 1),
		}},
		{"disabled greeting", withoutGreeting, 0, 4, []Cell{
			cell("clock", 0, 0, 1, 1), cell("compute", 1, 0, 2, 1), cell("disk", 3, 0, 1, 1),
			cell("processes", 0, 1, 2, 2), cell("calendar", 2, 1, 1, 2),
			cell("weather", 3, 1, 1, 1), cell("network", 3, 2, 1, 1),
		}},
		{"removed from a column", slices.Delete(slices.Clone(column), 1, 2), 2, 2, []Cell{
			cell("a", 0, 0, 1, 1), cell("d", 1, 0, 1, 2), cell("c", 0, 1, 1, 1), cell("e", 1, 2, 1, 1),
		}},
		// below cannot rise past wide, which tall keeps from rising.
		{"never passes a panel above it", []config.Panel{
			panel("tall", 1, 0, 1, 2), panel("wide", 0, 2, 2, 1), panel("below", 0, 5, 1, 1),
		}, 2, 2, []Cell{
			cell("tall", 1, 0, 1, 2), cell("wide", 0, 2, 2, 1), cell("below", 0, 3, 1, 1),
		}},
		{"two columns", withoutGreeting, 0, 2, []Cell{
			cell("clock", 0, 0, 1, 1), cell("compute", 0, 1, 2, 1), cell("disk", 0, 2, 1, 1),
			cell("processes", 0, 3, 2, 2), cell("calendar", 0, 5, 1, 2),
			cell("weather", 1, 5, 1, 1), cell("network", 1, 6, 1, 1),
		}},
		{"one column", withoutGreeting, 0, 1, []Cell{
			cell("clock", 0, 0, 1, 1), cell("compute", 0, 1, 1, 1), cell("disk", 0, 2, 1, 1),
			cell("processes", 0, 3, 1, 2), cell("calendar", 0, 5, 1, 2),
			cell("weather", 0, 7, 1, 1), cell("network", 0, 8, 1, 1),
		}},
		{"full width in one column", []config.Panel{panel("wide", 0, 0, 4, 1), panel("next", 0, 1, 1, 1)}, 0, 1, []Cell{
			cell("wide", 0, 0, 1, 1), cell("next", 0, 1, 1, 1),
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Arrange(tt.panels, cmp.Or(tt.configured, 4), tt.columns)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got  %v\nwant %v", got, tt.want)
			}
			for i, c := range got {
				if c.X < 0 || c.X+c.W > tt.columns {
					t.Errorf("%s outside %d columns", c.ID, tt.columns)
				}
				for _, d := range got[i+1:] {
					if c.overlaps(d) {
						t.Errorf("%s overlaps %s", c.ID, d.ID)
					}
				}
			}
		})
	}
}

func TestColumns(t *testing.T) {
	for _, tt := range []struct {
		width      unit.Dp
		configured int
		want       int
	}{
		{1440, 4, 4}, {1001, 4, 4}, {1000, 4, 2},
		// No column narrower than minColumn: (1384+14)/(200+14) and
		// (1600+14)/(200+14), rounded down.
		{1001, 6, 4}, {1440, 24, 6}, {2400, 24, 7}, {1000, 1, 1}, {561, 4, 2}, {560, 4, 1}, {300, 4, 1},
	} {
		if got := Columns(tt.width, tt.configured); got != tt.want {
			t.Errorf("Columns(%v, %d) = %d, want %d", tt.width, tt.configured, got, tt.want)
		}
	}
}

func gridConfig(panels []config.Panel) config.Config {
	return config.Config{Version: 1, Grid: config.Grid{Columns: 4, RowHeight: 190}, Panels: panels}
}

func live(ids ...string) []pluginhost.PanelState {
	var out []pluginhost.PanelState
	for _, id := range ids {
		out = append(out, pluginhost.PanelState{ID: id, Plugin: id, State: pluginhost.Live})
	}
	return out
}

func TestGridTilesFit(t *testing.T) {
	var r input.Router
	cfg := gridConfig(dashboard())
	for _, scale := range []float32{1, 2} {
		for _, w := range []int{360, 560, 1000, 1440, 2400} {
			var g Grid
			gtx := context(&r, image.Pt(w, 900), time.Now())
			gtx.Metric.PxPerSp = scale
			g.Layout(gtx, testTheme, cfg, live())
			if len(g.placed) != len(cfg.Panels) {
				t.Fatalf("width %d: %d tiles, want %d", w, len(g.placed), len(cfg.Panels))
			}
			pad := gtx.Dp(contentPadding(unit.Dp(w)))
			var rects []image.Rectangle
			for id, rect := range g.placed {
				if rect.Min.X < pad || rect.Max.X > w-pad || rect.Min.Y < pad {
					t.Errorf("scale %v, width %d: %s at %v, outside the content area", scale, w, id, rect)
				}
				if rect.Dx() > gtx.Dp(1600) {
					t.Errorf("width %d: %s wider than 1600 dp", w, id)
				}
				for _, o := range rects {
					if rect.Overlaps(o) {
						t.Errorf("width %d: %s at %v overlaps %v", w, id, rect, o)
					}
				}
				rects = append(rects, rect)
			}
		}
	}
}

// The breakpoints are the window's width, as the mockup's @media rules,
// not the content area's, which is narrower by the padding.
func TestGridColumnsFollowWindow(t *testing.T) {
	var r input.Router
	cfg := gridConfig(dashboard())
	for _, tt := range []struct{ width, columns int }{{1001, 4}, {1000, 2}, {561, 2}, {560, 1}} {
		var g Grid
		g.Layout(context(&r, image.Pt(tt.width, 900), time.Now()), testTheme, cfg, live())
		starts := map[int]bool{}
		for _, rect := range g.placed {
			starts[rect.Min.X] = true
		}
		if len(starts) != tt.columns {
			t.Errorf("width %d: tiles start in %d columns, want %d", tt.width, len(starts), tt.columns)
		}
	}
}

func TestGridScrolls(t *testing.T) {
	var r input.Router
	var g Grid
	size := image.Pt(800, 400)
	cfg := gridConfig(dashboard())
	gtx := context(&r, size, time.Now())
	g.Layout(gtx, testTheme, cfg, live())
	r.Frame(gtx.Ops)
	top := g.placed["clock"].Min.Y
	r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(400, 200), Scroll: f32.Pt(0, 300)})
	gtx = context(&r, size, time.Now())
	g.Layout(gtx, testTheme, cfg, live())
	if moved := top - g.placed["clock"].Min.Y; moved != 300 {
		t.Fatalf("scrolled %d px, want 300", moved)
	}
	r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(400, 200), Scroll: f32.Pt(0, 1e5)})
	gtx = context(&r, size, time.Now())
	g.Layout(gtx, testTheme, cfg, live())
	last := 0
	for _, rect := range g.placed {
		last = max(last, rect.Max.Y)
	}
	if want := size.Y - gtx.Dp(contentPadding(800)); last != want {
		t.Errorf("scrolled to the end, the last tile ends at %d, want %d", last, want)
	}
}

func click(p image.Point) []event.Event {
	f := f32.Pt(float32(p.X), float32(p.Y))
	return []event.Event{
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f},
	}
}

// frame lays g out after queueing events, and returns its actions.
func frame(r *input.Router, g *Grid, cfg config.Config, states []pluginhost.PanelState, now time.Time, events ...event.Event) []Action {
	for _, e := range events {
		r.Queue(e)
	}
	gtx := context(r, image.Pt(1440, 900), now)
	_, actions := g.Layout(gtx, testTheme, cfg, states)
	r.Frame(gtx.Ops)
	return actions
}

func TestTileMenu(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("clock", 0, 0, 1, 1)})
	states := live("clock")
	for _, tt := range []struct {
		name   string
		then   func(menu image.Point) []event.Event
		open   bool
		action []Action
	}{
		{"configure", func(menu image.Point) []event.Event { return click(menu.Add(image.Pt(-40, 34))) }, false,
			[]Action{{ActionConfigure, "clock"}}},
		{"disable", func(menu image.Point) []event.Event { return click(menu.Add(image.Pt(-40, 66))) }, false,
			[]Action{{ActionDisable, "clock"}}},
		{"escape", func(image.Point) []event.Event {
			return []event.Event{key.Event{Name: key.NameEscape, State: key.Press}}
		}, false, nil},
		{"click outside", func(image.Point) []event.Event { return click(image.Pt(1300, 800)) }, false, nil},
		// The menu's top-right corner is 8 dp in from the tile's edge,
		// below the head; its padding is 6 dp.
		{"click on the menu's padding", func(menu image.Point) []event.Event {
			return click(menu.Add(image.Pt(16+13-8-3, -22+44+3)))
		}, true, nil},
		{"toggle", func(menu image.Point) []event.Event { return click(menu) }, false, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var r input.Router
			var g Grid
			now := time.Now()
			frame(&r, &g, cfg, states, now)
			rect := g.placed["clock"]
			menu := image.Pt(rect.Max.X-16-13, rect.Min.Y+22)
			frame(&r, &g, cfg, states, now, click(menu)...)
			if !g.tiles["clock"].open {
				t.Fatal("the menu did not open")
			}
			frame(&r, &g, cfg, states, now) // the menu is drawn
			actions := frame(&r, &g, cfg, states, now, tt.then(menu)...)
			actions = append(actions, frame(&r, &g, cfg, states, now)...)
			if g.tiles["clock"].open != tt.open {
				t.Errorf("open %t, want %t", g.tiles["clock"].open, tt.open)
			}
			if !slices.Equal(actions, tt.action) {
				t.Errorf("actions %v, want %v", actions, tt.action)
			}
		})
	}
}

// A dialog disables the grid, and the menu's backdrop must not stay
// over the dialog.
func TestTileMenuClosesWhenDisabled(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("clock", 0, 0, 1, 1)})
	states := live("clock")
	var r input.Router
	var g Grid
	now := time.Now()
	frame(&r, &g, cfg, states, now)
	rect := g.placed["clock"]
	frame(&r, &g, cfg, states, now, click(image.Pt(rect.Max.X-16-13, rect.Min.Y+22))...)
	if !g.tiles["clock"].open {
		t.Fatal("the menu did not open")
	}
	gtx := context(&r, image.Pt(1440, 900), now).Disabled()
	g.Layout(gtx, testTheme, cfg, states)
	if g.tiles["clock"].open {
		t.Error("the menu stayed open under a dialog")
	}
}

// A menu opened at the window's bottom edge moves up into view, and its
// items can be chosen there.
func TestTileMenuStaysInView(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("clock", 0, 0, 1, 1)})
	states := live("clock")
	var r input.Router
	var g Grid
	size := image.Pt(1440, 130)
	layout := func(events ...event.Event) []Action {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, size, time.Now())
		_, actions := g.Layout(gtx, testTheme, cfg, states)
		r.Frame(gtx.Ops)
		return actions
	}
	layout()
	rect := g.placed["clock"]
	layout(click(image.Pt(rect.Max.X-16-13, rect.Min.Y+22))...)
	layout()
	tl := g.tiles["clock"]
	if !tl.open {
		t.Fatal("the menu did not open")
	}
	box := tl.menuBox.Add(rect.Min)
	if box.Max.Y > size.Y || box.Min.Y < 0 {
		t.Fatalf("menu at %v, outside the %v window", box, size)
	}
	last := image.Pt(box.Min.X+20, box.Max.Y-6-8) // inside the last item
	actions := layout(click(last)...)
	actions = append(actions, layout()...)
	if !slices.Equal(actions, []Action{{ActionDisable, "clock"}}) {
		t.Errorf("actions %v, want Disable panel", actions)
	}
}

func TestErrorTileActions(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("weather", 0, 0, 2, 1)})
	cfg.Grid.RowHeight = 150 // the least config allows, which the buttons fit
	now := time.Now()
	states := []pluginhost.PanelState{{
		ID: "weather", Plugin: "weather", State: pluginhost.Restarting,
		Err: errors.New("plugin \"weather\" exited unexpectedly"), Failures: 2, MaxAttempts: 5,
		RestartAt: now.Add(7500 * time.Millisecond),
	}}
	var r input.Router
	var g Grid
	frame(&r, &g, cfg, states, now)
	rect := g.placed["weather"]
	// The buttons sit below the message, after the 32 dp symbol and its
	// 15 dp gap, inside the body's 16 dp padding.
	y := rect.Min.Y + 44 + 32 + 12 + 16
	restart := image.Pt(rect.Min.X+16+47+20, y)
	if got := frame(&r, &g, cfg, states, now, click(restart)...); !slices.Equal(got, []Action{{ActionRestart, "weather"}}) {
		t.Errorf("Restart: actions %v", got)
	}
	logs := image.Pt(rect.Min.X+16+47+80, y)
	if got := frame(&r, &g, cfg, states, now, click(logs)...); !slices.Equal(got, []Action{{ActionLogs, "weather"}}) {
		t.Errorf("Logs: actions %v", got)
	}
}

func TestRetryText(t *testing.T) {
	now := time.Now()
	s := pluginhost.PanelState{State: pluginhost.Restarting, Failures: 3, MaxAttempts: 5, RestartAt: now.Add(7500 * time.Millisecond)}
	next, text := retryText(s, now)
	if text != "Restarting in 8s — attempt 4/5" {
		t.Errorf("text %q", text)
	}
	if want := now.Add(500 * time.Millisecond); !next.Equal(want) {
		t.Errorf("next change at %v, want %v", next.Sub(now), want.Sub(now))
	}
	if _, text := retryText(s, next); !strings.HasPrefix(text, "Restarting in 7s") {
		t.Errorf("at the next change: %q", text)
	}
	s.State, s.Failures = pluginhost.Crashed, 5
	if _, text := retryText(s, now); text != "Gave up after 5 attempts" {
		t.Errorf("crashed: %q", text)
	}
}

func TestErrorTileButtonsFitLongError(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("weather", 0, 0, 1, 1)})
	states := []pluginhost.PanelState{{
		ID: "weather", Plugin: "weather", State: pluginhost.Crashed, Failures: 5, MaxAttempts: 5,
		Err: errors.New(strings.Repeat("plugin \"weather\" failed to start: fork/exec: no such file or directory ", 4)),
	}}
	// Large text widens the columns and heightens the rows to fit them,
	// even with many columns and the shortest rows, and a window too
	// narrow for them side by side stacks them in a taller row.
	for _, tt := range []struct {
		scale                     float32
		width, columns, rowHeight int
	}{
		{1, 1440, 4, 150}, {1.5, 1440, 4, 150}, {2, 1440, 4, 150},
		{1, 1440, 24, 150}, {1.5, 1440, 24, 150}, {2, 1440, 24, 150}, {2.5, 1440, 24, 150}, {3, 1440, 24, 150},
		{3, 360, 4, 150}, {4, 360, 4, 150},
	} {
		scale := tt.scale
		cfg.Grid.Columns, cfg.Grid.RowHeight = tt.columns, tt.rowHeight
		var r input.Router
		var g Grid
		gtx := context(&r, image.Pt(tt.width, 900), time.Now())
		gtx.Metric.PxPerSp = scale
		g.Layout(gtx, testTheme, cfg, states)
		// Inside the tile's 16 dp side and bottom padding.
		size := g.placed["weather"].Size()
		body := image.Rect(16, 0, size.X-16, size.Y-16)
		if b := g.tiles["weather"].buttons; b.Empty() || !b.In(body) {
			t.Errorf("text ×%v, %d wide, %d columns: buttons at %v, outside the body %v", scale, tt.width, tt.columns, b, body)
		}
	}
}

func TestGridTinyWindow(t *testing.T) {
	for _, size := range []image.Point{{}, {10, 10}} {
		var r input.Router
		var g Grid
		g.Layout(context(&r, size, time.Now()), testTheme, gridConfig(dashboard()), live())
		for id, rect := range g.placed {
			if rect.Dx() < 0 || rect.Dy() < 0 {
				t.Errorf("%v window: %s at %v", size, id, rect)
			}
		}
	}
}

// Opening a tile's menu from the keyboard, while another tile's menu is
// open, closes that one.
func TestTileMenuOneAtATime(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("clock", 0, 0, 1, 1), panel("disk", 1, 0, 1, 1)})
	states := live("clock", "disk")
	var r input.Router
	var g Grid
	now := time.Now()
	frame(&r, &g, cfg, states, now)
	rect := g.placed["clock"]
	frame(&r, &g, cfg, states, now, click(image.Pt(rect.Max.X-16-13, rect.Min.Y+22))...)
	frame(&r, &g, cfg, states, now)
	gtx := context(&r, image.Pt(1440, 900), now)
	gtx.Execute(key.FocusCmd{Tag: &g.tiles["disk"].menu.Clickable})
	g.Layout(gtx, testTheme, cfg, states)
	r.Frame(gtx.Ops)
	frame(&r, &g, cfg, states, now, key.Event{Name: key.NameReturn, State: key.Press}, key.Event{Name: key.NameReturn, State: key.Release})
	frame(&r, &g, cfg, states, now)
	if !g.tiles["disk"].open || g.tiles["clock"].open {
		t.Errorf("disk open %t, clock open %t; want only disk", g.tiles["disk"].open, g.tiles["clock"].open)
	}
}

// The plugin popover stays up while the pointer moves from the title
// onto ⋯, as hovering anywhere on the head shows it.
func TestPluginPopoverOnMenuButton(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("clock", 0, 0, 1, 1)})
	states := live("clock")
	var r input.Router
	var g Grid
	now := time.Now()
	frame(&r, &g, cfg, states, now)
	rect := g.placed["clock"]
	for _, at := range []image.Point{{rect.Min.X + 30, rect.Min.Y + 22}, {rect.Max.X - 16 - 13, rect.Min.Y + 22}} {
		frame(&r, &g, cfg, states, now, pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(at.X), float32(at.Y))})
		frame(&r, &g, cfg, states, now)
		if g.tiles["clock"].infoBox.Empty() {
			t.Errorf("pointer at %v: no plugin popover", at)
		}
	}
}

// A narrow tile in the first column keeps its popovers inside the
// window, at large text too.
func TestTilePopoversStayInViewSideways(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("net", 0, 0, 1, 1)})
	cfg.Grid.Columns = 24
	states := []pluginhost.PanelState{{ID: "net", Plugin: "system-stats-network-monitor-with-a-long-name", State: pluginhost.Starting}}
	// At 400 wide the head is narrow; at both, large text makes it taller,
	// and the popovers keep below its ⋯ button.
	for _, size := range []image.Point{{1440, 900}, {400, 900}} {
		var r input.Router
		var g Grid
		layout := func(events ...event.Event) {
			for _, e := range events {
				r.Queue(e)
			}
			gtx := context(&r, size, time.Now())
			gtx.Metric.PxPerSp = 3
			g.Layout(gtx, testTheme, cfg, states)
			r.Frame(gtx.Ops)
		}
		layout()
		rect := g.placed["net"]
		head := f32.Pt(float32(rect.Min.X+20), float32(rect.Min.Y+10))
		layout(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: head})
		layout()
		window := image.Rectangle{Max: size}
		tl := g.tiles["net"]
		if box := tl.infoBox.Add(rect.Min); box.Empty() || !box.In(window) {
			t.Errorf("%v: plugin popover at %v, outside the window", size, box)
		}
		if tl.infoBox.Min.Y < tl.menuEnd {
			t.Errorf("%v: plugin popover top %d, over the ⋯ button ending at %d", size, tl.infoBox.Min.Y, tl.menuEnd)
		}
		headH := tl.headSize.Y
		layout(click(image.Pt(rect.Max.X-16-13, rect.Min.Y+headH/2))...)
		layout()
		if !tl.open {
			t.Fatalf("%v: the menu did not open", size)
		}
		if box := tl.menuBox.Add(rect.Min); !box.In(window) {
			t.Errorf("%v: menu at %v, outside the window", size, box)
		}
		if tl.menuBox.Min.Y < headH {
			t.Errorf("%v: menu top %d, over the %d px head", size, tl.menuBox.Min.Y, headH)
		}
	}
}
