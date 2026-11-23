// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"image"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joncbenderkh/tessera/internal/uidebug"
)

// A class narrows the elements before matching, so an exact match of
// another class does not hide a substring match of this one.
func TestPickClassBeforeMatching(t *testing.T) {
	tree := uidebug.Tree{Size: image.Pt(800, 600), Nodes: []uidebug.Node{
		{ID: 1, Label: "Save", Text: "Save", Bounds: image.Rect(10, 10, 100, 30)},
		{ID: 2, Class: "Button", Clickable: true, Label: "Save settings", Text: "Save settings", Bounds: image.Rect(10, 50, 120, 82)},
	}}
	n, err := pick(tree, "Save", "Button", 0)
	if err != nil || n.ID != 2 {
		t.Errorf("pick = %+v, %v; want the button", n, err)
	}
	if n, err := pick(tree, "", "button", 1); err != nil || n.ID != 2 {
		t.Errorf("pick by class alone = %+v, %v; want the button", n, err)
	}
}

func TestParseStat(t *testing.T) {
	// The command name may hold spaces and parentheses.
	line := "4242 (a (b) c) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 10\n"
	st, err := parseStat(line)
	if err != nil || st.state != 'S' || st.start != 987654 {
		t.Errorf("parseStat = %+v, %v; want state S, start 987654", st, err)
	}
	if _, err := parseStat("4242 (x) S 1 2"); err == nil {
		t.Error("parseStat took a short line")
	}
}

func TestSessionDirName(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	for _, bad := range []string{"", ".", "..", "a/b", "../x", ".locks"} {
		if _, err := sessionDir(bad); err == nil {
			t.Errorf("sessionDir(%q) took it", bad)
		}
	}
	if _, err := sessionDir("default"); err != nil {
		t.Error(err)
	}
}

// A base directory others can open is refused.
func TestSessionDirShared(t *testing.T) {
	run := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", run)
	if _, err := sessionDir("default"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(run+"/tessera-uidrive", 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionDir("default"); err == nil {
		t.Error("sessionDir took a directory anyone can open")
	}
}

func TestParsePoint(t *testing.T) {
	for in, want := range map[string]bool{"3,4": true, " 3 , 4 ": true, "3,4 items": false, "Save": false, "3,": false, ",4": false, "40000,5": false} {
		if _, ok := parsePoint(in); ok != want {
			t.Errorf("parsePoint(%q) ok = %v, want %v", in, ok, want)
		}
	}
	if p, _ := parsePoint("12,-5"); p != image.Pt(12, -5) {
		t.Errorf("parsePoint(12,-5) = %v", p)
	}
}

// An element is aimed at its center, and text near its start, as its
// bounds reach far past it.
func TestAim(t *testing.T) {
	button := uidebug.Node{Class: "Button", Bounds: image.Rect(0, 0, 300, 50), Visible: image.Rect(0, 0, 300, 50)}
	if at := aim(button); at != image.Pt(150, 25) {
		t.Errorf("aim(button) = %v, want its center", at)
	}
	title := uidebug.Node{Label: "Review", Bounds: image.Rect(53, 12, 287, 1000012), Visible: image.Rect(53, 12, 287, 400)}
	if at := aim(title); at != image.Pt(57, 16) {
		t.Errorf("aim(text) = %v, want just inside its start", at)
	}
	// A row half scrolled out of its list, which shows from y = 100.
	row := uidebug.Node{Class: "Button", Bounds: image.Rect(0, 80, 300, 130), Visible: image.Rect(0, 100, 300, 130)}
	if at := aim(row); at != image.Pt(150, 115) {
		t.Errorf("aim(half-hidden row) = %v, want the center of what shows", at)
	}
}

// The Xauthority file holds one wildcard MIT-MAGIC-COOKIE-1 entry.
func TestWriteXauth(t *testing.T) {
	path := t.TempDir() + "/Xauthority"
	if err := writeXauth(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	name := "MIT-MAGIC-COOKIE-1"
	want := 2 + 2 + 2 + 2 + len(name) + 2 + 16
	if len(b) != want || b[0] != 0xff || b[1] != 0xff || string(b[8:8+len(name)]) != name {
		t.Errorf("Xauthority = % x", b)
	}
}

// A second lock on a session waits for the first.
func TestLockSession(t *testing.T) {
	dir := t.TempDir() + "/default"
	unlock, err := lockSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		unlock2, err := lockSession(dir)
		if err == nil {
			unlock2()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("the second lock did not wait")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the second lock never came")
	}
}

// tree returns the tree the host sends.
func TestSessionTree(t *testing.T) {
	s := session{Dir: t.TempDir(), Host: process{PID: os.Getpid()}}
	self, err := started(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	s.Host = self
	l, err := net.Listen("unix", s.socket())
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"size":{"X":640,"Y":480},"nodes":[{"id":1,"label":"Save"}]}`))
	})}
	go srv.Serve(l)
	defer srv.Close()
	tree, err := s.tree(time.Second)
	if err != nil || tree.Size != image.Pt(640, 480) || len(tree.Nodes) != 1 {
		t.Errorf("tree = %+v, %v", tree, err)
	}
}

// A session's lock does not take the name of another session's
// directory.
func TestLockApartFromSessions(t *testing.T) {
	run := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", run)
	dir, err := sessionDir("x")
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if other, err := sessionDir("x.lock"); err != nil {
		t.Fatal(err)
	} else if err := os.MkdirAll(other, 0o700); err != nil {
		t.Errorf("the session x.lock cannot have its directory: %v", err)
	}
}

// A session whose socket path would pass a socket's limit is refused
// before anything starts.
func TestStartRefusesLongSocketPath(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	long := strings.Repeat("s", 100)
	if _, err := startSession(long, startOptions{host: "/nonexistent", size: image.Pt(10, 10)}); err == nil || !strings.Contains(err.Error(), "107") {
		t.Errorf("startSession with a long name: %v, want the socket path refused", err)
	}
}

// With a class, an ancestor of that class that matches only through a
// button below a viewport is not a candidate.
func TestPickClassKeepsAncestry(t *testing.T) {
	tree := uidebug.Tree{Size: image.Pt(800, 600), Nodes: []uidebug.Node{
		{ID: 2, Parent: 1, Class: "Button", Clickable: true, Description: "Save all", Text: "Save", Bounds: image.Rect(0, 0, 400, 300), Visible: image.Rect(0, 0, 400, 300)},
		{ID: 3, Parent: 2, Text: "Save", Bounds: image.Rect(0, 50, 400, 300), Visible: image.Rect(0, 50, 400, 300)},
		{ID: 4, Parent: 3, Class: "Button", Clickable: true, Label: "Save", Text: "Save", Bounds: image.Rect(10, 60, 60, 92), Visible: image.Rect(10, 60, 60, 92)},
	}}
	if n, err := pick(tree, "Save", "Button", 0); err != nil || n.ID != 4 {
		t.Errorf("pick = %+v, %v; want the inner button", n, err)
	}
}

func TestTarget(t *testing.T) {
	if at, err := target(nil, "120,40", ""); err != nil || at != image.Pt(120, 40) {
		t.Errorf("target(--at 120,40) = %v, %v", at, err)
	}
	if _, err := target(nil, "", "Button"); err != nil {
		t.Errorf("--class without NAME is refused: %v", err)
	}
	if _, err := target([]string{"12,5"}, "", ""); err != nil {
		t.Errorf("a name like 12,5 is refused: %v", err)
	}
	for _, c := range []struct {
		args         []string
		point, class string
	}{
		{nil, "", ""}, {[]string{""}, "", ""}, {[]string{" "}, "", ""}, {[]string{"Save"}, "1,2", ""}, {nil, "1,2", "Button"}, {nil, "a,b", ""},
	} {
		if _, err := target(c.args, c.point, c.class); err == nil {
			t.Errorf("target(%v, %q, %q) took it", c.args, c.point, c.class)
		}
	}
}

// Of two elements of one name, the one that shows is picked; one that
// does not is picked only when nothing shown has the name.
func TestPickPrefersShown(t *testing.T) {
	tree := uidebug.Tree{Size: image.Pt(800, 600), Nodes: []uidebug.Node{
		{ID: 2, Class: "Button", Clickable: true, Label: "Restart", Text: "Restart", Bounds: image.Rect(10, 900, 90, 932)},
		{ID: 3, Class: "Button", Clickable: true, Label: "Restart", Text: "Restart", Bounds: image.Rect(10, 100, 90, 132), Visible: image.Rect(10, 100, 90, 132)},
		{ID: 4, Class: "Button", Clickable: true, Label: "Close", Text: "Close", Bounds: image.Rect(10, 200, 90, 232), Covered: true},
	}}
	if n, err := pick(tree, "Restart", "", 0); err != nil || n.ID != 3 {
		t.Errorf("pick(Restart) = %+v, %v; want the shown one", n, err)
	}
	if n, err := pick(tree, "Close", "", 0); err != nil || n.ID != 4 {
		t.Errorf("pick(Close) = %+v, %v; want the covered one, to refuse it", n, err)
	}
}

// An element of exactly the name beats one that shows and only holds
// it, even hidden, to be refused with its reason; with none that shows,
// the first is taken at once, and a class alone is worded as such.
func TestPickExactBeatsShownSubstring(t *testing.T) {
	tree := uidebug.Tree{Size: image.Pt(800, 600), Nodes: []uidebug.Node{
		{ID: 2, Class: "Button", Clickable: true, Label: "Restart", Text: "Restart", Bounds: image.Rect(10, 900, 90, 932)},
		{ID: 3, Class: "Button", Clickable: true, Label: "Restart all", Text: "Restart all", Bounds: image.Rect(10, 100, 90, 132), Visible: image.Rect(10, 100, 90, 132)},
		{ID: 4, Class: "Button", Clickable: true, Label: "Close", Text: "Close", Bounds: image.Rect(10, 200, 90, 232), Covered: true},
		{ID: 5, Class: "Button", Clickable: true, Label: "Close", Text: "Close", Bounds: image.Rect(10, 300, 90, 332), Covered: true},
	}}
	if n, err := pick(tree, "Restart", "", 0); err != nil || n.ID != 2 {
		t.Errorf("pick(Restart) = %+v, %v; want the hidden Restart, not Restart all", n, err)
	}
	if n, err := pick(tree, "Restart a", "", 0); err != nil || n.ID != 3 {
		t.Errorf("pick(Restart a) = %+v, %v; want the shown Restart all", n, err)
	}
	if n, err := pick(tree, "Close", "", 0); err != nil || n.ID != 4 {
		t.Errorf("pick(Close) = %+v, %v; want the first covered one, not an ambiguity", n, err)
	}
	if _, err := pick(tree, "", "Editor", 0); err == nil || strings.Contains(err.Error(), `""`) {
		t.Errorf("pick(--class Editor) error %v, want one without an empty name", err)
	}
}

// A stop that cannot end a process keeps the session's state, so that a
// later stop can try again.
func TestStopKeepsStateOfSurvivor(t *testing.T) {
	kill = func(int, syscall.Signal) error { return syscall.EPERM } // no real signal
	defer func() { kill = syscall.Kill }()
	self, err := started(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	s := session{Dir: t.TempDir(), Host: self}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if err := s.stop(); err == nil {
		t.Error("stop reported success with the host still running")
	}
	if _, err := os.Stat(s.Dir + "/state.json"); err != nil {
		t.Errorf("state.json is gone: %v", err)
	}
}

// Without a name or class there is nothing to pick by.
func TestPickNeedsNameOrClass(t *testing.T) {
	tree := uidebug.Tree{Size: image.Pt(800, 600), Nodes: []uidebug.Node{
		{ID: 3, Class: "Button", Clickable: true, Label: "Delete all", Bounds: image.Rect(10, 10, 90, 42), Visible: image.Rect(10, 10, 90, 42)},
	}}
	for _, nth := range []int{0, 1} {
		if n, err := pick(tree, "", "", nth); err == nil {
			t.Errorf("pick(\"\", nth %d) = %+v", nth, n)
		}
	}
}

// The state file is replaced whole, leaving no temporary file behind.
func TestSaveAtomic(t *testing.T) {
	s := session{Dir: t.TempDir(), Display: ":9"}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Errorf("session directory holds %v, want state.json alone", entries)
	}
}

// A blank NAME with a class picks among all of the class, as no NAME.
func TestPickBlankNameWithClass(t *testing.T) {
	tree := uidebug.Tree{Size: image.Pt(800, 600), Nodes: []uidebug.Node{
		{ID: 3, Class: "Button", Clickable: true, Label: "Save", Bounds: image.Rect(10, 10, 90, 42), Visible: image.Rect(10, 10, 90, 42)},
	}}
	if n, err := pick(tree, " ", "Button", 0); err != nil || n.ID != 3 {
		t.Errorf("pick(\" \", Button) = %+v, %v; want the button", n, err)
	}
}

// start leaves a session whose state cannot be read.
func TestStartKeepsUnreadableSession(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir, err := sessionDir("default")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/state.json", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/config.yaml", []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := startSession("default", startOptions{host: "/nonexistent", size: image.Pt(10, 10)}); err == nil {
		t.Error("start took over a session whose state cannot be read")
	}
	if b, _ := os.ReadFile(dir + "/config.yaml"); string(b) != "keep" {
		t.Errorf("config.yaml = %q, want it untouched", b)
	}
}

// A process that is gone by the time stop signals it is stopped, not a
// failure.
func TestStopProcessAlreadyGone(t *testing.T) {
	self, err := started(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	gone := false
	kill = func(int, syscall.Signal) error { gone = true; return syscall.ESRCH }
	defer func() { kill = syscall.Kill }()
	// alive keeps reporting this process, so stop waits; the kill's
	// ESRCH alone must not be its error.
	s := session{Dir: t.TempDir(), Host: self}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	err = s.stop()
	if !gone || err == nil || errors.Is(err, syscall.ESRCH) {
		t.Errorf("stop = %v, want only the survivor error, not ESRCH", err)
	}
}

func TestWaitNeedsName(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	cmd := newRoot()
	cmd.SetArgs([]string{"wait", " ", "--gone"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "give NAME") {
		t.Errorf("wait \" \" --gone = %v, want give NAME", err)
	}
}

// A hidden element named exactly by labels across its children, which
// do not show, still beats a shown element that holds the name.
func TestPickHiddenExactAcrossLabels(t *testing.T) {
	tree := uidebug.Tree{Size: image.Pt(800, 600), Nodes: []uidebug.Node{
		{ID: 2, Parent: 1, Bounds: image.Rect(0, 0, 400, 100), Visible: image.Rect(0, 0, 400, 100)}, // a viewport
		{ID: 3, Parent: 2, AllText: "World clock", Bounds: image.Rect(0, 200, 400, 230)},
		{ID: 4, Parent: 3, Label: "World", Text: "World", AllText: "World", Bounds: image.Rect(0, 200, 100, 230)},
		{ID: 5, Parent: 3, Label: "clock", Text: "clock", AllText: "clock", Bounds: image.Rect(100, 200, 200, 230)},
		{ID: 6, Parent: 1, Class: "Button", Clickable: true, Label: "World clock options", Text: "World clock options", AllText: "World clock options", Bounds: image.Rect(0, 300, 100, 330), Visible: image.Rect(0, 300, 100, 330)},
	}}
	if n, err := pick(tree, "World clock", "", 0); err != nil || n.ID != 3 {
		t.Errorf("pick(World clock) = %+v, %v; want the hidden row", n, err)
	}
}

func TestPickNegativeNth(t *testing.T) {
	tree := uidebug.Tree{Size: image.Pt(800, 600), Nodes: []uidebug.Node{
		{ID: 3, Class: "Button", Clickable: true, Label: "Save", Bounds: image.Rect(10, 10, 90, 42), Visible: image.Rect(10, 10, 90, 42)},
	}}
	if _, err := pick(tree, "Save", "", -1); err == nil {
		t.Error("pick took --nth -1")
	}
}

// A stop after a start that wrote no state is not a failure.
func TestStopWithoutStateFile(t *testing.T) {
	if err := (session{Dir: t.TempDir()}).stop(); err != nil {
		t.Errorf("stop = %v", err)
	}
}

// An element that shows is named by what shows of it alone: a row whose
// "clock" is clipped is not "World clock", and a word scrolled out of a
// row that shows does not name it.
func TestPickShownByWhatShows(t *testing.T) {
	tree := uidebug.Tree{Size: image.Pt(800, 600), Nodes: []uidebug.Node{
		{ID: 3, Parent: 2, AllText: "World clock", Bounds: image.Rect(0, 20, 400, 50)},
		{ID: 6, Parent: 2, Text: "World", AllText: "World clock", Bounds: image.Rect(0, 80, 400, 110), Visible: image.Rect(0, 80, 400, 100)},
		{ID: 7, Parent: 2, Class: "Button", Clickable: true, Label: "", Text: "Open", AllText: "Open Secret", Bounds: image.Rect(0, 120, 400, 150), Visible: image.Rect(0, 120, 400, 150)},
	}}
	if n, err := pick(tree, "World clock", "", 0); err != nil || n.ID != 3 {
		t.Errorf("pick(World clock) = %+v, %v; want the hidden row, to be refused", n, err)
	}
	if n, err := pick(tree, "Secret", "Button", 0); err == nil {
		t.Errorf("pick(Secret, Button) = %+v; want nothing, Secret does not show", n)
	}
}
