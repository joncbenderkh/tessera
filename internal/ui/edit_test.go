// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
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
	"gioui.org/op/clip"

	"github.com/joncbenderkh/tessera/internal/config"
)

func pt(p image.Point) f32.Point { return f32.Pt(float32(p.X), float32(p.Y)) }

// drag presses at from, moves to to, and releases there.
func drag(from, to image.Point) []event.Event {
	return []event.Event{
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pt(from)},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pt(to)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pt(to)},
	}
}

// editGrid is a grid in edit mode, laid out once in a 1440 by 900 window,
// whose 4 columns are 335 px wide and 14 px apart, and rows 190 px tall.
func editGrid(r *input.Router, cfg config.Config) *Grid {
	g := &Grid{Edit: true}
	frame(r, g, cfg, live("a", "b"), time.Now())
	return g
}

func TestEditDragMoves(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1), panel("b", 1, 0, 1, 1)})
	g := editGrid(&r, cfg)
	if !g.Movable() {
		t.Fatal("not movable in edit mode with all columns")
	}
	head := g.placed["a"].Min.Add(image.Pt(60, 22))
	// Two columns right and a row down, less than half a cell off.
	to := head.Add(image.Pt(2*349+100, 204+60))
	actions := frame(&r, g, cfg, live("a", "b"), time.Now(), drag(head, to)...)
	want := []Action{{Kind: ActionPlace, Panel: "a", Cell: cell("a", 2, 1, 1, 1)}}
	if !slices.Equal(actions, want) {
		t.Errorf("actions %v, want %v", actions, want)
	}
}

func TestEditDragShowsWhereTheTileGoes(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	g := editGrid(&r, cfg)
	head := g.placed["a"].Min.Add(image.Pt(60, 22))
	events := drag(head, head.Add(image.Pt(349, 0)))
	frame(&r, g, cfg, live("a"), time.Now(), events[:2]...)
	if tl := g.tiles["a"]; tl.drag != dragMove || dragTarget(cell("a", 0, 0, 1, 1), tl.drag, tl.dragBy, image.Pt(349, 204), 4) != cell("a", 1, 0, 1, 1) {
		t.Errorf("dragging: %v by %v, want a move one column right", tl.drag, tl.dragBy)
	}
}

// The preview shows where Place puts the tile, not the cell under it.
func TestLanding(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1), panel("b", 0, 1, 1, 3)})
	for _, tc := range []struct {
		to, want Cell
		refused  bool
	}{
		{cell("a", 0, 2, 1, 1), cell("a", 0, 3, 1, 1), false},    // onto b: below it
		{cell("a", 1, 5, 1, 1), cell("a", 1, 0, 1, 1), false},    // into a free column: rises
		{cell("a", 0, 2, 1, 999), cell("a", 0, 2, 1, 999), true}, // past the last row: as dragged, refused
	} {
		if got, refused := landing(cfg, tc.to); got != tc.want || refused != tc.refused {
			t.Errorf("landing(%v) = %v, %t, want %v, %t", tc.to, got, refused, tc.want, tc.refused)
		}
	}
	if cfg.Panels[0].Y != 0 || cfg.Panels[1].Y != 1 {
		t.Errorf("cfg changed: %v", cfg.Panels)
	}
}

// The handle covers none of the body: a drag on its last pixel does not
// resize the panel.
func TestEditResizeHandleLeavesTheBody(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	g := editGrid(&r, cfg)
	body := g.placed["a"].Max.Sub(image.Pt(17, 17))
	if actions := frame(&r, g, cfg, live("a"), time.Now(), drag(body, body.Add(image.Pt(349, 204)))...); len(actions) != 0 {
		t.Errorf("actions %v, want none", actions)
	}
}

// touchDrag is a finger moved from one point to another in steps, each
// read in a frame of its own.
func touchDrag(from, to image.Point) []event.Event {
	ev := []event.Event{pointer.Event{Kind: pointer.Press, Source: pointer.Touch, Position: pt(from)}}
	for i := 1; i <= 10; i++ {
		p := from.Add(to.Sub(from).Mul(i).Div(10))
		ev = append(ev, pointer.Event{Kind: pointer.Move, Source: pointer.Touch, Position: pt(p)})
	}
	return append(ev, pointer.Event{Kind: pointer.Release, Source: pointer.Touch, Position: pt(to)})
}

func TestEditTouchDrag(t *testing.T) {
	for _, tc := range []struct {
		name     string
		from, by image.Point
		want     Cell
	}{
		{"move", image.Pt(60, 22), image.Pt(2*349+100, 204+60), cell("a", 2, 1, 1, 1)},
		{"resize", image.Pt(335-9, 190-9), image.Pt(349, 204), cell("a", 0, 0, 2, 2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r input.Router
			cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1), panel("b", 1, 0, 1, 1)})
			g := editGrid(&r, cfg)
			// The grid's scroll must not take a drag that moves down.
			from := g.placed["a"].Min.Add(tc.from)
			var actions []Action
			for _, e := range touchDrag(from, from.Add(tc.by)) {
				actions = append(actions, frame(&r, g, cfg, live("a", "b"), time.Now(), e)...)
			}
			want := []Action{{Kind: ActionPlace, Panel: "a", Cell: tc.want}}
			if !slices.Equal(actions, want) {
				t.Errorf("actions %v, want %v", actions, want)
			}
		})
	}
}

func TestEditEscapeEndsADrag(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1), panel("b", 1, 0, 1, 1)})
	g := editGrid(&r, cfg)
	head := g.placed["a"].Min.Add(image.Pt(60, 22))
	to := head.Add(image.Pt(2*349+100, 0))
	ev := drag(head, to)
	frame(&r, g, cfg, live("a", "b"), time.Now(), ev[:2]...)
	frame(&r, g, cfg, live("a", "b"), time.Now(), key.Event{Name: key.NameEscape, State: key.Press})
	if actions := frame(&r, g, cfg, live("a", "b"), time.Now(), ev[2]); len(actions) != 0 {
		t.Errorf("drop after Escape: actions %v, want none", actions)
	}
	want := []Action{{Kind: ActionPlace, Panel: "a", Cell: cell("a", 2, 0, 1, 1)}}
	if actions := frame(&r, g, cfg, live("a", "b"), time.Now(), drag(head, to)...); !slices.Equal(actions, want) {
		t.Errorf("next drag: actions %v, want %v", actions, want)
	}
}

func TestEditDragResizes(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	g := editGrid(&r, cfg)
	corner := g.placed["a"].Max.Sub(image.Pt(9, 9))
	actions := frame(&r, g, cfg, live("a"), time.Now(), drag(corner, corner.Add(image.Pt(349, 204)))...)
	want := []Action{{Kind: ActionPlace, Panel: "a", Cell: cell("a", 0, 0, 2, 2)}}
	if !slices.Equal(actions, want) {
		t.Errorf("actions %v, want %v", actions, want)
	}
}

func TestEditClickIsNoMove(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	g := editGrid(&r, cfg)
	head := g.placed["a"].Min.Add(image.Pt(60, 22))
	if actions := frame(&r, g, cfg, live("a"), time.Now(), click(head)...); len(actions) != 0 {
		t.Errorf("actions %v, want none", actions)
	}
}

func TestEditKeys(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	g := editGrid(&r, cfg)
	// A click on the head focuses it.
	frame(&r, g, cfg, live("a"), time.Now(), click(g.placed["a"].Min.Add(image.Pt(60, 22)))...)
	actions := frame(&r, g, cfg, live("a"), time.Now(),
		key.Event{Name: key.NameDownArrow, State: key.Press},
		key.Event{Name: key.NameRightArrow, State: key.Press, Modifiers: key.ModShift})
	want := []Action{
		{Kind: ActionStep, Panel: "a", Step: image.Pt(0, 1)},
		{Kind: ActionResize, Panel: "a", Step: image.Pt(1, 0)},
	}
	if !slices.Equal(actions, want) {
		t.Errorf("actions %v, want %v", actions, want)
	}
}

// A menu over the grid, opened by a click, leaves the head focused but
// takes the arrow keys from it.
func TestEditKeysCovered(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	g := editGrid(&r, cfg)
	frame(&r, g, cfg, live("a"), time.Now(), click(g.placed["a"].Min.Add(image.Pt(60, 22)))...)
	down := key.Event{Name: key.NameDownArrow, State: key.Press}
	g.Covered = true
	frame(&r, g, cfg, live("a"), time.Now())
	if actions := frame(&r, g, cfg, live("a"), time.Now(), down); len(actions) != 0 {
		t.Errorf("covered: actions %v, want none", actions)
	}
	g.Covered = false
	frame(&r, g, cfg, live("a"), time.Now())
	want := []Action{{Kind: ActionStep, Panel: "a", Step: image.Pt(0, 1)}}
	if actions := frame(&r, g, cfg, live("a"), time.Now(), down); !slices.Equal(actions, want) {
		t.Errorf("uncovered: actions %v, want %v", actions, want)
	}
}

// A panel the keyboard moves out of sight is scrolled into view, and
// keeps the focus.
func TestEditKeysFollowThePanel(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1), panel("b", 0, 1, 1, 10)})
	g := editGrid(&r, cfg)
	frame(&r, g, cfg, live("a", "b"), time.Now(), click(g.placed["a"].Min.Add(image.Pt(60, 22)))...)
	actions := frame(&r, g, cfg, live("a", "b"), time.Now(), key.Event{Name: key.NameDownArrow, State: key.Press})
	if len(actions) != 1 {
		t.Fatalf("actions %v, want a step", actions)
	}
	if err := cfg.Step("a", 0, 1); err != nil { // as the host does
		t.Fatal(err)
	}
	frame(&r, g, cfg, live("a", "b"), time.Now())
	if a := g.placed["a"]; !a.In(image.Rect(0, 0, 1440, 900)) {
		t.Errorf("a at %v, out of the window's view", a)
	}
	actions = frame(&r, g, cfg, live("a", "b"), time.Now(), key.Event{Name: key.NameUpArrow, State: key.Press})
	if want := []Action{{Kind: ActionStep, Panel: "a", Step: image.Pt(0, -1)}}; !slices.Equal(actions, want) {
		t.Errorf("actions after the move %v, want %v", actions, want)
	}
}

// A drag whose release came while the window took no input, as with a
// dialog open, ends.
func TestEditDragEndsWhenDisabled(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	g := editGrid(&r, cfg)
	head := g.placed["a"].Min.Add(image.Pt(60, 22))
	frame(&r, g, cfg, live("a"), time.Now(), drag(head, head.Add(image.Pt(349, 0)))[:2]...)
	if g.tiles["a"].drag != dragMove {
		t.Fatal("not dragging")
	}
	gtx := context(&r, image.Pt(1440, 900), time.Now()).Disabled()
	g.Layout(gtx, testTheme, cfg, live("a"))
	r.Frame(gtx.Ops)
	if g.tiles["a"].drag != dragNone {
		t.Error("still dragging after a disabled frame")
	}
	// The release went by while disabled; the next drag still moves it.
	gtx = context(&r, image.Pt(1440, 900), time.Now()).Disabled()
	r.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pt(head)})
	g.Layout(gtx, testTheme, cfg, live("a"))
	r.Frame(gtx.Ops)
	frame(&r, g, cfg, live("a"), time.Now()) // the dialog closed
	actions := frame(&r, g, cfg, live("a"), time.Now(), drag(head, head.Add(image.Pt(349, 0)))...)
	if want := []Action{{Kind: ActionPlace, Panel: "a", Cell: cell("a", 1, 0, 1, 1)}}; !slices.Equal(actions, want) {
		t.Errorf("next drag: actions %v, want %v", actions, want)
	}
}

// Each arrow key is an action of its own, in the order pressed.
func TestEditKeysInTurn(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	g := editGrid(&r, cfg)
	frame(&r, g, cfg, live("a"), time.Now(), click(g.placed["a"].Min.Add(image.Pt(60, 22)))...)
	actions := frame(&r, g, cfg, live("a"), time.Now(),
		key.Event{Name: key.NameRightArrow, State: key.Press, Modifiers: key.ModShift},
		key.Event{Name: key.NameDownArrow, State: key.Press},
		key.Event{Name: key.NameDownArrow, State: key.Press})
	want := []Action{
		{Kind: ActionResize, Panel: "a", Step: image.Pt(1, 0)},
		{Kind: ActionStep, Panel: "a", Step: image.Pt(0, 1)},
		{Kind: ActionStep, Panel: "a", Step: image.Pt(0, 1)},
	}
	if !slices.Equal(actions, want) {
		t.Errorf("actions %v, want %v", actions, want)
	}
}

// With fewer columns than the config's, the cells shown are not the
// config's, so nothing moves.
func TestEditNarrowDoesNotMove(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	g := &Grid{Edit: true}
	layoutAt := func(events ...event.Event) []Action {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(900, 900), time.Now())
		_, actions := g.Layout(gtx, testTheme, cfg, live("a"))
		r.Frame(gtx.Ops)
		return actions
	}
	layoutAt()
	if g.Movable() {
		t.Fatal("movable with 2 of the config's 4 columns")
	}
	head := g.placed["a"].Min.Add(image.Pt(60, 22))
	if actions := layoutAt(drag(head, head.Add(image.Pt(400, 0)))...); len(actions) != 0 {
		t.Errorf("actions %v, want none", actions)
	}
}

func TestEditBar(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1), disabled(panel("notes", 1, 0, 1, 1))})
	plugins := []string{"clock", "notes"}
	var r input.Router
	var b EditBar
	layoutBar := func(events ...event.Event) []Action {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(1440, 900), time.Now())
		_, actions := b.Layout(gtx, testTheme, cfg, plugins, HintMove)
		r.Frame(gtx.Ops)
		return actions
	}
	layoutBar()
	if len(b.boxes) != 2 {
		t.Fatalf("%d buttons, want Enable notes and Add panel", len(b.boxes))
	}
	center := func(r image.Rectangle) image.Point { return r.Min.Add(r.Size().Div(2)) }
	if actions := layoutBar(click(center(b.boxes[0]))...); !slices.Equal(actions, []Action{{Kind: ActionEnable, Panel: "notes"}}) {
		t.Errorf("Enable: actions %v", actions)
	}
	// A click leaves the focus alone.
	cfg.Panels[1].Enabled = nil
	if layoutBar(); r.Source().Focused(&b.add.button.Clickable) {
		t.Error("Add panel focused after a click on Enable")
	}
	if layoutBar(click(center(b.boxes[len(b.boxes)-1]))...); !b.MenuOpened() {
		t.Error("opening Add panel's menu not reported")
	}
	layoutBar() // the menu is drawn
	if !b.Adding() || b.add.box.Empty() {
		t.Fatal("Add panel did not open its menu")
	}
	// The second item, below the first; items are 32 px tall below 6 px
	// of padding.
	item := b.add.box.Min.Add(image.Pt(20, 6+32+16))
	if actions := layoutBar(click(item)...); !slices.Equal(actions, []Action{{Kind: ActionAdd, Plugin: "notes"}}) {
		t.Errorf("Add: actions %v", actions)
	}
	if b.add.open {
		t.Error("the menu stayed open")
	}
	if layoutBar(); r.Source().Focused(&b.add.button.Clickable) {
		t.Error("Add panel focused after a click on an item")
	}
	// Nor after a click on the first item, which a click does not focus.
	layoutBar(click(center(b.boxes[len(b.boxes)-1]))...)
	layoutBar()
	first := b.add.box.Min.Add(image.Pt(20, 6+16))
	if actions := layoutBar(click(first)...); !slices.Equal(actions, []Action{{Kind: ActionAdd, Plugin: "clock"}}) {
		t.Errorf("Add first: actions %v", actions)
	}
	if layoutBar(); r.Source().Focused(&b.add.button.Clickable) {
		t.Error("Add panel focused after a click on the first item")
	}
	// Chosen with the keyboard, the focus goes back to Add panel….
	r.Source().Execute(key.FocusCmd{Tag: &b.add.button.Clickable})
	layoutBar(pressKey(key.NameReturn)...)
	layoutBar() // the menu is drawn, its first item focused
	if actions := layoutBar(pressKey(key.NameReturn)...); !slices.Equal(actions, []Action{{Kind: ActionAdd, Plugin: "clock"}}) {
		t.Errorf("Add by key: actions %v", actions)
	}
	if layoutBar(); !r.Source().Focused(&b.add.button.Clickable) {
		t.Error("Add panel not focused after a choice by key")
	}
	layoutBar(click(center(b.boxes[len(b.boxes)-1]))...)
	b.Close()
	if layoutBar(); b.add.open || !b.add.box.Empty() {
		t.Error("the menu is open after Close")
	}
}

// pressKey is a press and release of the key name.
func pressKey(name key.Name) []event.Event {
	return []event.Event{key.Event{Name: name, State: key.Press}, key.Event{Name: name, State: key.Release}}
}

// The button that takes the place of an Enable button that went takes
// its focus, though Enable… goes too.
func TestEditBarEnableKeepsTheFocus(t *testing.T) {
	var panels []config.Panel
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		panels = append(panels, disabled(panel(id, 0, 0, 1, 1)))
	}
	cfg := gridConfig(panels)
	var r input.Router
	var b EditBar
	layoutBar := func(events ...event.Event) []Action {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(1440, 900), time.Now())
		_, actions := b.Layout(gtx, testTheme, cfg, []string{"clock"}, HintMove)
		r.Frame(gtx.Ops)
		return actions
	}
	layoutBar()
	// Enable c, the last inline one, before Enable… for d and e, by key.
	r.Source().Execute(key.FocusCmd{Tag: &b.enable["c"].Clickable})
	if actions := layoutBar(pressKey(key.NameReturn)...); !slices.Equal(actions, []Action{{Kind: ActionEnable, Panel: "c"}}) {
		t.Fatalf("actions %v", actions)
	}
	cfg.Panels[2].Enabled = nil
	layoutBar()
	if !r.Source().Focused(&b.enable["d"].Clickable) {
		t.Error("Enable d, now where Enable c was, not focused")
	}
}

// A choice from Enable… by key leaves the focus on Enable…, or on Add
// panel once Enable… goes with it.
func TestEditBarEnableMenuKeepsTheFocus(t *testing.T) {
	var panels []config.Panel
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		panels = append(panels, disabled(panel(id, 0, 0, 1, 1)))
	}
	cfg := gridConfig(panels)
	var r input.Router
	var b EditBar
	layoutBar := func(events ...event.Event) []Action {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(1440, 900), time.Now())
		_, actions := b.Layout(gtx, testTheme, cfg, []string{"clock"}, HintMove)
		r.Frame(gtx.Ops)
		return actions
	}
	layoutBar()
	r.Source().Execute(key.FocusCmd{Tag: &b.more.button.Clickable})
	layoutBar(pressKey(key.NameReturn)...)
	layoutBar()
	if !b.more.open {
		t.Fatal("Enable… did not open its menu")
	}
	if actions := layoutBar(pressKey(key.NameReturn)...); !slices.Equal(actions, []Action{{Kind: ActionEnable, Panel: "d"}}) {
		t.Fatalf("actions %v", actions)
	}
	cfg.Panels[3].Enabled = nil
	layoutBar()
	if !r.Source().Focused(&b.add.button.Clickable) {
		t.Error("Add panel… not focused once Enable… went")
	}
}

// A list that changes while its menu is open, as when Add panel… lists
// the plugins anew, keeps the focus in the menu.
func TestEditBarMenuKeepsTheFocusOnANewList(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	plugins := []string{"clock"}
	var r input.Router
	var b EditBar
	layoutBar := func(events ...event.Event) []Action {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(1440, 900), time.Now())
		_, actions := b.Layout(gtx, testTheme, cfg, plugins, HintMove)
		r.Frame(gtx.Ops)
		return actions
	}
	layoutBar()
	r.Source().Execute(key.FocusCmd{Tag: &b.add.button.Clickable})
	layoutBar(pressKey(key.NameReturn)...)
	layoutBar() // the menu is drawn, its first item focused
	plugins = []string{"clock", "notes"}
	layoutBar()
	if actions := layoutBar(pressKey(key.NameReturn)...); !slices.Equal(actions, []Action{{Kind: ActionAdd, Plugin: "clock"}}) {
		t.Errorf("actions %v, want clock added", actions)
	}
}

// A menu scrolled down and closed opens again at the top: by a key,
// with its first item focused and in view, so Return chooses it.
func TestEditBarMenuReopensAtTheTop(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	var plugins []string
	for i := range 40 {
		plugins = append(plugins, fmt.Sprintf("plugin%02d", i))
	}
	var r input.Router
	var b EditBar
	layoutBar := func(events ...event.Event) []Action {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(1440, 400), time.Now())
		_, actions := b.Layout(gtx, testTheme, cfg, plugins, HintMove)
		r.Frame(gtx.Ops)
		return actions
	}
	layoutBar()
	r.Source().Execute(key.FocusCmd{Tag: &b.add.button.Clickable})
	layoutBar(pressKey(key.NameReturn)...)
	layoutBar()
	if !b.add.open {
		t.Fatal("Add panel… did not open its menu")
	}
	b.add.list.Position.First = 30 // scrolled down
	layoutBar()
	r.Source().Execute(key.FocusCmd{Tag: &b.add.button.Clickable})
	layoutBar(pressKey(key.NameReturn)...) // closes it
	if b.add.open {
		t.Fatal("Add panel… did not close")
	}
	layoutBar(pressKey(key.NameReturn)...) // opens it again
	layoutBar()
	if actions := layoutBar(pressKey(key.NameReturn)...); !slices.Equal(actions, []Action{{Kind: ActionAdd, Plugin: "plugin00"}}) {
		t.Errorf("actions %v, want plugin00 added", actions)
	}

	// Opened by a click, it starts at the top too.
	b.add.open, b.add.list.Position.First = false, 30
	r.Source().Execute(key.FocusCmd{}) // a click does not focus the button
	layoutBar()
	layoutBar(click(b.add.anchor.Min.Add(b.add.anchor.Size().Div(2)))...)
	layoutBar()
	if !b.add.open || b.add.list.Position.First != 0 {
		t.Errorf("clicked open %t at item %d, want open at 0", b.add.open, b.add.list.Position.First)
	}
}

// A layout wider than any window is not one a wider window lets move.
func TestEditHint(t *testing.T) {
	for _, tc := range []struct {
		columns int
		width   int
		scale   float32
		want    string
	}{
		{4, 1440, 1, HintMove},
		{4, 900, 1, HintNarrow},
		{7, 1440, 1, HintNarrow},
		{8, 3000, 1, HintWide},
		{7, 3000, 2, HintWide}, // large text widens the columns
	} {
		cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
		cfg.Grid.Columns = tc.columns
		var r input.Router
		// Laid out before edit mode, as the frame it starts in finds it.
		g := &Grid{}
		gtx := context(&r, image.Pt(tc.width, 900), time.Now())
		gtx.Metric.PxPerSp = tc.scale
		g.Layout(gtx, testTheme, cfg, live("a"))
		g.Edit = true
		if got := g.EditHint(); got != tc.want {
			t.Errorf("%d columns, %d px, text ×%v: %q, want %q", tc.columns, tc.width, tc.scale, got, tc.want)
		}
	}
}

// A long panel id is cut short, so its button stays in the bar.
func TestEditBarLongID(t *testing.T) {
	cfg := gridConfig([]config.Panel{disabled(panel(strings.Repeat("long-", 40), 0, 0, 1, 1))})
	var r input.Router
	var b EditBar
	gtx := context(&r, image.Pt(400, 900), time.Now())
	b.Layout(gtx, testTheme, cfg, nil, HintMove)
	for i, box := range b.boxes {
		if box.Min.X < 0 || box.Max.X > 400 {
			t.Errorf("button %d at %v, outside the 400 px window", i, box)
		}
	}
}

// A tile's menu opening is reported, so the bar's can close, and
// CloseMenus closes it when one of the bar's opens.
func TestGridMenuOpened(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("clock", 0, 0, 1, 1)})
	var r input.Router
	var g Grid
	frame(&r, &g, cfg, live("clock"), time.Now())
	rect := g.placed["clock"]
	frame(&r, &g, cfg, live("clock"), time.Now(), click(image.Pt(rect.Max.X-16-13, rect.Min.Y+22))...)
	if !g.MenuOpened() || !g.tiles["clock"].open {
		t.Fatal("the menu's opening not reported")
	}
	if frame(&r, &g, cfg, live("clock"), time.Now()); g.MenuOpened() {
		t.Error("reported again on the next frame")
	}
	g.CloseMenus()
	if g.tiles["clock"].open {
		t.Error("open after CloseMenus")
	}
}

// In the smallest window, the buttons keep to one line, the Enable
// buttons that do not fit going into Enable…, and the hint to one, so
// the grid keeps room.
func TestEditBarSmallWindow(t *testing.T) {
	var panels []config.Panel
	for _, id := range []string{"clock", "weather", "notes", "disk"} {
		panels = append(panels, disabled(panel(id, 0, 0, 1, 1)))
	}
	cfg := gridConfig(panels)
	var r input.Router
	var b EditBar
	gtx := context(&r, image.Pt(360, 184), time.Now()) // 240 dp less the header
	dims, _ := b.Layout(gtx, testTheme, cfg, nil, HintMove)
	for i, box := range b.boxes {
		if box.Min.Y != b.boxes[0].Min.Y || box.Max.X > 360 {
			t.Errorf("button %d at %v, not on the first line in the window", i, box)
		}
	}
	if len(b.boxes) > 4 {
		t.Errorf("%d buttons, want some Enable buttons in Enable…", len(b.boxes))
	}
	if h := dims.Size.Y; h > 120 {
		t.Errorf("bar %d px high, want room for the grid", h)
	}
}

// Past a few disabled panels, the rest are in Enable…'s menu.
func TestEditBarEnableMenu(t *testing.T) {
	var panels []config.Panel
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		panels = append(panels, disabled(panel(id, 0, 0, 1, 1)))
	}
	cfg := gridConfig(panels)
	var r input.Router
	var b EditBar
	layoutBar := func(events ...event.Event) []Action {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(1440, 900), time.Now())
		_, actions := b.Layout(gtx, testTheme, cfg, []string{"clock"}, HintMove)
		r.Frame(gtx.Ops)
		return actions
	}
	layoutBar()
	// Enable a, b and c, Enable…, and Add panel.
	if len(b.boxes) != inlineEnable+1 {
		t.Fatalf("%d buttons, want %d", len(b.boxes), inlineEnable+1)
	}
	center := func(r image.Rectangle) image.Point { return r.Min.Add(r.Size().Div(2)) }
	layoutBar(click(center(b.boxes[inlineEnable-1]))...)
	layoutBar()
	if !b.more.open || b.more.box.Empty() {
		t.Fatal("Enable… did not open its menu")
	}
	// The last item, f's: items are 32 px tall below 6 px of padding.
	// It is f's still when a is enabled meanwhile, and the menu lists
	// only e and f once it is drawn again.
	item := b.more.box.Min.Add(image.Pt(20, 6+2*32+16))
	cfg.Panels[0].Enabled = nil
	if actions := layoutBar(click(item)...); !slices.Equal(actions, []Action{{Kind: ActionEnable, Panel: "f"}}) {
		t.Errorf("Enable…: actions %v", actions)
	}
}

// Behind a dialog, Tab does not reach a toast's action.
func TestToasterInert(t *testing.T) {
	for _, inert := range []bool{false, true} {
		var r input.Router
		var toast Toaster
		toast.ShowAction("Panel notes disabled", "Undo", "notes")
		toast.Inert = inert
		for range 2 {
			gtx := context(&r, image.Pt(800, 600), time.Now())
			toast.Layout(gtx, testTheme)
			r.Frame(gtx.Ops)
		}
		r.MoveFocus(key.FocusForward) // Tab, as the window does it
		gtx := context(&r, image.Pt(800, 600), time.Now())
		toast.Layout(gtx, testTheme)
		r.Frame(gtx.Ops)
		if got := r.Source().Focused(&toast.act.Clickable); got == inert {
			t.Errorf("inert %t: action focused %t", inert, got)
		}
	}
}

func TestToasterAction(t *testing.T) {
	var r input.Router
	t0 := time.Now()
	var toast Toaster
	layoutToast := func(at time.Duration, events ...event.Event) (string, bool) {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(800, 600), t0.Add(at))
		tag, ok := toast.Acted(gtx)
		toast.Layout(gtx, testTheme)
		r.Frame(gtx.Ops)
		return tag, ok
	}
	toast.ShowAction("Panel notes disabled", "Undo", "notes")
	layoutToast(0)
	if box := toast.actBox; box.Empty() || box.Dx() > 200 || box.Dy() > 60 {
		t.Fatalf("action drawn at %v, want a small button", box)
	}
	// It outlasts a plain toast.
	layoutToast(ToastFor + time.Second)
	if toast.Text() == "" {
		t.Fatalf("gone after %v", ToastFor+time.Second)
	}
	center := toast.actBox.Min.Add(toast.actBox.Size().Div(2))
	tag, ok := layoutToast(ToastFor+time.Second, click(center)...)
	if !ok || tag != "notes" {
		t.Errorf("Acted = %q, %t, want notes", tag, ok)
	}
	if toast.Text() != "" {
		t.Errorf("still showing %q after its action", toast.Text())
	}
	// A later one takes the place of the one showing.
	toast.ShowAction("Panel a disabled", "Undo", "a")
	layoutToast(time.Minute)
	toast.ShowAction("Panel b disabled", "Undo", "b")
	layoutToast(time.Minute + time.Second)
	if toast.Text() != "Panel b disabled" {
		t.Errorf("showing %q, want b's", toast.Text())
	}
	center = toast.actBox.Min.Add(toast.actBox.Size().Div(2))
	if tag, ok := layoutToast(time.Minute+time.Second, click(center)...); tag != "b" || !ok {
		t.Errorf("Acted = %q, %t, want b", tag, ok)
	}
	toast.ShowAction("Panel a disabled", "Undo", "a")
	layoutToast(time.Minute)
	if layoutToast(time.Minute + ActionFor); toast.Text() != "" {
		t.Errorf("still showing %q after %v", toast.Text(), ActionFor)
	}
	// Undone some other way, it goes, and leaves other messages be.
	toast.ShowAction("Panel a disabled", "Undo", "a")
	layoutToast(2 * time.Minute)
	toast.Show("Layout not saved")
	toast.DropAction("b")
	if toast.Text() != "Panel a disabled" {
		t.Errorf("showing %q after dropping b's, want a's", toast.Text())
	}
	toast.DropAction("a")
	if layoutToast(2*time.Minute + time.Second); toast.Text() != "Layout not saved" {
		t.Errorf("showing %q after dropping a's, want the next message", toast.Text())
	}
	// An action's toast goes ahead of the messages waiting.
	toast.Show("one")
	layoutToast(3 * time.Minute)
	toast.Show("two")
	toast.ShowAction("Panel b disabled", "Undo", "b")
	if layoutToast(3*time.Minute + ToastFor); toast.Text() != "Panel b disabled" {
		t.Errorf("showing %q after the first message, want the action's", toast.Text())
	}
	toast.DropAction("b")
	layoutToast(3*time.Minute + ToastFor)
	if toast.Text() != "two" {
		t.Errorf("showing %q after the action's, want two", toast.Text())
	}
	if layoutToast(3*time.Minute + 2*ToastFor + time.Second); toast.Text() != "" {
		t.Fatalf("showing %q, want the queue empty", toast.Text())
	}
	// A message waiting cuts an action's toast short, to ToastFor.
	toast.ShowAction("Panel a disabled", "Undo", "a")
	layoutToast(4 * time.Minute)
	toast.Show("Layout not saved for a")
	layoutToast(4*time.Minute + time.Second)
	if layoutToast(4*time.Minute + time.Second + ToastFor); toast.Text() != "Layout not saved for a" {
		t.Errorf("showing %q %v after a message came, want the message", toast.Text(), ToastFor)
	}
	// So does one waiting when the action's toast comes from the queue.
	toast.Show("one")
	layoutToast(5 * time.Minute)
	toast.Show("two")
	toast.ShowAction("Panel c disabled", "Undo", "c")
	layoutToast(5*time.Minute + ToastFor)
	if toast.Text() != "Panel c disabled" {
		t.Fatalf("showing %q, want the action's", toast.Text())
	}
	if layoutToast(5*time.Minute + 2*ToastFor); toast.Text() != "two" {
		t.Errorf("showing %q %v after the action's toast came, want two", toast.Text(), ToastFor)
	}
}

// A press on the toast, beside its action, does not reach what is
// below it.
func TestToasterTakesPresses(t *testing.T) {
	var r input.Router
	var toast Toaster
	var below int // the presses that reach the area below
	layoutAll := func(events ...event.Event) {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(800, 600), time.Now())
		for {
			e, ok := gtx.Event(pointer.Filter{Target: &below, Kinds: pointer.Press})
			if !ok {
				break
			}
			if e, ok := e.(pointer.Event); ok && e.Kind == pointer.Press {
				below++
			}
		}
		area := clip.Rect{Max: image.Pt(800, 600)}.Push(gtx.Ops)
		event.Op(gtx.Ops, &below)
		area.Pop()
		toast.Layout(gtx, testTheme)
		r.Frame(gtx.Ops)
	}
	toast.ShowAction("Panel notes disabled", "Undo", "notes")
	layoutAll()
	beside := image.Pt(toast.actBox.Min.X-4, toast.actBox.Min.Y+toast.actBox.Dy()/2)
	layoutAll(click(beside)...)
	layoutAll()
	if below != 0 {
		t.Errorf("%d presses on the toast reached the area below", below)
	}
	// One away from the toast still does.
	layoutAll(click(image.Pt(10, 10))...)
	layoutAll()
	if below != 1 {
		t.Errorf("%d presses reached the area below, want 1", below)
	}
}
