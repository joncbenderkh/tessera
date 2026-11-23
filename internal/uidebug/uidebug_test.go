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
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
	"github.com/joncbenderkh/tessera/internal/uimark"
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

// A frame drawn as soon as the request asks for one, before the
// handler waits, still answers it.
func TestTreeAnsweredByFrameAskedFor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debug.sock")
	var s *Server
	var ops op.Ops
	var err error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err = Serve(ctx, path, func() { s.Frame(&ops, image.Pt(10, 10)) })
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}},
	}
	resp, err := client.Get("http://host/tree")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tree Tree
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil || resp.StatusCode != http.StatusOK || tree.Size != image.Pt(10, 10) {
		t.Errorf("status %s, tree %+v, error %v", resp.Status, tree, err)
	}
}

// Serve replaces a stale socket, and refuses to remove anything else.
func TestServeRemovesOnlySockets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Serve(ctx, file, func() {}); err == nil {
		t.Error("Serve took a regular file's path")
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "keep" {
		t.Errorf("the file is gone or changed: %q, %v", b, err)
	}
	sock := filepath.Join(dir, "stale.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if _, err := Serve(ctx, sock, func() {}); err != nil {
		t.Errorf("Serve over a stale socket: %v", err)
	}
}

// An exact match is not joined by an ancestor that matches only
// through its text and whose own description just contains the query.
func TestFindExactSkipsContainingAncestor(t *testing.T) {
	tree := Tree{Nodes: []Node{
		{ID: 1, Description: "Save settings", Text: "Save", Bounds: image.Rect(0, 0, 400, 300)},
		{ID: 2, Parent: 1, Class: "Button", Clickable: true, Label: "Save", Text: "Save", Bounds: image.Rect(10, 10, 60, 42)},
	}}
	if got := tree.Find("save"); len(got) != 1 || got[0].ID != 2 {
		t.Errorf("Find(save) = %+v, want the button alone", got)
	}
}

// Serve refuses a socket another process still listens on.
func TestServeKeepsLiveSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "live.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Serve(ctx, sock, func() {}); err == nil {
		t.Error("Serve took over a live socket")
	}
	if c, err := net.Dial("unix", sock); err != nil {
		t.Errorf("the first listener is unreachable: %v", err)
	} else {
		c.Close()
	}
}

// The root's text, which holds every label, matches nothing, as with
// text that is not shown.
func TestRootMatchesNothing(t *testing.T) {
	nodes := flatten([]input.SemanticNode{{
		ID:   1,
		Desc: input.SemanticDesc{Bounds: image.Rect(-1e6, -1e6, 1e6, 1e6)},
		Children: []input.SemanticNode{
			{ID: 2, ParentID: 1, Desc: input.SemanticDesc{Label: "Hidden"}},
		},
	}})
	if got := (Tree{Nodes: nodes}).Find("hidden"); len(got) != 0 {
		t.Errorf("Find(hidden) = %+v, want nothing", got)
	}
}

// A request whose first call for a frame the window drops, as one with
// a frame still to draw does, is answered by a later call.
func TestTreeAsksAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.sock")
	var s *Server
	var ops op.Ops
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var err error
	s, err = Serve(ctx, path, func() {
		if calls++; calls > 1 { // the first is dropped
			s.Frame(&ops, image.Pt(10, 10))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}},
	}
	resp, err := client.Get("http://host/tree")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status %s", resp.Status)
	}
}

// An element in a viewport is visible only where the viewport shows.
func TestFlattenClipsToViewport(t *testing.T) {
	nodes := flatten([]input.SemanticNode{{
		ID:   1,
		Desc: input.SemanticDesc{Bounds: image.Rect(-1e6, -1e6, 1e6, 1e6)},
		Children: []input.SemanticNode{{
			ID: 2, ParentID: 1, Desc: input.SemanticDesc{Description: uimark.Viewport, Bounds: image.Rect(0, 100, 300, 300)},
			Children: []input.SemanticNode{
				{ID: 3, ParentID: 2, Desc: input.SemanticDesc{Class: semantic.Button, Label: "Half", Bounds: image.Rect(0, 80, 300, 130)}},
				{ID: 4, ParentID: 2, Desc: input.SemanticDesc{Class: semantic.Button, Label: "Gone", Bounds: image.Rect(0, 20, 300, 60)}},
			},
		}},
	}})
	want := map[uint]image.Rectangle{3: image.Rect(0, 100, 300, 130), 4: {}}
	for _, n := range nodes {
		if w, ok := want[n.ID]; ok && !n.Visible.Eq(w) {
			t.Errorf("node %d (%s) visible %v, want %v", n.ID, n.Label, n.Visible, w)
		}
	}
	// The viewport is named by what shows in it alone: among the nodes
	// that show, as wait looks, nothing is named for the hidden row.
	shown := func(n Node) bool { return !n.Visible.Empty() }
	if got := (Tree{Nodes: nodes}).FindFunc("gone", shown); len(got) != 0 {
		t.Errorf("FindFunc(gone, shown) = %+v, want nothing", got)
	}
}

// An ancestor that matches only through a button below a viewport is
// dropped, with or without a filter that leaves the viewport out.
func TestFindDropsAncestorAcrossViewport(t *testing.T) {
	tree := Tree{Nodes: []Node{
		{ID: 2, Parent: 1, Description: "Save all", Text: "Save", Bounds: image.Rect(0, 0, 400, 300)},
		{ID: 3, Parent: 2, Text: "Save", Bounds: image.Rect(0, 50, 400, 300)}, // a viewport
		{ID: 4, Parent: 3, Class: "Button", Clickable: true, Label: "Save", Text: "Save", Bounds: image.Rect(10, 60, 60, 92)},
	}}
	for _, keep := range []func(Node) bool{nil, func(n Node) bool { return n.Class == "Button" || n.ID == 2 }} {
		if got := tree.FindFunc("save", keep); len(got) != 1 || got[0].ID != 4 {
			t.Errorf("FindFunc(save) = %+v, want the button alone", got)
		}
	}
}

// What was drawn before the last modal layer is covered, but not the
// layer's ancestors nor what came after it; the markers lose their
// descriptions.
func TestFlattenCoversBeforeModal(t *testing.T) {
	nodes := flatten([]input.SemanticNode{{
		ID:   1,
		Desc: input.SemanticDesc{Bounds: image.Rect(-1e6, -1e6, 1e6, 1e6)},
		Children: []input.SemanticNode{
			{ID: 2, ParentID: 1, Desc: input.SemanticDesc{Class: semantic.Button, Label: "Restart", Bounds: image.Rect(0, 0, 80, 32)}},
			{ID: 3, ParentID: 1, Desc: input.SemanticDesc{Description: uimark.Modal, Bounds: image.Rect(0, 0, 800, 600)}},
			{ID: 4, ParentID: 1, Desc: input.SemanticDesc{Class: semantic.Button, Label: "Close", Bounds: image.Rect(300, 300, 360, 332)}},
		},
	}})
	byID := map[uint]Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if n := byID[2]; !n.Covered || !n.Visible.Empty() {
		t.Errorf("Restart = %+v, want covered", n)
	}
	if n := byID[4]; n.Covered || n.Visible.Empty() {
		t.Errorf("Close = %+v, want shown", n)
	}
	if n := byID[1]; n.Covered {
		t.Error("the root is covered")
	}
	if n := byID[3]; n.Description != "" {
		t.Errorf("the marker kept its description %q", n.Description)
	}
}

// A covered element does not name the element around it that shows.
func TestCoveredTextNamesNothing(t *testing.T) {
	nodes := flatten([]input.SemanticNode{{
		ID:   1,
		Desc: input.SemanticDesc{Bounds: image.Rect(-1e6, -1e6, 1e6, 1e6)},
		Children: []input.SemanticNode{
			{ID: 2, ParentID: 1, Desc: input.SemanticDesc{Description: uimark.Viewport, Bounds: image.Rect(0, 0, 800, 600)}, Children: []input.SemanticNode{
				{ID: 3, ParentID: 2, Desc: input.SemanticDesc{Class: semantic.Button, Label: "Restart", Bounds: image.Rect(0, 0, 80, 32)}},
				{ID: 4, ParentID: 2, Desc: input.SemanticDesc{Description: uimark.Modal, Bounds: image.Rect(0, 0, 800, 600)}},
				{ID: 5, ParentID: 2, Desc: input.SemanticDesc{Class: semantic.Button, Label: "Close", Bounds: image.Rect(300, 300, 360, 332)}},
			}},
		},
	}})
	for _, n := range nodes {
		if n.ID == 2 && n.Text != "Close" {
			t.Errorf("the viewport's text %q, want only what shows: Close", n.Text)
		}
	}
}

// Nothing shows outside the window, in a viewport or not.
func TestFlattenClipsToWindow(t *testing.T) {
	nodes := flattenIn([]input.SemanticNode{{
		ID:   1,
		Desc: input.SemanticDesc{Bounds: image.Rect(-1e6, -1e6, 1e6, 1e6)},
		Children: []input.SemanticNode{
			{ID: 2, ParentID: 1, Desc: input.SemanticDesc{Class: semantic.Button, Label: "Restart", Bounds: image.Rect(10, 900, 90, 932)}},
		},
	}}, image.Rect(0, 0, 800, 600))
	for _, n := range nodes {
		if n.ID == 2 && !n.Visible.Empty() {
			t.Errorf("a button below the window shows at %v", n.Visible)
		}
	}
}

// A request that gives up leaves the queue.
func TestAbandonedRequestLeavesQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := Serve(ctx, path, func() {}) // draws no frame
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{
		Timeout: 200 * time.Millisecond,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}},
	}
	for range 3 {
		if resp, err := client.Get("http://host/tree"); err == nil {
			resp.Body.Close()
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.pending)
		s.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d abandoned requests still queued", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The socket is the user's alone, whatever the umask.
func TestSocketPrivate(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), "debug.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := Serve(ctx, path, func() {}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("socket mode %v, want no access for others", perm)
	}
}

// A hidden element's whole text holds its hidden children too.
func TestAllTextHoldsHidden(t *testing.T) {
	nodes := flattenIn([]input.SemanticNode{{
		ID:   1,
		Desc: input.SemanticDesc{Bounds: image.Rect(-1e6, -1e6, 1e6, 1e6)},
		Children: []input.SemanticNode{{
			ID: 2, ParentID: 1, Desc: input.SemanticDesc{Description: uimark.Viewport, Bounds: image.Rect(0, 0, 400, 100)},
			Children: []input.SemanticNode{{
				ID: 3, ParentID: 2, Desc: input.SemanticDesc{Bounds: image.Rect(0, 200, 400, 230)},
				Children: []input.SemanticNode{
					{ID: 4, ParentID: 3, Desc: input.SemanticDesc{Label: "World", Bounds: image.Rect(0, 200, 100, 230)}},
					{ID: 5, ParentID: 3, Desc: input.SemanticDesc{Label: "clock", Bounds: image.Rect(100, 200, 200, 230)}},
				},
			}},
		}},
	}}, image.Rect(0, 0, 800, 600))
	for _, n := range nodes {
		if n.ID == 3 && (n.Text != "" || n.AllText != "World clock") {
			t.Errorf("row text %q, all text %q; want none shown, all World clock", n.Text, n.AllText)
		}
	}
}

// A label that shows is not taken for the echo of a covered button of
// the same label under it.
func TestEchoNeedsSameCoverage(t *testing.T) {
	nodes := withoutEchoes([]Node{
		{ID: 2, Class: "Button", Label: "Save", Text: "Save", Bounds: image.Rect(0, 0, 100, 40), Covered: true},
		{ID: 3, Label: "Save", Text: "Save", Bounds: image.Rect(10, 10, 300, 300), Visible: image.Rect(10, 10, 300, 300)},
	})
	if len(nodes) != 2 {
		t.Errorf("nodes %+v, want the shown label kept", nodes)
	}
}

// A path whose socket is made at a longer one first is refused when
// that one would not fit, before anything is made.
func TestCheckPathCountsTheFirstSocket(t *testing.T) {
	dir := "/" + strings.Repeat("d", 95)
	if err := CheckPath(dir + "/debug.sock"); err == nil { // 107 bytes, but up to 111 at first
		t.Error("CheckPath took a path whose first socket is too long")
	}
	if err := CheckPath("/run/user/1000/tessera-uidrive/default/debug.sock"); err != nil {
		t.Error(err)
	}
}

// Publishing fails rather than replace a file made at the path in the
// meantime, and the server's socket goes when it stops.
func TestServePublishesWithoutReplacing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debug.sock")
	l, fi, err := listenPrivate(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if fi == nil {
		t.Fatal("no file info")
	}
	os.Remove(path)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listenPrivate(path); err == nil {
		t.Error("listenPrivate replaced a file at the path")
	}
	if b, _ := os.ReadFile(path); string(b) != "keep" {
		t.Errorf("the file at the path is %q", b)
	}
	os.Remove(path)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := Serve(ctx, path, func() {}); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the socket outlived its server")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
