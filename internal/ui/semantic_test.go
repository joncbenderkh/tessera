// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/semantic"
	"gioui.org/op"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/uimark"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// foreignSemantics reads the semantics of ops with a router other than
// the one the widgets drew for, as uidebug does.
func foreignSemantics(ops *op.Ops) []input.SemanticNode {
	var r input.Router
	r.Frame(ops)
	return r.AppendSemantics(nil)
}

func findSemantic(nodes []input.SemanticNode, match func(input.SemanticDesc) bool) (input.SemanticDesc, bool) {
	for _, n := range nodes {
		if match(n.Desc) {
			return n.Desc, true
		}
	}
	return input.SemanticDesc{}, false
}

// An actionable list row keeps its semantics for another router.
func TestRowSemanticsSeenByAnotherRouter(t *testing.T) {
	view := &tesserav1.View{Body: listOf("", &tesserav1.ListItem{Title: "Review", ActionId: "ev:1"})}
	var r input.Router
	var v viewState
	size := image.Pt(300, 400)
	viewFrame(&r, &v, view, size)
	gtx := context(&r, size, time.Now())
	v.Layout(gtx, testTheme, view)
	r.Frame(gtx.Ops)
	d, ok := findSemantic(foreignSemantics(gtx.Ops), func(d input.SemanticDesc) bool {
		return d.Class == semantic.Button && strings.Contains(d.Label, "Review")
	})
	if !ok {
		t.Fatal("no button for the row")
	}
	if d.Bounds.Empty() || !d.Bounds.In(image.Rectangle{Max: size}) {
		t.Errorf("row bounds %v, want within %v", d.Bounds, size)
	}
}

// A tile's head in edit mode keeps its description for another router.
func TestTileHeadSemanticsSeenByAnotherRouter(t *testing.T) {
	var r input.Router
	cfg := gridConfig([]config.Panel{panel("a", 0, 0, 1, 1)})
	g := editGrid(&r, cfg)
	gtx := context(&r, image.Pt(1440, 900), time.Now())
	g.Layout(gtx, testTheme, cfg, live("a"))
	r.Frame(gtx.Ops)
	d, ok := findSemantic(foreignSemantics(gtx.Ops), func(d input.SemanticDesc) bool {
		return strings.HasPrefix(d.Description, "Move ")
	})
	if !ok {
		t.Fatal("no description for moving the tile")
	}
	if head := g.placed["a"]; !d.Bounds.Min.In(head) {
		t.Errorf("head bounds %v, want in the tile %v", d.Bounds, head)
	}
}

// A list scrolled so that its first row is half out of view gives the
// rows a viewport, which another router reads: the row's visible part
// is what lies within it.
func TestListViewportSeenByAnotherRouter(t *testing.T) {
	var items []*tesserav1.ListItem
	for i := range 20 {
		items = append(items, &tesserav1.ListItem{Title: fmt.Sprintf("Row %d", i), ActionId: fmt.Sprintf("ev:%d", i)})
	}
	view := &tesserav1.View{Body: listOf("", items...)}
	var r input.Router
	var v viewState
	size := image.Pt(300, 200)
	viewFrame(&r, &v, view, size)
	gtx := context(&r, size, time.Now())
	v.Layout(gtx, testTheme, view)
	r.Frame(gtx.Ops)
	nodes := foreignSemantics(gtx.Ops)
	var viewport, row input.SemanticNode
	for _, n := range nodes {
		d := n.Desc
		switch {
		case d.Description == uimark.Viewport:
			viewport = n
		case d.Class == semantic.Button && strings.Contains(d.Label, "Row 0"):
			row = n
		}
	}
	if viewport.ID == 0 {
		t.Fatal("no viewport node for the list")
	}
	if row.ParentID != viewport.ID {
		t.Errorf("row's parent %d, want the viewport %d", row.ParentID, viewport.ID)
	}
	if !viewport.Desc.Bounds.In(image.Rectangle{Max: size}) {
		t.Errorf("viewport bounds %v, want within the view %v", viewport.Desc.Bounds, size)
	}
}

// A dialog's backdrop is a modal layer another router reads, after the
// elements it covers.
func TestDialogModalSeenByAnotherRouter(t *testing.T) {
	var r input.Router
	var d Dialog
	d.Open("Logs", "Plugin clock", "one\ntwo")
	var b Button
	gtx := context(&r, image.Pt(800, 600), time.Now())
	b.Layout(gtx, testTheme, "Restart")
	d.Layout(gtx, testTheme)
	r.Frame(gtx.Ops)
	nodes := foreignSemantics(gtx.Ops)
	restart, modal := -1, -1
	for i, n := range nodes {
		switch {
		case n.Desc.Class == semantic.Button && n.Desc.Label == "Restart":
			restart = i
		case n.Desc.Description == uimark.Modal:
			modal = i
		}
	}
	if restart < 0 || modal < 0 || modal < restart {
		t.Errorf("Restart at %d, modal layer at %d; want the layer after the button", restart, modal)
	}
}
