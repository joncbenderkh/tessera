// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"reflect"
	"slices"
	"testing"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/joncbenderkh/tessera/internal/config"
)

// The first-party plugins' kinds of schema, as plugins/worldclock,
// plugins/calendar and plugins/processes declare them.
const (
	worldclockSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "clocks": {
      "type": "array",
      "description": "The clocks, in order: 1 to 4.",
      "minItems": 1,
      "maxItems": 4,
      "items": {
        "type": "object",
        "properties": {
          "timezone": {"type": "string", "default": "Local"},
          "label": {"type": "string"}
        },
        "additionalProperties": false
      },
      "default": [{"timezone": "Local"}]
    },
    "utc_offsets": {"type": "boolean", "default": false}
  },
  "additionalProperties": false
}`
	calendarSchema = `{
  "type": "object",
  "properties": {
    "sources": {"type": "array", "items": {"type": "string"}, "minItems": 1},
    "title": {"type": "string", "default": "Next events"},
    "refresh": {"type": "string", "default": "15m"}
  },
  "required": ["sources"]
}`
	greetingSchema = `{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "\"\" hides it."},
    "host_info": {"type": "boolean", "default": true}
  }
}`
	processesSchema = `{
  "type": "object",
  "properties": {
    "sort": {"enum": ["cpu", "memory", "pid", "name"], "default": "cpu"},
    "rows": {"type": "integer", "minimum": 1, "maximum": 50, "default": 10},
    "scale": {"type": "number"},
    "interval": {"type": "string", "default": "2s"},
    "extra": {"oneOf": [{"type": "string"}, {"type": "integer"}]}
  },
  "additionalProperties": false
}`
)

func TestParseForm(t *testing.T) {
	type want struct {
		name string
		kind fieldKind
	}
	kinds := func(fields []*fieldSpec) []want {
		var out []want
		for _, f := range fields {
			out = append(out, want{f.name, f.kind})
		}
		return out
	}
	fields, ok := parseForm(worldclockSchema)
	if !ok || !slices.Equal(kinds(fields), []want{{"clocks", kindObjects}, {"utc_offsets", kindBool}}) {
		t.Fatalf("worldclock: %v %v", ok, kinds(fields))
	}
	if got := kinds(fields[0].props); !slices.Equal(got, []want{{"timezone", kindText}, {"label", kindText}}) {
		t.Errorf("clock items: %v", got)
	}
	if fields[0].maxItems != 4 || !fields[0].hasDef {
		t.Errorf("clocks: maxItems %d, default %v", fields[0].maxItems, fields[0].hasDef)
	}

	fields, ok = parseForm(calendarSchema)
	if !ok || !slices.Equal(kinds(fields), []want{{"sources", kindList}, {"title", kindText}, {"refresh", kindText}}) {
		t.Fatalf("calendar: %v %v", ok, kinds(fields))
	}
	if !fields[0].required || fields[1].required || fields[1].def != "Next events" {
		t.Errorf("calendar: required %v %v, default %v", fields[0].required, fields[1].required, fields[1].def)
	}

	fields, ok = parseForm(processesSchema)
	if !ok || !slices.Equal(kinds(fields), []want{{"sort", kindEnum}, {"rows", kindInteger}, {"scale", kindNumber}, {"interval", kindText}, {"extra", kindRaw}}) {
		t.Fatalf("processes: %v %v", ok, kinds(fields))
	}
	if fields[1].def != int64(10) {
		t.Errorf("rows default %#v, want int64 10", fields[1].def)
	}

	if fields, ok := parseForm(""); !ok || fields != nil {
		t.Errorf("no schema: %v %v", fields, ok)
	}
	for _, s := range []string{`{"type": "array"}`, `{"type": "object"}`, `nope`, `{"type": ["object", "null"], "properties": {"a": {}}}`} {
		if _, ok := parseForm(s); ok {
			t.Errorf("%s: a form", s)
		}
	}
}

// submit presses Save on d, without drawing it.
func submit(d *ConfigDialog) (config.Settings, bool) {
	d.submit = true
	return d.Submitted()
}

// Saving a form left as it was gives back the settings it opened with.
func TestConfigDialogRoundTrip(t *testing.T) {
	tests := []struct {
		schema   string
		settings config.Settings
	}{
		{worldclockSchema, nil},
		{worldclockSchema, config.Settings{
			"clocks":      []any{map[string]any{"timezone": "Asia/Tokyo", "label": "Tokyo"}, map[string]any{}},
			"utc_offsets": false,
		}},
		{calendarSchema, config.Settings{"sources": []any{"https://example.com/a.ics", "~/b.ics"}, "refresh": "1h", "other": int64(3)}},
		{processesSchema, config.Settings{"sort": "pid", "rows": int64(5), "scale": 1.5, "extra": []any{int64(1)}}},
		// Values that do not fit their type are edited in YAML.
		{processesSchema, config.Settings{"rows": "five", "sort": "size", "scale": int64(2)}},
		{`{"type": "array"}`, config.Settings{"a": map[string]any{"b": []any{true}}}},
		// Empty values are values, not the plugin's default.
		{greetingSchema, config.Settings{"text": "", "host_info": false}},
		{calendarSchema, config.Settings{"sources": []any{}}},
		{worldclockSchema, config.Settings{"clocks": []any{map[string]any{"timezone": nil, "label": ""}}}},
	}
	for _, tc := range tests {
		var d ConfigDialog
		d.Open("p", "plugin", tc.schema, tc.settings)
		got, ok := submit(&d)
		if !ok {
			t.Errorf("%v: not submitted: %v", tc.settings, d.general)
			continue
		}
		if !reflect.DeepEqual(got, tc.settings) {
			t.Errorf("opened with %#v, saved %#v", tc.settings, got)
		}
		if !d.Busy() {
			t.Error("not busy after Save")
		}
	}
}

func TestConfigDialogEdits(t *testing.T) {
	var d ConfigDialog
	d.Open("p", "plugin", processesSchema, config.Settings{"rows": int64(5)})
	rows := d.fields[1]
	rows.editor.SetText("5.5")
	if _, ok := submit(&d); ok {
		t.Fatal("5.5 rows submitted")
	}
	if !slices.Equal(rows.problems, []string{"must be a whole number"}) || d.Busy() {
		t.Errorf("problems %q, busy %v", rows.problems, d.Busy())
	}
	rows.editor.SetText(" ")
	d.fields[0].choice = 2 // pid
	d.fields[2].editor.SetText("2e3")
	got, ok := submit(&d)
	if want := (config.Settings{"sort": "pid", "scale": 2000.0}); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("saved %#v, %v; want %#v", got, ok, want)
	}
	if rows.problems != nil {
		t.Errorf("old problems kept: %q", rows.problems)
	}

	// Problems the schema or the plugin finds go where they name.
	d.Open("w", "worldclock", worldclockSchema, config.Settings{"clocks": []any{map[string]any{"timezone": "Mars"}}})
	d.Failed([]config.Problem{
		{Path: []string{"clocks", "0", "timezone"}, Message: "unknown"},
		{Path: []string{"clocks", "0"}, Message: "bad clock"},
		{Path: []string{"clocks"}, Message: "too few"},
		{Path: []string{"colour"}, Message: "not a known setting"},
		{Message: "whole"},
	}, "The plugin rejected these settings: no")
	clocks := d.fields[0]
	if got := clocks.items[0].fields[0].problems; !slices.Equal(got, []string{"unknown"}) {
		t.Errorf("timezone problems %q", got)
	}
	if got := clocks.items[0].problems; !slices.Equal(got, []string{"bad clock"}) {
		t.Errorf("item problems %q", got)
	}
	if !slices.Equal(clocks.problems, []string{"too few"}) {
		t.Errorf("clocks problems %q", clocks.problems)
	}
	want := []string{"settings.colour: not a known setting", "settings: whole", "The plugin rejected these settings: no"}
	if !slices.Equal(d.general, want) {
		t.Errorf("general %q, want %q", d.general, want)
	}

	// Items come and go, and blank ones in a list are dropped.
	d.Open("c", "calendar", calendarSchema, nil)
	sources := d.fields[0]
	sources.items = append(sources.items, newItem(sources.spec, nil), newItem(sources.spec, nil))
	sources.items[0].editor.SetText("/a.ics")
	got, ok = submit(&d)
	if want := (config.Settings{"sources": []any{"/a.ics"}}); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("saved %#v, want %#v", got, want)
	}

	// A form in YAML must hold a mapping.
	d.Open("x", "x", `{"type": "array"}`, nil)
	d.whole.editor.SetText("- 1")
	if _, ok := submit(&d); ok || len(d.whole.problems) != 1 {
		t.Errorf("a list saved as the settings: problems %q", d.whole.problems)
	}
	d.whole.editor.SetText("a: [")
	if _, ok := submit(&d); ok || len(d.whole.problems) != 1 {
		t.Errorf("bad YAML saved: problems %q", d.whole.problems)
	}
}

// The dialog draws every kind of field, and Escape closes it, except
// while Save is under way.
func TestConfigDialogLayout(t *testing.T) {
	var r input.Router
	var d ConfigDialog
	frame := func(events ...event.Event) {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(800, 600), time.Now())
		d.Layout(gtx, testTheme)
		r.Frame(gtx.Ops)
	}
	for _, s := range []string{worldclockSchema, calendarSchema, processesSchema, `{"type": "array"}`, ""} {
		d.Open("p", "plugin", s, config.Settings{"clocks": []any{map[string]any{"timezone": "UTC"}}, "sources": []any{"/a"}})
		frame()
		frame()
	}
	d.Open("p", "plugin", worldclockSchema, nil)
	frame()
	if _, ok := submit(&d); !ok {
		t.Fatal("not submitted")
	}
	frame(pressKey(key.NameEscape)...)
	if !d.IsOpen() {
		t.Error("Escape closed the dialog while saving")
	}
	d.Failed(nil, "no")
	frame(pressKey(key.NameEscape)...)
	if d.IsOpen() {
		t.Error("Escape left the dialog open")
	}
}

// A setting is left out until it is given a value, and again after its
// Default button; a null one is left out from the start.
func TestConfigDialogDefaults(t *testing.T) {
	var r input.Router
	var d ConfigDialog
	frame := func(events ...event.Event) layout.Context {
		for _, e := range events {
			r.Queue(e)
		}
		gtx := context(&r, image.Pt(800, 600), time.Now())
		d.Layout(gtx, testTheme)
		r.Frame(gtx.Ops)
		return gtx
	}
	d.Open("g", "greeting", greetingSchema, config.Settings{"host_info": nil})
	if got, ok := submit(&d); !ok || got != nil {
		t.Errorf("untouched: %#v, %v; want none", got, ok)
	}
	d.Close()
	d.Open("g", "greeting", greetingSchema, nil)
	frame() // text has the focus
	frame(key.EditEvent{Text: "Hi"})
	frame(key.EditEvent{Range: key.Range{Start: 0, End: 2}})
	if got, ok := submit(&d); !ok || !reflect.DeepEqual(got, config.Settings{"text": ""}) {
		t.Errorf("typed and emptied: %#v, %v; want text \"\"", got, ok)
	}

	d.Open("g", "greeting", greetingSchema, config.Settings{"text": "Hi", "host_info": false, "x": int64(1)})
	gtx := frame()
	for _, f := range d.fields {
		if !f.isSet() {
			t.Errorf("%s not set", f.spec.name)
		}
		f.unset(gtx)
	}
	frame()
	if got, ok := submit(&d); !ok || !reflect.DeepEqual(got, config.Settings{"x": int64(1)}) {
		t.Errorf("after Default: %#v, %v; want only the unknown setting", got, ok)
	}
	if on := d.fields[1].on; !on {
		t.Error("toggle not back at its default, on")
	}

	d.Open("w", "worldclock", worldclockSchema, config.Settings{"clocks": []any{map[string]any{}}})
	clocks := d.fields[0]
	clocks.unset(frame())
	if got, ok := submit(&d); !ok || got != nil {
		t.Errorf("list after Default: %#v, %v", got, ok)
	}
}

// A schema the host cannot compile, and so does not check, gives the
// settings in YAML.
func TestConfigDialogBadSchema(t *testing.T) {
	var d ConfigDialog
	settings := config.Settings{"a": "x"}
	d.Open("p", "plugin", `{"type": "object", "properties": {"a": {"type": "string", "pattern": "^(?!x)"}}}`, settings)
	if d.whole == nil {
		t.Fatal("a form for a schema that does not compile")
	}
	if got, ok := submit(&d); !ok || !reflect.DeepEqual(got, settings) {
		t.Errorf("saved %#v, %v", got, ok)
	}
}
