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
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
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
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	switch fi, err := os.Lstat(path); {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	case fi.Mode().Type() != os.ModeSocket:
		return nil, fmt.Errorf("%s exists and is not a socket", path)
	default:
		// Only a refused connection shows that no one listens there: a
		// listener with a full backlog fails otherwise.
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			c.Close()
			return nil, fmt.Errorf("%s is in use by another process", path)
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("%s may be in use by another process: %w", path, err)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	l, made, err := listenPrivate(path)
	if err != nil {
		return nil, err
	}
	s := &Server{invalidate: invalidate}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tree", s.tree)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
		// The socket goes with the server, unless another has taken
		// its path since.
		if fi, err := os.Lstat(path); err == nil && os.SameFile(fi, made) {
			os.Remove(path)
		}
	}()
	go srv.Serve(l)
	return s, nil
}

// privateDir is the prefix of the directory a socket is made in before
// it is published at its path, and privateSocket its name there.
const (
	privateDir    = ".u"
	privateSocket = "s"
)

// CheckPath reports whether path can take a socket: its path, and the
// longer one it is made at first, beside it, must both fit the 107
// bytes a Unix socket's path allows.
func CheckPath(path string) error {
	// MkdirTemp adds up to 10 digits.
	made := len(filepath.Dir(path)) + len("/"+privateDir) + 10 + len("/"+privateSocket)
	if n := max(len(path), made); n > 107 {
		return fmt.Errorf("the socket path %s needs %d bytes, over the 107 a socket allows", path, n)
	}
	return nil
}

// listenPrivate listens on a Unix socket at path that only the user may
// connect to: the window's text is theirs. It is made in a directory
// of its own, closed to others, restricted, and only then linked at
// path, which fails rather than replace anything there. It returns the
// socket's file, to know it again.
func listenPrivate(path string) (net.Listener, os.FileInfo, error) {
	private, err := os.MkdirTemp(filepath.Dir(path), privateDir)
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(private)
	made := filepath.Join(private, privateSocket)
	l, err := net.Listen("unix", made)
	if err != nil {
		return nil, nil, err
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false) // its name moves; Serve removes it
	fail := func(err error) (net.Listener, os.FileInfo, error) {
		l.Close()
		return nil, nil, err
	}
	if err := os.Chmod(made, 0o600); err != nil {
		return fail(err)
	}
	fi, err := os.Lstat(made)
	if err != nil {
		return fail(err)
	}
	if err := os.Link(made, path); err != nil {
		return fail(fmt.Errorf("%s was taken meanwhile: %w", path, err))
	}
	return l, fi, nil
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
			s.drop(reply)
			return
		case <-timeout:
			s.drop(reply)
			http.Error(w, "the window drew no frame", http.StatusServiceUnavailable)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

// drop takes back a request that no longer waits, if no frame has
// taken it yet.
func (s *Server) drop(reply chan Tree) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = slices.DeleteFunc(s.pending, func(c chan Tree) bool { return c == reply })
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
	t := Tree{Size: size, Nodes: flattenIn(s.router.AppendSemantics(nil), image.Rectangle{Max: size})}
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
	if len(nodes) == 0 {
		return nil
	}
	return flattenIn(nodes, nodes[0].Desc.Bounds)
}

// flattenIn is flatten for a window that shows window, its rectangle:
// nothing shows outside it.
func flattenIn(nodes []input.SemanticNode, window image.Rectangle) []Node {
	var out []Node
	modal := -1 // the last modal layer's index in out
	var walk func(n input.SemanticNode, shown image.Rectangle)
	walk = func(n input.SemanticNode, shown image.Rectangle) {
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
		for _, c := range n.Children {
			walk(c, shown)
		}
	}
	// AppendSemantics gives every node with its children; the root is
	// the first, and the tree hangs from it.
	if len(nodes) > 0 {
		walk(nodes[0], nodes[0].Desc.Bounds.Intersect(window))
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
	// A node's text is its label and its children's texts, in order, of
	// the children that show: a row scrolled out of its list, or one a
	// dialog covers, does not name the list. Children come after their
	// parent, so a pass from the end has them first.
	// AllText is the same of every child, shown or not.
	children := map[uint][]string{} // in reverse order
	all := map[uint][]string{}
	join := func(label string, rev []string) string {
		var parts []string
		if label != "" {
			parts = append(parts, label)
		}
		for j := len(rev) - 1; j >= 0; j-- {
			parts = append(parts, rev[j])
		}
		return strings.Join(parts, " ")
	}
	for i := len(out) - 1; i >= 0; i-- {
		n := &out[i]
		if n.Parent == 0 {
			// The root's text, the whole window's, would match any
			// query.
			continue
		}
		n.Text = join(n.Label, children[n.ID])
		n.AllText = join(n.Label, all[n.ID])
		if n.Text != "" && !n.Visible.Empty() {
			children[n.Parent] = append(children[n.Parent], n.Text)
		}
		if n.AllText != "" {
			all[n.Parent] = append(all[n.Parent], n.AllText)
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
			if o.ID != n.ID && o.Class != "" && o.Label == n.Label && o.Covered == n.Covered && n.Bounds.Min.In(o.Bounds) {
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
