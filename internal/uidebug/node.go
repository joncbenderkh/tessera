// SPDX-License-Identifier: GPL-3.0-or-later

// Package uidebug serves the host window's semantic tree, each button,
// label, and editor with its bounds, on a Unix socket, so cmd/uidrive
// can find and drive them by name. The host serves it only when asked
// to, with its hidden --debug-socket flag.
package uidebug

import (
	"cmp"
	"image"
	"slices"
	"strings"
)

// Node is one element of the window's semantic tree, in window pixels.
// The tree holds the elements that give semantics in an area without
// input handlers of their own (see internal/ui's semanticArea), and
// text: the router reading it is not the window's, so an area's
// handlers ask it for nothing, and it lists no area for them.
type Node struct {
	ID          uint            `json:"id"`
	Parent      uint            `json:"parent,omitempty"`
	Class       string          `json:"class,omitempty"` // "Button", "Editor", "CheckBox", "RadioButton", "Switch", or "" for none
	Label       string          `json:"label,omitempty"`
	Description string          `json:"description,omitempty"`
	Text        string          `json:"text,omitempty"` // the labels of the node and its descendants, in order
	Selected    bool            `json:"selected,omitempty"`
	Disabled    bool            `json:"disabled,omitempty"`
	Clickable   bool            `json:"clickable,omitempty"` // has a class: a Button, Editor, and so on
	Bounds      image.Rectangle `json:"bounds"`
	// Visible is the part of Bounds that shows: Bounds cut to the
	// viewports, such as a scrolling list, the node is in. It is empty
	// for a node scrolled out of view or Covered.
	Visible image.Rectangle `json:"visible"`
	// Covered is set for a node under a dialog or popover drawn after
	// it, which takes the clicks there.
	Covered bool `json:"covered,omitempty"`
}

// Tree is the window's semantic tree, flattened in depth-first order.
type Tree struct {
	Size  image.Point `json:"size"` // the window's size in pixels
	Nodes []Node      `json:"nodes"`
}

// Find returns the nodes query names. A node names query if its label,
// description, or text equals it, ignoring case; failing any, if one
// of them contains it. Of those, nodes that take clicks come first,
// then smaller ones, so a button wins over the label in it and the
// tile around it.
func (t Tree) Find(query string) []Node { return t.FindFunc(query, nil) }

// FindFunc is Find among the nodes keep keeps, or all if keep is nil.
// The nodes it leaves out still count as ancestors.
func (t Tree) FindFunc(query string, keep func(Node) bool) []Node {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	loose := false // matching by substring
	match := func(contains bool) []Node {
		var found []Node
		for _, n := range t.Nodes {
			if n.Bounds.Empty() || keep != nil && !keep(n) {
				continue
			}
			for _, s := range []string{n.Label, n.Description, n.Text} {
				s = strings.ToLower(strings.TrimSpace(s))
				if s == q || contains && strings.Contains(s, q) {
					found = append(found, n)
					break
				}
			}
		}
		return found
	}
	found := match(false)
	if len(found) == 0 {
		loose = true
		found = match(true)
	}
	// Drop a node whose match comes only from a descendant that also
	// matched: the tile around a button is not the button, nor are the
	// viewports between them.
	parentOf := make(map[uint]uint, len(t.Nodes))
	for _, n := range t.Nodes {
		parentOf[n.ID] = n.Parent
	}
	parents := map[uint]bool{}
	for _, n := range found {
		for p, seen := n.Parent, 0; p != 0 && seen < len(t.Nodes); p, seen = parentOf[p], seen+1 {
			parents[p] = true
		}
	}
	var kept []Node
	for _, n := range found {
		if !parents[n.ID] || own(n, q, loose) {
			kept = append(kept, n)
		}
	}
	sortNodes(kept)
	return kept
}

// own reports whether n's own label or description names q: holds it,
// if contains, and otherwise equals it.
func own(n Node, q string, contains bool) bool {
	for _, s := range []string{n.Label, n.Description} {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == q || contains && strings.Contains(s, q) {
			return true
		}
	}
	return false
}

// sortNodes orders nodes that take clicks first, then by area, then by
// position, top to bottom and left to right.
func sortNodes(nodes []Node) {
	slices.SortStableFunc(nodes, func(a, b Node) int {
		if a.Clickable != b.Clickable {
			if a.Clickable {
				return -1
			}
			return 1
		}
		return cmp.Or(
			cmp.Compare(area(a.Bounds), area(b.Bounds)),
			cmp.Compare(a.Bounds.Min.Y, b.Bounds.Min.Y),
			cmp.Compare(a.Bounds.Min.X, b.Bounds.Min.X),
		)
	})
}

func area(r image.Rectangle) int { return r.Dx() * r.Dy() }
