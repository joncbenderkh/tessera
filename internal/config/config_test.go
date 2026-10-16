// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestParseRejectsFractionalIntegers(t *testing.T) {
	for _, tt := range []struct{ old, new string }{
		{"version: 1", "version: 1.9"},
		{"    x: 0\n", "    x: -0.5\n"},
		{"    w: 4\n", "    w: 1e0\n"},
	} {
		data := strings.Replace(example, tt.old, tt.new, 1)
		_, err := Parse([]byte(data))
		if err == nil || !strings.Contains(err.Error(), "not an integer") {
			t.Errorf("%s: expected a not-an-integer error, got %v", strings.TrimSpace(tt.new), err)
		}
	}
}

// withSetting returns the example config with the clock's show_seconds
// set to value, and an anchor on its plugin for alias tests.
func withSetting(value string) []byte {
	data := strings.Replace(example, "show_seconds: no", "show_seconds: "+value, 1)
	return []byte(strings.Replace(data, "plugin: clock", "plugin: &other clock", 1))
}

func TestParseRejectsSettings(t *testing.T) {
	for _, tt := range []struct{ value, want string }{
		{".inf", "must be a finite number"},
		{"-.Inf", "must be a finite number"},
		{".nan", "must be a finite number"},
		{"{deep: [1, .inf]}", "must be a finite number"},
		{"!!timestamp 2026-10-15", "may not use the !!timestamp tag"},
		{"!!int 99999999999999999999", "may not use the !!int tag"},
		{"!!binary aGk=", "may not use the !!binary tag"},
		{"!!map {a: 1}", "may not use the !!map tag"},
		{"!custom 1", "may not use the !custom tag"},
		{"*other", "aliases are not supported"},
		{"{<<: *other}", "aliases are not supported"},
		{"{*other : 1}", "aliases are not supported"},
		{"1e400", "out of range for a double"},
		{"-.5e999", "out of range for a double"},
		{"1e-400", "too small for a double"},
		{"-0.5e-999", "too small for a double"},
		{"99999999999999999999", "beyond ±2^53"},
		{"9007199254740993", "beyond ±2^53"},
		{"-9007199254740993", "beyond ±2^53"},
		{"{deep: [0x1FFFFFFFFFFFFFFFFF]}", "0x1FFFFFFFFFFFFFFFFF is beyond ±2^53"},
		{"{!!int 17: x}", "may not use the !!int tag"},
		{"{? !!timestamp 2026-01-01 : x}", "may not use the !!timestamp tag"},
	} {
		_, err := Parse(withSetting(tt.value))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: expected %q, got %v", tt.value, tt.want, err)
		}
	}
}

func TestParseResolvesSettingsByCoreSchema(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  any
	}{
		{"017", int64(17)},
		{"08", int64(8)},
		{"-017", int64(-17)},
		{"0o17", int64(15)},
		{"0x1F", int64(31)},
		{"9007199254740992", int64(1 << 53)},
		{"-9007199254740992", int64(-1 << 53)},
		{"{!!str 017: x}", map[string]any{"017": "x"}},
		{"!!str", ""},
		{"!!str null", "null"},
		{"{a: !!str, b: 1}", map[string]any{"a": "", "b": int64(1)}},
		{"0e5", 0.0},
		{"5e-324", 5e-324},
		{"\n        ? |-\n          block\n        : x", map[string]any{"block": "x"}},
		{"1e5", 100000.0},
		{"+.5", 0.5},
		{"1_000", "1_000"},
		{"0b101", "0b101"},
		{"no", "no"},
		{"True", true},
		{"~", nil},
		{"2026-10-15", "2026-10-15"},
		{`"1e400"`, "1e400"},
		{"'017'", "017"},
		{"!!str 017", "017"},
		{"&s 3", int64(3)},
		{"[1, x]", []any{int64(1), "x"}},
		{"{017: x}", map[string]any{"017": "x"}},
		{"{<<: {a: 1}, b: 2}", map[string]any{"<<": map[string]any{"a": int64(1)}, "b": int64(2)}},
		{"{<<: 1}", map[string]any{"<<": int64(1)}},
		{"{k: [{<<: x}]}", map[string]any{"k": []any{map[string]any{"<<": "x"}}}},
	} {
		c, err := Parse(withSetting(tt.value))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tt.value, err)
			continue
		}
		if got := c.Panels[1].Settings["show_seconds"]; !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %#v, want %#v", tt.value, got, tt.want)
		}
	}
}

func TestParseResolvesDecoratedSettingsKey(t *testing.T) {
	for _, key := range []string{`"settings":`, "!!str settings:", "&k settings:", "? settings\n    :"} {
		data := strings.Replace(string(withSetting("017")), "    settings:", "    "+key, 1)
		c, err := Parse([]byte(data))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", key, err)
			continue
		}
		if got := c.Panels[1].Settings["show_seconds"]; got != int64(17) {
			t.Errorf("%s: got %#v, want 17", key, got)
		}
	}
}

func TestParseReadsIDAndPluginAsWritten(t *testing.T) {
	for _, text := range []string{"017", "0x1f", "0b101", "1e5", "7zip"} {
		data := strings.Replace(example, "id: clock\n    plugin: clock", "id: "+text+"\n    plugin: "+text, 1)
		c, err := Parse([]byte(data))
		if err != nil {
			t.Errorf("%s: unexpected error: %v", text, err)
			continue
		}
		if p := c.Panels[1]; p.ID != text || p.Plugin != text {
			t.Errorf("%s: got id %q, plugin %q", text, p.ID, p.Plugin)
		}
	}
}

func TestParseRejectsMergeKeysOutsideSettings(t *testing.T) {
	data := strings.Replace(example, "  - id: clock\n", "  - <<: {id: clock}\n", 1)
	if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "merge keys are not supported") {
		t.Fatalf("expected a merge-key error, got %v", err)
	}
}

func TestSaveQuotesCoreSchemaLookalikes(t *testing.T) {
	c := Default()
	settings := map[string]any{
		"nested": []any{"1e5", map[string]any{"08": "08"}, []any{}, map[string]any{}},
		"<<":     "merge", "a\tb": "tab key", "a\nb": "newline key", "": "empty key", "1e5": "number key",
		"float": 100000.0, "int": int64(-17), "big": int64(1 << 53), "bool": true, "null": nil,
	}
	for i, s := range []string{"1e5", "08", "017", "0o17", "0x1F", "True", "~", "NULL", "-.5", ".inf", "1_000", "yes", "a: b", "tab\there", "plain", "", " lead", "trail ", "#x", "- x", "Europe/Berlin"} {
		settings[fmt.Sprint("k", i)] = s
	}
	c.Panels = []Panel{{ID: "a", Plugin: "p", W: 1, H: 1, Settings: settings}}
	data, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if !reflect.DeepEqual(map[string]any(back.Panels[0].Settings), settings) {
		t.Fatalf("round trip changed settings:\ngot  %#v\nwant %#v\n%s", back.Panels[0].Settings, settings, data)
	}
}

func TestValidateMatchesMarshal(t *testing.T) {
	for _, tt := range []struct {
		value any
		want  string
	}{
		{time.Now(), "unsupported settings value time.Time"},
		{int32(3), "unsupported settings value int32"},
		{float32(0.5), "unsupported settings value float32"},
		{[]byte("hi"), "unsupported settings value []uint8"},
		{int64(1<<53 + 1), "beyond ±2^53"},
		{uint64(1<<53 + 1), "beyond ±2^53"},
		{"\xff", "not valid UTF-8"},
		{map[string]any{"\xff": 1}, "not valid UTF-8"},
		{Settings{"n": 1}, ""},
		{[]any{Settings{"n": int64(1)}, 2.5, nil, true}, ""},
	} {
		c := Default()
		c.Panels = []Panel{{ID: "a", Plugin: "p", W: 1, H: 1, Settings: Settings{"v": tt.value}}}
		err := c.Validate()
		if tt.want == "" {
			if err != nil {
				t.Errorf("%#v: unexpected error: %v", tt.value, err)
			} else if _, err := Marshal(c); err != nil {
				t.Errorf("%#v: validates but does not save: %v", tt.value, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "settings.v") || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%#v: expected %q at settings.v, got %v", tt.value, tt.want, err)
		}
		if _, merr := Marshal(c); merr == nil || !strings.Contains(err.Error(), merr.Error()) {
			t.Errorf("%#v: Marshal error %v differs from Validate's %v", tt.value, merr, err)
		}
	}
}

func TestValidateRejectsNonUTF8ID(t *testing.T) {
	c := Default()
	c.Panels = []Panel{{ID: "\xff", Plugin: "p", W: 1, H: 1}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("expected a UTF-8 error, got %v", err)
	}
}

func TestParseRejectsExtraDocuments(t *testing.T) {
	_, err := Parse([]byte(example + "---\ngrid: {row_hieght: 200}\n"))
	if err == nil || !strings.Contains(err.Error(), "YAML documents") {
		t.Fatalf("expected a multiple-documents error, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	panel := func(id string, x, y, w, h int) Panel {
		return Panel{ID: id, Plugin: "p", X: x, Y: y, W: w, H: h}
	}
	plugin := func(name string) Panel {
		return Panel{ID: "a", Plugin: name, W: 1, H: 1}
	}
	disabled := panel("b", 0, 0, 1, 1)
	disabled.Enabled = new(bool)

	type test struct {
		name   string
		panels []Panel
		want   string
	}
	tests := []test{
		{"valid", []Panel{panel("a", 0, 0, 2, 1), panel("b", 2, 0, 2, 2)}, ""},
		{"duplicate id", []Panel{panel("a", 0, 0, 1, 1), panel("a", 1, 0, 1, 1)}, "not unique"},
		{"missing id", []Panel{panel("", 0, 0, 1, 1)}, "id is required"},
		{"plugin path", []Panel{plugin("../../bin/sh")}, "lowercase letters"},
		{"plugin name with hyphen", []Panel{plugin("system-stats")}, ""},
		{"plugin name of 64 characters", []Panel{plugin(strings.Repeat("a", 64))}, ""},
		{"plugin name of 65 characters", []Panel{plugin(strings.Repeat("a", 65))}, "at most 64"},
		{"plugin name with uppercase", []Panel{plugin("Clock")}, "lowercase letters"},
		{"plugin name with underscore", []Panel{plugin("system_stats")}, "lowercase letters"},
		{"plugin name with leading hyphen", []Panel{plugin("-clock")}, "lowercase letters"},
		{"plugin name reserved on Windows", []Panel{plugin("com1")}, "reserved device name"},
		{"plugin name lpt0 reserved on Windows", []Panel{plugin("lpt0")}, "reserved device name"},
		{"plugin name containing a reserved name", []Panel{plugin("console")}, ""},
		{"zero span", []Panel{panel("a", 0, 0, 0, 1)}, "at least 1"},
		{"negative position", []Panel{panel("a", -1, 0, 1, 1)}, "must not be negative"},
		{"past last column", []Panel{panel("a", 3, 0, 2, 1)}, "4 columns"},
		{"x past the grid", []Panel{panel("a", 4, 0, 1, 1)}, "4 columns"},
		{"x+w overflows", []Panel{panel("a", math.MaxInt, 0, 1, 1)}, "4 columns"},
		{"y+h overflows", []Panel{panel("a", 0, math.MaxInt, 1, 1)}, "out of range"},
		{"huge panels overlap", []Panel{panel("a", 0, 0, 1, math.MaxInt32), panel("b", 0, 1_000_000_000, 1, 1)}, `overlaps panel "a"`},
		{"overlap", []Panel{panel("a", 0, 0, 2, 2), panel("b", 1, 1, 1, 1)}, `overlaps panel "a"`},
		{"disabled panels may overlap", []Panel{panel("a", 0, 0, 1, 1), disabled}, ""},
	}
	if strconv.IntSize == 64 {
		maxSpan := int(^uint(0) >> 32) // 2^32-1 where int has 64 bits
		tests = append(tests,
			test{"h of 2^32-1", []Panel{panel("a", 0, 0, 1, maxSpan)}, ""},
			test{"h beyond uint32", []Panel{panel("a", 0, 0, 1, maxSpan+1)}, "at most 4294967295"},
		)
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

func TestSavePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits; Go reports 0666 or 0444")
	}
	path := filepath.Join(t.TempDir(), FileName)
	mode := func() os.FileMode {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.Mode().Perm()
	}
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	if got := mode(); got != 0o600 {
		t.Fatalf("new config has mode %o, want 600", got)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	if got := mode(); got != 0o640 {
		t.Fatalf("replaced config has mode %o, want the existing 640", got)
	}
}
