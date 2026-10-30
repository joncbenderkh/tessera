// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
)

// Every first-party plugin is installed under the name it gives itself,
// which the host finds, and nothing is left beside them.
func TestInstallPlugins(t *testing.T) {
	if testing.Short() {
		t.Skip("builds every plugin")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "plugins")
	names, err := installPlugins(root, dest)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := pluginhost.List(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"calendar", "clock", "greeting", "notes", "worldclock"} {
		if !slices.Contains(names, want) {
			t.Errorf("installed %q, missing %q", names, want)
		}
	}
	if !slices.Equal(names, listed) {
		t.Errorf("installed %q, host lists %q", names, listed)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Errorf("plugins directory holds %d entries, want %d", len(entries), len(names))
	}
}

// An installed plugin replaces the one before it.
func TestInstallReplaces(t *testing.T) {
	staging, dest := t.TempDir(), t.TempDir()
	for dir, content := range map[string]string{staging: "new", dest: "old"} {
		if err := os.WriteFile(filepath.Join(dir, "clock"+exeSuffix()), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	names, err := install(staging, dest)
	if err != nil || !slices.Equal(names, []string{"clock"}) {
		t.Fatalf("install: %q, %v", names, err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "clock"+exeSuffix())); err != nil || string(got) != "new" {
		t.Errorf("clock holds %q, %v; want new", got, err)
	}
}
