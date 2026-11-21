// SPDX-License-Identifier: GPL-3.0-or-later

package uidebug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/semantic"
	"gioui.org/op"

	"github.com/joncbenderkh/tessera/internal/uimark"
)

// Server answers requests for the semantic tree from the window's
// frames: the frame loop calls Frame with each frame's ops.
type Server struct {
	invalidate func()
	router     input.Router

	mu      sync.Mutex
	pending []chan Tree // requests waiting for the next frame
}

// Serve listens on the Unix socket at path, replacing a stale socket
// but nothing else, nor one another process still listens on, and
// serves GET /tree until ctx ends. invalidate asks the window for a
// frame, which answers the request.
func Serve(ctx context.Context, path string, invalidate func()) (*Server, error) {
	switch fi, err := os.Lstat(path); {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	case fi.Mode().Type() != os.ModeSocket:
		return nil, fmt.Errorf("%s exists and is not a socket", path)
	default:
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("%s is in use by another process", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	s := &Server{invalidate: invalidate}
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
	// Queued before the frame is asked for, so that frame answers it.
	reply := make(chan Tree, 1)
	s.mu.Lock()
	s.pending = append(s.pending, reply)
	s.mu.Unlock()
	// A window drops a request for a frame while it still has one
	// to draw, which may be the frame just taken, so ask again until
	// one answers.
	s.invalidate()
	again := time.NewTicker(50 * time.Millisecond)
	defer again.Stop()
	timeout := time.After(5 * time.Second)
	var t Tree
wait:
	for {
		select {
		case t = <-reply:
			break wait
		case <-again.C:
			s.invalidate()
		case <-r.Context().Done():
			return
		case <-timeout:
			http.Error(w, "the window drew no frame", http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

// Frame answers the requests waiting with the semantic tree of ops,
// the frame of a window size pixels large, about to be drawn. A nil
// Server does nothing.
func (s *Server) Frame(ops *op.Ops, size image.Point) {
	if s == nil {
		return
	}
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	s.router.Frame(ops)
	t := Tree{Size: size, Nodes: flatten(s.router.AppendSemantics(nil))}
	for _, reply := range pending {
		reply <- t // buffered; one whose request gave up is dropped
	}
}

// flatten lists nodes and their descendants depth first, giving each
// the labels of its subtree as its text, and the part of its bounds
// its viewports show.
//
// The markers internal/ui gives (see uimark) are kept, without their
// descriptions: what is in a viewport shows only within it, and what
// comes before the last modal layer, other than its ancestors, is
// covered.
func flatten(nodes []input.SemanticNode) []Node {
	var out []Node
	modal := -1 // the last modal layer's index in out
	var walk func(n input.SemanticNode, shown image.Rectangle) string
	walk = func(n input.SemanticNode, shown image.Rectangle) string {
		d := n.Desc
		visible := d.Bounds.Intersect(shown)
		switch d.Description {
		case uimark.Viewport:
			shown = visible
			d.Description = ""
		case uimark.Modal:
			modal = len(out)
			d.Description = ""
		}
		i := len(out)
		out = append(out, Node{
			ID:          uint(n.ID),
			Parent:      uint(n.ParentID),
			Label:       d.Label,
			Description: d.Description,
			Selected:    d.Selected,
			Disabled:    d.Disabled,
			Clickable:   d.Class != semantic.Unknown,
			Bounds:      d.Bounds,
			Visible:     visible,
		})
		if d.Class != semantic.Unknown {
			out[i].Class = d.Class.String()
		}
		text := d.Label
		for _, c := range n.Children {
			if t := walk(c, shown); t != "" {
				if text != "" {
					text += " "
				}
				text += t
			}
		}
		if n.ParentID != 0 {
			// The root's text, the whole window's, would match any
			// query, through elements that are not shown, too.
			out[i].Text = text
		}
		return text
	}
	// AppendSemantics gives every node with its children; the root is
	// the first, and the tree hangs from it.
	if len(nodes) > 0 {
		walk(nodes[0], nodes[0].Desc.Bounds)
	}
	if modal >= 0 {
		above := map[uint]bool{}
		for i := modal; i >= 0; i-- {
			// Depth first, so the layer's ancestors come before it,
			// each the parent of the one found after it.
			if i == modal || above[out[i].ID] {
				above[out[i].Parent] = true
			}
		}
		for i := range out[:modal] {
			if !above[out[i].ID] {
				out[i].Visible, out[i].Covered = image.Rectangle{}, true
			}
		}
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
