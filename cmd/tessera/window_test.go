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
