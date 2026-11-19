// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const clocksSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "clocks": {
      "type": "array",
      "minItems": 1,
      "maxItems": 2,
      "items": {
        "type": "object",
        "properties": {"timezone": {"type": "string"}},
        "required": ["timezone"],
        "additionalProperties": false
      }
    },
    "rows": {"type": "integer", "minimum": 1},
    "sort": {"enum": ["cpu", "pid"]}
  },
  "additionalProperties": false
}`

func TestSchemaCheck(t *testing.T) {
	s, err := CompileSchema(clocksSchema)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		settings Settings
		want     []string // Problem.String()s
	}{
		{"none", nil, nil},
		{"valid", Settings{"clocks": []any{map[string]any{"timezone": "UTC"}}, "rows": int64(3), "sort": "pid"}, nil},
		{"float that is whole", Settings{"rows": 2.0}, nil},
		{"unknown", Settings{"colour": "red"}, []string{"settings.colour: not a known setting"}},
		{"nested missing", Settings{"clocks": []any{map[string]any{}}}, []string{"settings.clocks[0].timezone: required"}},
		{"nested unknown", Settings{"clocks": []any{map[string]any{"timezone": "UTC", "x": true}}}, []string{"settings.clocks[0].x: not a known setting"}},
		{"too few", Settings{"clocks": []any{}}, []string{"settings.clocks: minItems: got 0, want 1"}},
		{"type", Settings{"rows": "3"}, []string{"settings.rows: got string, want integer"}},
		{"minimum", Settings{"rows": int64(0)}, []string{"settings.rows: minimum: got 0, want 1"}},
		{"enum", Settings{"sort": "name"}, []string{"settings.sort: value must be one of 'cpu', 'pid'"}},
		{"two", Settings{"rows": int64(0), "sort": "name"}, []string{"settings.rows: minimum: got 0, want 1", "settings.sort: value must be one of 'cpu', 'pid'"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, p := range s.Check(tc.settings) {
				got = append(got, p.String())
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Check = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSchemaEmpty(t *testing.T) {
	s, err := CompileSchema(" ")
	if err != nil {
		t.Fatal(err)
	}
	if p := s.Check(nil); p != nil {
		t.Errorf("no settings: %v", p)
	}
	if p := s.Check(Settings{"a": int64(1)}); len(p) == 0 {
		t.Error("a panel that takes no settings accepts some")
	}
}

func TestCompileSchemaRejects(t *testing.T) {
	for _, src := range []string{
		`{`,
		`{"type": 3}`,
		`{"$ref": "https://example.com/other.json"}`,
		`{"$ref": "file:///etc/passwd"}`,
	} {
		if _, err := CompileSchema(src); err == nil {
			t.Errorf("CompileSchema(%s) compiled", src)
		}
	}
}

func TestWithoutNulls(t *testing.T) {
	in := Settings{"a": nil, "b": int64(1), "c": []any{nil}}
	if got, want := in.WithoutNulls(), (Settings{"b": int64(1), "c": []any{nil}}); !reflect.DeepEqual(got, want) {
		t.Errorf("WithoutNulls = %v, want %v", got, want)
	}
	if in["a"] != nil || len(in) != 3 {
		t.Errorf("changed its receiver: %v", in)
	}
	if got := Settings(nil).WithoutNulls(); got != nil {
		t.Errorf("nil: %v", got)
	}
}

func TestSettingsError(t *testing.T) {
	err := &SettingsError{[]Problem{{[]string{"a"}, "required"}, {nil, "bad"}}}
	if got := err.Error(); !strings.Contains(got, "settings.a: required; settings: bad") {
		t.Errorf("Error() = %q", got)
	}
}

func TestParseFormatValue(t *testing.T) {
	for _, v := range []any{
		"text", "017", "yes", int64(17), 1.5, true, nil,
		[]any{"a", int64(2)},
		map[string]any{"timezone": "UTC", "label": "Home"},
		[]any{map[string]any{"timezone": "Asia/Tokyo"}, map[string]any{}},
	} {
		text, err := FormatValue(v)
		if err != nil {
			t.Fatalf("FormatValue(%#v): %v", v, err)
		}
		got, err := ParseValue(text)
		if err != nil {
			t.Fatalf("ParseValue(%q): %v", text, err)
		}
		if !reflect.DeepEqual(got, v) {
			t.Errorf("%#v: written %q, read back %#v", v, text, got)
		}
	}
	if v, err := ParseValue("  \n"); v != nil || err != nil {
		t.Errorf("blank: %v, %v", v, err)
	}
	for _, bad := range []string{"a: [", "!!int 3", "a: &x 1\nb: *x", "--- 1\n--- 2"} {
		if _, err := ParseValue(bad); err == nil {
			t.Errorf("ParseValue(%q) passed", bad)
		}
	}
}

func TestSetSettings(t *testing.T) {
	c := Config{Panels: []Panel{{ID: "a", Plugin: "clock", W: 1, H: 1}}}
	if err := c.SetSettings("a", Settings{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	if c.Panels[0].Settings["timezone"] != "UTC" {
		t.Errorf("settings %v", c.Panels[0].Settings)
	}
	if err := c.SetSettings("a", Settings{}); err != nil || c.Panels[0].Settings != nil {
		t.Errorf("empty: %v, %v", err, c.Panels[0].Settings)
	}
	if err := c.SetSettings("b", nil); !errors.Is(err, ErrNoPanel) {
		t.Errorf("unknown panel: %v", err)
	}
	if err := c.SetSettings("a", Settings{"x": "\xff"}); err == nil {
		t.Error("invalid UTF-8 set")
	}
}
