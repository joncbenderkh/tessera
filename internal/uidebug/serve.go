// SPDX-License-Identifier: GPL-3.0-or-later

package uidebug

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"net"
	"net/http"
	"os"
	"slices"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/semantic"
	"gioui.org/op"
)

// Server answers requests for the semantic tree from the window's
// frames: the frame loop calls Frame with each frame's ops.
type Server struct {
	requests   chan chan Tree
	invalidate func()
	router     input.Router
}

// Serve listens on the Unix socket at path, replacing a stale one, and
// serves GET /tree until ctx ends. invalidate asks the window for a
// frame, which answers the request.
func Serve(ctx context.Context, path string, invalidate func()) (*Server, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	s := &Server{requests: make(chan chan Tree), invalidate: invalidate}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tree", s.tree)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go srv.Serve(l)
	return s, nil
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	reply := make(chan Tree, 1)
	s.invalidate()
	select {
	case s.requests <- reply:
	case <-r.Context().Done():
		return
	case <-time.After(5 * time.Second):
		http.Error(w, "the window drew no frame", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(<-reply)
}

// Frame answers a waiting request with the semantic tree of ops, the
// frame of a window size pixels large, about to be drawn. A nil Server
// does nothing.
func (s *Server) Frame(ops *op.Ops, size image.Point) {
	if s == nil {
		return
	}
	select {
	case reply := <-s.requests:
		s.router.Frame(ops)
		reply <- Tree{Size: size, Nodes: flatten(s.router.AppendSemantics(nil))}
	default:
		// A request that arrives between frames asked for one.
	}
}

// flatten lists nodes and their descendants depth first, giving each
// the labels of its subtree as its text.
func flatten(nodes []input.SemanticNode) []Node {
	var out []Node
	var walk func(n input.SemanticNode) string
	walk = func(n input.SemanticNode) string {
		d := n.Desc
		i := len(out)
		out = append(out, Node{
			ID:          uint(n.ID),
			Parent:      uint(n.ParentID),
			Label:       d.Label,
			Description: d.Description,
			Selected:    d.Selected,
			Disabled:    d.Disabled,
			Clickable:   d.Gestures&input.ClickGesture != 0 || d.Class != semantic.Unknown,
			Scrollable:  d.Gestures&input.ScrollGesture != 0,
			Bounds:      d.Bounds,
		})
		if d.Class != semantic.Unknown {
			out[i].Class = d.Class.String()
		}
		text := d.Label
		for _, c := range n.Children {
			if t := walk(c); t != "" {
				if text != "" {
					text += " "
				}
				text += t
			}
		}
		out[i].Text = text
		return text
	}
	// AppendSemantics gives every node with its children; the root is
	// the first, and the tree hangs from it.
	if len(nodes) > 0 {
		walk(nodes[0])
	}
	return withoutEchoes(out)
}

// withoutEchoes drops the text drawn on an element that already has it
// as its label, as a button's: a plain label node, with nothing below
// it, that starts inside a node it repeats the label of. It drops a
// repeat of an element too.
func withoutEchoes(nodes []Node) []Node {
	echo := func(n Node) bool {
		if n.Class != "" || n.Description != "" || n.Label == "" || n.Text != n.Label {
			return false
		}
		for _, o := range nodes {
			if o.ID != n.ID && o.Class != "" && o.Label == n.Label && n.Bounds.Min.In(o.Bounds) {
				return true
			}
		}
		return false
	}
	// Gio can list one element twice, as an editor whose own semantics
	// sit beside those its box gives: same parent, class, label,
	// description, and bounds.
	type key struct {
		parent             uint
		class, label, desc string
		bounds             image.Rectangle
	}
	seen := map[key]bool{}
	echoes := map[uint]bool{}
	for _, n := range nodes {
		k := key{n.Parent, n.Class, n.Label, n.Description, n.Bounds}
		if echo(n) || seen[k] && n.Text == n.Label {
			echoes[n.ID] = true
		}
		seen[k] = true
	}
	return slices.DeleteFunc(nodes, func(n Node) bool { return echoes[n.ID] })
}
