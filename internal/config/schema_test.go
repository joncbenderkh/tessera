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

// No schema is nothing to check by.
func TestSchemaEmpty(t *testing.T) {
	if _, err := CompileSchema(" "); !errors.Is(err, ErrNoSchema) {
		t.Errorf("CompileSchema(blank) = %v, want ErrNoSchema", err)
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
	err := &SettingsError{[]Problem{{Path: []string{"a"}, Message: "required"}, {Message: "bad"}}}
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

// A schema that compiles but cannot check, as one whose $ref refers to
// itself, does not compile here: it checks nothing.
func TestCompileSchemaThatCannotCheck(t *testing.T) {
	if _, err := CompileSchema(`{"$ref": "tessera:config-schema.json"}`); err == nil {
		t.Error("a self-referring schema compiled")
	}
}

// One of several branches failing is one problem, at the place.
func TestCheckAnyOfIsOneProblem(t *testing.T) {
	s, err := CompileSchema(`{"anyOf": [{"required": ["x"]}, {"required": ["y"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Check(Settings{}); len(got) != 1 || len(got[0].Path) != 0 {
		t.Errorf("problems %v, want one for the settings as a whole", got)
	}
}

// Indexes sort by number, and keys made of digits read as keys.
func TestProblemPaths(t *testing.T) {
	s, err := CompileSchema(`{"type": "object", "properties": {
		"list": {"type": "array", "items": {"type": "string"}},
		"map": {"type": "object", "additionalProperties": {"type": "string"}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	list := make([]any, 11)
	for i := range list {
		list[i] = "ok"
	}
	list[2], list[10] = 1, 1
	got := s.Check(Settings{"list": list, "map": map[string]any{"0": 1}})
	var lines []string
	for _, p := range got {
		lines = append(lines, p.String())
	}
	want := []string{"settings.list[2]: got number, want string", "settings.list[10]: got number, want string", "settings.map.0: got number, want string"}
	if !slices.Equal(lines, want) {
		t.Errorf("problems %q, want %q", lines, want)
	}
}

func TestParseValueCommentIsBlank(t *testing.T) {
	if v, err := ParseValue("# only a comment"); err != nil || v != nil {
		t.Errorf("ParseValue(comment) = %v, %v; want nil, nil", v, err)
	}
}

// A $ref cycle under a property, which compiling does not reach, still
// checks nothing.
func TestCheckDeepRefCycleChecksNothing(t *testing.T) {
	s, err := CompileSchema(`{"type": "object", "properties": {"a": {"$ref": "#/properties/a"}}}`)
	if err != nil {
		t.Skip("compiled as unusable:", err)
	}
	for _, v := range []any{int64(1), "x", map[string]any{}} {
		if p := s.Check(Settings{"a": v}); p != nil {
			t.Errorf("Check(a: %v) = %v, want nothing", v, p)
		}
	}
}

// An unknown key under unevaluatedProperties: false is "not a known
// setting", at the key.
func TestUnevaluatedPropertyNotKnown(t *testing.T) {
	s, err := CompileSchema(`{"type": "object", "properties": {"x": {}}, "unevaluatedProperties": false}`)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Check(Settings{"zz": int64(1)})
	if len(got) != 1 || got[0].String() != "settings.zz: not a known setting" {
		t.Errorf("problems %v", got)
	}
}

// A dialog field reads "<<" as settings in the file do: a plain key,
// as YAML 1.2 has no merge keys.
func TestParseValueMergeKey(t *testing.T) {
	v, err := ParseValue("<<: {a: 1}\nb: 2")
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := v.(map[string]any); !ok || m["b"] != int64(2) || m["<<"] == nil {
		t.Errorf("ParseValue = %#v", v)
	}
	if _, err := ParseValue("a: &x 1\nb: *x"); err == nil {
		t.Error("an alias was taken")
	}
}

// A null member at any depth is left out; a null list item stays.
func TestWithoutNullsAtAnyDepth(t *testing.T) {
	s := Settings{"a": nil, "clocks": []any{map[string]any{"timezone": "UTC", "label": nil}, nil}}
	got := s.WithoutNulls()
	want := Settings{"clocks": []any{map[string]any{"timezone": "UTC"}, nil}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WithoutNulls = %#v, want %#v", got, want)
	}
}

// format is not checked, even by a draft-07 schema, which asserts it.
func TestFormatNotChecked(t *testing.T) {
	s, err := CompileSchema(`{"$schema": "http://json-schema.org/draft-07/schema#", "properties": {"a": {"format": "email"}, "r": {"format": "regex"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if p := s.Check(Settings{"a": "nope", "r": "(?=x)"}); p != nil {
		t.Errorf("problems %v, want none", p)
	}
}

// Directives and document markers make no value of their own.
func TestParseValueDirectives(t *testing.T) {
	if v, err := ParseValue("%YAML 1.2\n---\n1"); err != nil || v != int64(1) {
		t.Errorf("with a directive: %v, %v", v, err)
	}
	if v, err := ParseValue("..."); err != nil || v != nil {
		t.Errorf("a marker alone: %v, %v", v, err)
	}
}

// format is not checked inside anyOf either: a branch failing only on
// it passes.
func TestFormatNotCheckedInAnyOf(t *testing.T) {
	s, err := CompileSchema(`{"$schema": "http://json-schema.org/draft-07/schema#", "properties": {
		"a": {"anyOf": [{"type": "string", "format": "email"}, {"type": "integer"}]},
		"format": {"type": "string", "enum": [{"format": "x"}]}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if p := s.Check(Settings{"a": "hello"}); p != nil {
		t.Errorf("problems %v, want none", p)
	}
	// A property named format, and data shaped like the keyword, stay.
	if p := s.Check(Settings{"format": int64(1)}); p == nil {
		t.Error("the property named format is no longer checked")
	}
}

// contains is one problem at the list, propertyNames one at the key,
// and a declared setting named like a keyword is not "unknown".
func TestProblemPlacementMore(t *testing.T) {
	cases := []struct {
		schema   string
		settings Settings
		want     []string
	}{
		{`{"properties": {"l": {"type": "array", "contains": {"const": "x"}}}}`, Settings{"l": []any{"a", "b"}}, []string{"settings.l: "}},
		{`{"propertyNames": {"maxLength": 2}}`, Settings{"abc": int64(1)}, []string{"settings.abc: not an allowed setting name"}},
		{`{"properties": {"unevaluatedProperties": false}}`, Settings{"unevaluatedProperties": int64(1)}, []string{"settings.unevaluatedProperties: false schema"}},
		{`{"properties": {"n": {"type": "object", "properties": {"a": {"type": "string"}}}}}`, Settings{"n": Settings{"a": "x"}}, nil},
	}
	for _, c := range cases {
		s, err := CompileSchema(c.schema)
		if err != nil {
			t.Fatal(err)
		}
		got := s.Check(c.settings)
		if len(got) != len(c.want) {
			t.Errorf("%s: problems %v, want %q", c.schema, got, c.want)
			continue
		}
		for i := range got {
			if !strings.HasPrefix(got[i].String(), c.want[i]) {
				t.Errorf("%s: problem %q, want %q…", c.schema, got[i], c.want[i])
			}
		}
	}
}

// A setting named like a data keyword has a schema whose format is not
// checked either.
func TestFormatNotCheckedUnderKeywordNamedSetting(t *testing.T) {
	s, err := CompileSchema(`{"$schema": "http://json-schema.org/draft-07/schema#", "properties": {
		"enum": {"anyOf": [{"type": "string", "format": "email"}, {"type": "integer"}]}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if p := s.Check(Settings{"enum": "x"}); p != nil {
		t.Errorf("problems %v, want none", p)
	}
}

// In a draft before 2019-09, format under not, oneOf, or if cannot be
// stripped without refusing settings the schema takes, so such a
// schema checks nothing; elsewhere, and in later drafts, it is stripped.
func TestOldDraftFormatUnderOneOf(t *testing.T) {
	const draft07 = `"$schema": "http://json-schema.org/draft-07/schema#", `
	for _, body := range []string{
		`"properties": {"host": {"type": "string", "oneOf": [{"format": "ipv4"}, {"format": "ipv6"}]}}`,
		`"properties": {"a": {"not": {"format": "email"}}}`,
		`"properties": {"a": {"if": {"properties": {"b": {"format": "email"}}}, "then": {"required": ["c"]}}}`,
		`"definitions": {"email": {"type": "string", "format": "email"}}, "properties": {"a": {"not": {"$ref": "#/definitions/email"}}}`,
	} {
		if _, err := CompileSchema("{" + draft07 + body + "}"); err == nil {
			t.Errorf("draft-07 {%s} compiled", body)
		}
		if _, err := CompileSchema("{" + body + "}"); err != nil {
			t.Errorf("2020-12 {%s}: %v", body, err)
		}
	}
	for _, body := range []string{
		`"properties": {"a": {"anyOf": [{"format": "email"}, {"type": "integer"}]}}`,
		`"definitions": {"n": {"type": "integer"}}, "properties": {"a": {"not": {"$ref": "#/definitions/n"}}}`,
	} {
		if _, err := CompileSchema("{" + draft07 + body + "}"); err != nil {
			t.Errorf("draft-07 {%s}: %v", body, err)
		}
	}
}
