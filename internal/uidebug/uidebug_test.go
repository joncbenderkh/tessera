// SPDX-License-Identifier: GPL-3.0-or-later

package uidebug

import (
	"image"
	"testing"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// TestButtonSeenByAnotherRouter checks that a button drawn for the
// window's router keeps its semantics in the tree another router reads,
// as Server's does, and that the text drawn on it is not listed again.
func TestButtonSeenByAnotherRouter(t *testing.T) {
	th := theme.New(true, theme.NewShaper(true))
	var window input.Router
	var b ui.Button
	b.Description = "Save the settings"
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Constraints: layout.Constraints{Max: image.Pt(400, 300)}, Source: window.Source()}
	op.Offset(image.Pt(10, 20)).Add(&ops)
	b.Layout(gtx, th, "Save")
	window.Frame(&ops)

	var other input.Router
	other.Frame(&ops)
	tree := Tree{Nodes: flatten(other.AppendSemantics(nil))}
	found := tree.Find("save")
	if len(found) != 1 {
		t.Fatalf("Find(save) = %+v, want the button alone", found)
	}
	n := found[0]
	if n.Class != "Button" || !n.Clickable || n.Label != "Save" || n.Description != "Save the settings" {
		t.Errorf("button = %+v", n)
	}
	if !n.Bounds.Min.Eq(image.Pt(10, 20)) || n.Bounds.Dx() < 32 || n.Bounds.Dy() < 32 {
		t.Errorf("button bounds = %v, want 32 px or more from 10,20", n.Bounds)
	}
}

func TestFind(t *testing.T) {
	r := func(x, y, w, h int) image.Rectangle { return image.Rect(x, y, x+w, y+h) }
	tree := Tree{Nodes: []Node{
		{ID: 1, Text: "World clock ⋯ Configure… Tokyo", Bounds: r(0, 0, 1000, 1000)},
		{ID: 2, Parent: 1, Label: "World clock", Text: "World clock", Bounds: r(40, 100, 500, 300)},
		{ID: 3, Parent: 1, Class: "Button", Clickable: true, Label: "⋯", Description: "World clock panel options", Text: "⋯", Bounds: r(590, 94, 27, 27)},
		{ID: 4, Parent: 1, Class: "Button", Clickable: true, Label: "Configure…", Text: "Configure…", Bounds: r(510, 136, 105, 32)},
		{ID: 5, Parent: 1, Label: "Tokyo", Text: "Tokyo", Bounds: r(40, 130, 500, 300)},
		{ID: 6, Parent: 1, Label: "Empty", Text: "Empty"},
	}}
	ids := func(nodes []Node) []uint {
		var out []uint
		for _, n := range nodes {
			out = append(out, n.ID)
		}
		return out
	}
	for _, c := range []struct {
		query string
		want  []uint
	}{
		{"configure…", []uint{4}},    // exact, and not the root whose text holds it
		{"Configure", []uint{4}},     // contained
		{"world clock", []uint{2}},   // exact beats contained
		{"panel options", []uint{3}}, // by description
		{"o", []uint{3, 4, 2, 5}},    // clickable first, then by size
		{"Empty", nil},               // no bounds: not shown
		{"", nil},
	} {
		got := ids(tree.Find(c.query))
		if len(got) != len(c.want) {
			t.Errorf("Find(%q) = %v, want %v", c.query, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Find(%q) = %v, want %v", c.query, got, c.want)
				break
			}
		}
	}
}
