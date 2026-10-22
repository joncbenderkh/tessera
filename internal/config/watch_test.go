// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"context"
	"os"
	"path/filepath"
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
		done <- Watch(ctx, path, func(c Config, err error) { changes <- change{c, err} })
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
		done <- Watch(ctx, path, func(c Config, err error) { changes <- change{c, err} })
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
