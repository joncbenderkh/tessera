// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const example = `version: 1
grid:
  columns: 4
  row_height: 180
panels:
  - id: greeting
    plugin: greeting
    enabled: false
    x: 0
    y: 0
    w: 4
    h: 1
  - id: clock
    plugin: clock
    x: 0
    y: 0
    w: 1
    h: 1
    settings:
      timezone: Local
      show_seconds: no
`

func TestParseExample(t *testing.T) {
	c, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Panels) != 2 || c.Panels[0].IsEnabled() || !c.Panels[1].IsEnabled() {
		t.Fatalf("unexpected panels: %+v", c.Panels)
	}
	if got := c.Panels[1].Settings["show_seconds"]; got != "no" {
		t.Fatalf(`settings: "no" must stay a string (YAML 1.2), got %#v`, got)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(strings.Replace(example, "row_height", "row_hieght", 1)))
	if err == nil {
		t.Fatal("expected an error for the misspelt field")
	}
}

func TestValidate(t *testing.T) {
	panel := func(id string, x, y, w, h int) Panel {
		return Panel{ID: id, Plugin: "p", X: x, Y: y, W: w, H: h}
	}
	disabled := panel("b", 0, 0, 1, 1)
	disabled.Enabled = new(bool)

	tests := []struct {
		name   string
		panels []Panel
		want   string
	}{
		{"valid", []Panel{panel("a", 0, 0, 2, 1), panel("b", 2, 0, 2, 2)}, ""},
		{"duplicate id", []Panel{panel("a", 0, 0, 1, 1), panel("a", 1, 0, 1, 1)}, "not unique"},
		{"missing id", []Panel{panel("", 0, 0, 1, 1)}, "id is required"},
		{"zero span", []Panel{panel("a", 0, 0, 0, 1)}, "at least 1"},
		{"negative position", []Panel{panel("a", -1, 0, 1, 1)}, "must not be negative"},
		{"past last column", []Panel{panel("a", 3, 0, 2, 1)}, "the grid has 4"},
		{"overlap", []Panel{panel("a", 0, 0, 2, 2), panel("b", 1, 1, 1, 1)}, `overlaps panel "a"`},
		{"disabled panels may overlap", []Panel{panel("a", 0, 0, 1, 1), disabled}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Default()
			c.Panels = tt.panels
			err := c.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v does not contain %q", err, tt.want)
			}
		})
	}
}

func TestValidateGrid(t *testing.T) {
	c := Config{Version: 2}
	err := c.Validate()
	for _, want := range []string{"version 2", "grid.columns", "grid.row_height"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q", err, want)
		}
	}
}

func TestLoadWritesDefaultOnFirstRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tessera", FileName)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, Default()) {
		t.Fatalf("got %+v, want the default", c)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatalf("reloading the written default: %v", err)
	}
	if !reflect.DeepEqual(again, c) {
		t.Fatalf("round trip changed the config: %+v", again)
	}
}

func TestSaveRoundTripsAndLeavesNoTempFiles(t *testing.T) {
	want, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only %s in the directory, found %d entries", FileName, len(entries))
	}
}

func TestSaveRejectsInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, Config{}); err == nil {
		t.Fatal("expected a validation error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an invalid config must not be written, stat: %v", err)
	}
}
