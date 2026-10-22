// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joncbenderkh/tessera/internal/config"
)

func TestSaveThemeKeepsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	edited := config.Default()
	edited.Grid.Columns = 6 // an edit made while tessera runs
	if err := config.Save(path, edited); err != nil {
		t.Fatal(err)
	}
	if err := saveTheme(path, false); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != config.ThemeLight || got.Grid.Columns != 6 {
		t.Fatalf("got theme %q and %d columns, want light and 6", got.Theme, got.Grid.Columns)
	}
}

func TestSaveThemeLeavesABrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	broken := []byte("version: 1\ngrid: [\n")
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveTheme(path, true); err == nil {
		t.Fatal("saving over a file that does not parse succeeded")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(broken) {
		t.Fatalf("the file was rewritten: %q", data)
	}
}

func TestThemeSaverWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatal(err)
	}
	s := themeSaver{path: path, done: make(chan error, 1), invalidate: func() {}}
	s.save(false)
	s.wait()
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != config.ThemeLight {
		t.Fatalf("theme %q after wait, want light", got.Theme)
	}
}

func TestThemeSaverIgnoresSavesAfterWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, config.Default()); err != nil {
		t.Fatal(err)
	}
	s := themeSaver{path: path, done: make(chan error, 1), invalidate: func() {}}
	s.wait()
	s.save(false)
	s.running.Wait()
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != "" {
		t.Fatalf("a save after wait wrote theme %q", got.Theme)
	}
}

func TestThemeSaverDropsASupersededError(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	s := themeSaver{done: make(chan error, 1), invalidate: func() {}}
	s.write = func(string, bool) error {
		calls++
		if calls == 1 {
			close(started)
			<-release
			return config.ErrChanged
		}
		return nil
	}
	s.save(false)
	<-started
	s.save(true)
	close(release)
	s.wait()
	select {
	case err := <-s.done:
		t.Fatalf("reported %v, which a later save replaced", err)
	default:
	}
}

func TestThemeSaverWritesQueuedSavesOnce(t *testing.T) {
	writes := 0
	s := themeSaver{done: make(chan error, 1), invalidate: func() {}}
	s.write = func(string, bool) error {
		writes++
		return nil
	}
	// Both saves are asked for before either goroutine starts writing.
	s.mu.Lock()
	s.save(false)
	s.save(true)
	s.mu.Unlock()
	s.wait()
	if writes != 1 {
		t.Fatalf("%d writes, want 1", writes)
	}
}
