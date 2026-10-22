// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type change struct {
	config Config
	err    error
}

func TestWatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	changes := make(chan change, 8)
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, path, read(t, path), func(c Config, err error) { changes <- change{c, err} })
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch: %v", err)
		}
	}()
	time.Sleep(50 * time.Millisecond) // let the watcher register before writing

	next := func() change {
		t.Helper()
		select {
		case c := <-changes:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("no change reported")
			return change{}
		}
	}

	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	wider := Default()
	wider.Grid.Columns = 6
	if err := Save(path, wider); err != nil {
		t.Fatal(err)
	}
	if c := next(); c.err != nil || c.config.Grid.Columns != 6 {
		t.Fatalf("after saving 6 columns: got %+v", c)
	}

	if err := os.WriteFile(path, []byte("version: 1\ngrid: {columns: 0, row_height: 1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := next(); c.err == nil {
		t.Fatal("expected an error for an invalid edit")
	}

	select {
	case c := <-changes:
		t.Fatalf("unexpected extra change: %+v", c)
	case <-time.After(3 * settleDelay):
	}
}

func TestWatchFollowsSymlink(t *testing.T) {
	if err := os.Symlink(t.TempDir(), filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dotfiles, other := t.TempDir(), t.TempDir()
	target := filepath.Join(dotfiles, "tessera.yaml")
	if err := Save(target, Default()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	changes := make(chan change, 8)
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, path, read(t, path), func(c Config, err error) { changes <- change{c, err} })
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch: %v", err)
		}
	}()
	time.Sleep(50 * time.Millisecond) // let the watcher register before writing

	columns := func(n int) Config {
		c := Default()
		c.Grid.Columns = n
		return c
	}
	want := func(step string, n int) {
		t.Helper()
		select {
		case c := <-changes:
			if c.err != nil || c.config.Grid.Columns != n {
				t.Fatalf("%s: got %+v, want %d columns", step, c, n)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: no change reported", step)
		}
	}

	if err := Save(path, columns(5)); err != nil {
		t.Fatal(err)
	}
	want("saving through the link", 5)
	if err := Save(target, columns(6)); err != nil {
		t.Fatal(err)
	}
	want("saving the target", 6)

	moved := filepath.Join(other, "tessera.yaml")
	if err := Save(moved, columns(7)); err != nil {
		t.Fatal(err)
	}
	swap := path + ".new"
	if err := os.Symlink(moved, swap); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(swap, path); err != nil {
		t.Fatal(err)
	}
	want("pointing the link elsewhere", 7)
	if err := Save(moved, columns(8)); err != nil {
		t.Fatal(err)
	}
	want("saving the new target", 8)
}

// A link can point at a file before it exists, as when a dotfiles
// checkout is linked first and cloned second.
func TestWatchFollowsDanglingSymlink(t *testing.T) {
	if err := os.Symlink(t.TempDir(), filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dotfiles := filepath.Join(t.TempDir(), "dotfiles")
	target := filepath.Join(dotfiles, "tessera.yaml")
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	changes := make(chan change, 8)
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, path, read(t, path), func(c Config, err error) { changes <- change{c, err} })
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch: %v", err)
		}
	}()
	time.Sleep(50 * time.Millisecond) // let the watcher register before writing

	want := func(step string, n int) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case c := <-changes:
				if c.err == nil && c.config.Grid.Columns == n {
					return
				}
			case <-deadline:
				t.Fatalf("%s: no change reported", step)
			}
		}
	}
	c := Default()
	if err := os.Mkdir(dotfiles, 0o755); err != nil {
		t.Fatal(err)
	}
	c.Grid.Columns = 5
	if err := Save(target, c); err != nil {
		t.Fatal(err)
	}
	want("creating the target", 5)
	c.Grid.Columns = 6
	if err := Save(target, c); err != nil {
		t.Fatal(err)
	}
	want("saving the target", 6)
}

// read is path's content, or nil if there is none.
func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return data
}

// An edit made after the caller read the file but before Watch started
// is reported.
func TestWatchReportsEditBeforeStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	_, data, err := Read(path) // writes Default
	if err != nil {
		t.Fatal(err)
	}
	edited := Default()
	edited.Grid.Columns = 5
	if err := Save(path, edited); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	changes := make(chan change, 8)
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, path, data, func(c Config, err error) { changes <- change{c, err} })
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch: %v", err)
		}
	}()
	select {
	case c := <-changes:
		if c.err != nil || c.config.Grid.Columns != 5 {
			t.Fatalf("got %+v, want the edit", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the edit made before Watch started was not reported")
	}
}

// watching runs Watch on path until the test ends and returns a function
// that waits for a valid change to n columns, skipping errors and other
// changes.
func watching(t *testing.T, path string) func(step string, n int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	changes := make(chan change, 8)
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, path, read(t, path), func(c Config, err error) { changes <- change{c, err} })
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch: %v", err)
		}
	})
	time.Sleep(50 * time.Millisecond) // let the watcher register before writing
	return func(step string, n int) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case c := <-changes:
				if c.err == nil && c.config.Grid.Columns == n {
					return
				}
			case <-deadline:
				t.Fatalf("%s: no change to %d columns reported", step, n)
			}
		}
	}
}

func columns(n int) Config {
	c := Default()
	c.Grid.Columns = n
	return c
}

// A symlinked config directory, as a dotfiles manager makes, is followed
// when it is pointed at another directory.
func TestWatchFollowsLinkedDirectory(t *testing.T) {
	if err := os.Symlink(t.TempDir(), filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	first, second := t.TempDir(), t.TempDir()
	for _, dir := range []string{first, second} {
		if err := Save(filepath.Join(dir, FileName), Default()); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "tessera")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(link, FileName)
	want := watching(t, path)

	if err := Save(path, columns(5)); err != nil {
		t.Fatal(err)
	}
	want("saving in the linked directory", 5)
	if err := Save(filepath.Join(second, FileName), columns(6)); err != nil {
		t.Fatal(err)
	}
	repoint(t, second, link)
	want("pointing the directory elsewhere", 6)
	for _, n := range []int{7, 8} {
		if err := Save(filepath.Join(second, FileName), columns(n)); err != nil {
			t.Fatal(err)
		}
		want("saving in the new directory", n)
	}
}

// A linked config directory whose config.yaml links to a file beside it:
// events name the file by the directory's link, not by its real path.
func TestWatchFollowsLinkInLinkedDirectory(t *testing.T) {
	if err := os.Symlink(t.TempDir(), filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	real := t.TempDir()
	target := filepath.Join(real, "work.yaml")
	if err := Save(target, Default()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("work.yaml", filepath.Join(real, FileName)); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "tessera")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(link, FileName)
	want := watching(t, path)

	if err := Save(target, columns(5)); err != nil {
		t.Fatal(err)
	}
	want("saving the target", 5)
	if err := Save(path, columns(6)); err != nil {
		t.Fatal(err)
	}
	want("saving through both links", 6)
}

// Read must not write Default over a link to a file not there yet: the
// link would become a file, and the dotfiles cloned later be ignored.
func TestReadKeepsDanglingLink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "dotfiles", "tessera.yaml")
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := Read(path); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Read: %v, want an error naming the missing target", err)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", fi.Mode(), err)
	}
}

// A link's target directory replaced by another of the same name, as a
// dotfiles checkout can do, is watched again.
func TestWatchFollowsReplacedTargetDirectory(t *testing.T) {
	if err := os.Symlink(t.TempDir(), filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root := t.TempDir()
	dots := filepath.Join(root, "dots")
	for _, dir := range []string{dots, dots + ".new"} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(filepath.Join(dots, FileName), Default()); err != nil {
		t.Fatal(err)
	}
	if err := Save(filepath.Join(dots+".new", FileName), columns(5)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.Symlink(filepath.Join(dots, FileName), path); err != nil {
		t.Fatal(err)
	}
	want := watching(t, path)

	if err := os.Rename(dots, dots+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dots+".new", dots); err != nil {
		t.Fatal(err)
	}
	want("replacing the directory", 5)
	if err := Save(filepath.Join(dots, FileName), columns(6)); err != nil {
		t.Fatal(err)
	}
	want("saving in the new directory", 6)
}

// A missing config directory is reported once, not at every check.
func TestWatchReportsMissingDirectoryOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tessera")
	path := filepath.Join(dir, FileName)
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var errs []error
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, path, read(t, path), func(_ Config, err error) {
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
		})
	}()
	time.Sleep(50 * time.Millisecond) // let the watcher register before removing
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3*linkCheck + settleDelay)
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Watch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(errs) != 1 {
		t.Errorf("%d errors reported, want 1: %v", len(errs), errs)
	}
}

// repoint points the link at link to target. It replaces the link in
// one rename, as dotfiles managers do, where it can: Windows refuses to
// rename over a link to a directory, so there it removes the link first.
func repoint(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		return
	}
	swap := link + ".new"
	if err := os.Symlink(target, swap); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(swap, link); err != nil {
		t.Fatal(err)
	}
}
