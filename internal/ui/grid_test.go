// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
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
		name    string
		panels  []config.Panel
		columns int
		want    []Cell
	}{
		{"mockup", dashboard(), 4, []Cell{
			cell("greeting", 0, 0, 4, 1), cell("clock", 0, 1, 1, 1), cell("compute", 1, 1, 2, 1),
			cell("disk", 3, 1, 1, 1), cell("processes", 0, 2, 2, 2), cell("calendar", 2, 2, 1, 2),
			cell("weather", 3, 2, 1, 1), cell("network", 3, 3, 1, 1),
		}},
		{"config order does not matter", shuffled, 4, []Cell{
			cell("greeting", 0, 0, 4, 1), cell("clock", 0, 1, 1, 1), cell("compute", 1, 1, 2, 1),
			cell("disk", 3, 1, 1, 1), cell("processes", 0, 2, 2, 2), cell("calendar", 2, 2, 1, 2),
			cell("weather", 3, 2, 1, 1), cell("network", 3, 3, 1, 1),
		}},
		{"disabled greeting", withoutGreeting, 4, []Cell{
			cell("clock", 0, 0, 1, 1), cell("compute", 1, 0, 2, 1), cell("disk", 3, 0, 1, 1),
			cell("processes", 0, 1, 2, 2), cell("calendar", 2, 1, 1, 2),
			cell("weather", 3, 1, 1, 1), cell("network", 3, 2, 1, 1),
		}},
		{"removed from a column", slices.Delete(slices.Clone(column), 1, 2), 2, []Cell{
			cell("a", 0, 0, 1, 1), cell("d", 1, 0, 1, 2), cell("c", 0, 1, 1, 1), cell("e", 1, 2, 1, 1),
		}},
		{"two columns", withoutGreeting, 2, []Cell{
			cell("clock", 0, 0, 1, 1), cell("compute", 0, 1, 2, 1), cell("disk", 0, 2, 1, 1),
			cell("processes", 0, 3, 2, 2), cell("calendar", 0, 5, 1, 2),
			cell("weather", 1, 5, 1, 1), cell("network", 1, 6, 1, 1),
		}},
		{"one column", withoutGreeting, 1, []Cell{
			cell("clock", 0, 0, 1, 1), cell("compute", 0, 1, 1, 1), cell("disk", 0, 2, 1, 1),
			cell("processes", 0, 3, 1, 2), cell("calendar", 0, 5, 1, 2),
			cell("weather", 0, 7, 1, 1), cell("network", 0, 8, 1, 1),
		}},
		{"full width in one column", []config.Panel{panel("wide", 0, 0, 4, 1), panel("next", 0, 1, 1, 1)}, 1, []Cell{
			cell("wide", 0, 0, 1, 1), cell("next", 0, 1, 1, 1),
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Arrange(tt.panels, 4, tt.columns)
			if tt.name == "removed from a column" {
				got = Arrange(tt.panels, 2, tt.columns)
			}
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
		{1440, 4, 4}, {1001, 6, 6}, {1000, 4, 2}, {1000, 1, 1}, {561, 4, 2}, {560, 4, 1}, {300, 4, 1},
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

func TestErrorTileActions(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("weather", 0, 0, 2, 1)})
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
