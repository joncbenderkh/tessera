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
type Node struct {
	ID          uint            `json:"id"`
	Parent      uint            `json:"parent,omitempty"`
	Class       string          `json:"class,omitempty"` // "Button", "Editor", "CheckBox", "RadioButton", "Switch", or "" for none
	Label       string          `json:"label,omitempty"`
	Description string          `json:"description,omitempty"`
	Text        string          `json:"text,omitempty"` // the labels of the node and its descendants, in order
	Selected    bool            `json:"selected,omitempty"`
	Disabled    bool            `json:"disabled,omitempty"`
	Clickable   bool            `json:"clickable,omitempty"` // takes clicks: a Button, Editor, and so on
	Scrollable  bool            `json:"scrollable,omitempty"`
	Bounds      image.Rectangle `json:"bounds"`
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
func (t Tree) Find(query string) []Node {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	match := func(contains bool) []Node {
		var found []Node
		for _, n := range t.Nodes {
			if n.Bounds.Empty() {
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
		found = match(true)
	}
	// Drop a node whose match comes only from a descendant that also
	// matched: the tile around a button is not the button.
	byID := map[uint]bool{}
	for _, n := range found {
		byID[n.ID] = true
	}
	parents := map[uint]bool{}
	for _, n := range found {
		if byID[n.Parent] {
			parents[n.Parent] = true
		}
	}
	var kept []Node
	for _, n := range found {
		if !parents[n.ID] || own(n, q) {
			kept = append(kept, n)
		}
	}
	sortNodes(kept)
	return kept
}

// own reports whether n's own label or description names q.
func own(n Node, q string) bool {
	for _, s := range []string{n.Label, n.Description} {
		if strings.Contains(strings.ToLower(s), q) {
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
