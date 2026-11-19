// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/joncbenderkh/tessera/internal/config"
)

// fieldKind is the control a setting gets in the Configure dialog.
type fieldKind int

const (
	kindText    fieldKind = iota // a string
	kindInteger                  // an integer, typed as text
	kindNumber                   // a number, typed as text
	kindBool                     // true or false
	kindEnum                     // one of a few values
	kindList                     // an array of strings or numbers
	kindObjects                  // an array of objects of simple settings
	kindRaw                      // anything else, written in YAML
)

// fieldSpec is one setting as its schema describes it.
type fieldSpec struct {
	name        string
	title       string // the schema's title, if any
	description string
	kind        fieldKind
	required    bool
	enum        []any        // kindEnum: the values offered
	def         any          // the schema's default, if hasDef
	hasDef      bool         //
	item        *fieldSpec   // kindList: each item
	props       []*fieldSpec // kindObjects: each item's settings
	maxItems    int          // kindList, kindObjects: 0 for no limit
}

// schemaNode is the part of a JSON Schema the form reads.
type schemaNode struct {
	Type        json.RawMessage `json:"type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Enum        []any           `json:"enum"`
	Default     json.RawMessage `json:"default"`
	Properties  json.RawMessage `json:"properties"`
	Required    []string        `json:"required"`
	Items       json.RawMessage `json:"items"`
	MaxItems    *int            `json:"maxItems"`
}

// parseForm reads a config schema into the form's fields, in the order
// the schema lists the properties. It returns no fields, and ok false,
// for a schema whose settings are not an object of named properties:
// the dialog then edits them whole, in YAML.
func parseForm(schema string) (fields []*fieldSpec, ok bool) {
	if strings.TrimSpace(schema) == "" {
		return nil, true // no settings
	}
	var root schemaNode
	if err := json.Unmarshal([]byte(schema), &root); err != nil || len(root.Properties) == 0 {
		return nil, false
	}
	if t, ok := typeName(root.Type); !ok || t != "object" && t != "" {
		return nil, false
	}
	names, nodes, err := orderedProperties(root.Properties)
	if err != nil {
		return nil, false
	}
	for i, name := range names {
		f := specOf(name, nodes[i], slices.Contains(root.Required, name), true)
		fields = append(fields, f)
	}
	return fields, true
}

// orderedProperties reads a properties object, keeping its order.
func orderedProperties(raw json.RawMessage) ([]string, []json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, fmt.Errorf("properties is not an object")
	}
	var names []string
	var nodes []json.RawMessage
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		var node json.RawMessage
		if err := dec.Decode(&node); err != nil {
			return nil, nil, err
		}
		names = append(names, t.(string))
		nodes = append(nodes, node)
	}
	return names, nodes, nil
}

// typeName is a schema's single type, "" if it names none, and ok
// false if it names several.
func typeName(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", true
	}
	var t string
	if json.Unmarshal(raw, &t) != nil {
		return "", false
	}
	return t, true
}

// specOf is the field for the property name with schema raw. nested
// allows arrays; an array's items may not hold arrays themselves.
func specOf(name string, raw json.RawMessage, required, nested bool) *fieldSpec {
	f := &fieldSpec{name: name, required: required, kind: kindRaw}
	var n schemaNode
	if json.Unmarshal(raw, &n) != nil {
		return f
	}
	f.title, f.description = n.Title, n.Description
	if len(n.Default) > 0 {
		if v, err := jsonValue(n.Default); err == nil {
			f.def, f.hasDef = v, true
		}
	}
	if n.MaxItems != nil {
		f.maxItems = max(*n.MaxItems, 0)
	}
	t, ok := typeName(n.Type)
	if !ok {
		return f
	}
	if len(n.Enum) > 0 {
		for _, v := range n.Enum {
			v, err := normalize(v)
			if err != nil || !isScalar(v) || v == nil {
				return f
			}
			f.enum = append(f.enum, v)
		}
		f.kind = kindEnum
		return f
	}
	switch t {
	case "string":
		f.kind = kindText
	case "integer":
		f.kind = kindInteger
	case "number":
		f.kind = kindNumber
	case "boolean":
		f.kind = kindBool
	case "array":
		if !nested || len(n.Items) == 0 {
			return f
		}
		item := specOf("", n.Items, false, false)
		switch item.kind {
		case kindText, kindInteger, kindNumber:
			f.kind, f.item = kindList, item
		case kindRaw:
			var in schemaNode
			if json.Unmarshal(n.Items, &in) != nil || len(in.Properties) == 0 {
				return f
			}
			if t, ok := typeName(in.Type); !ok || t != "object" && t != "" {
				return f
			}
			names, nodes, err := orderedProperties(in.Properties)
			if err != nil {
				return f
			}
			var props []*fieldSpec
			for i, name := range names {
				p := specOf(name, nodes[i], slices.Contains(in.Required, name), false)
				if p.kind == kindRaw {
					return f // an item too complex for a sub-form
				}
				props = append(props, p)
			}
			f.kind, f.props = kindObjects, props
		}
	}
	return f
}

// jsonValue decodes a JSON value with numbers as settings hold them:
// int64 if whole and exact, float64 otherwise.
func jsonValue(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return normalize(v)
}

// normalize turns the numbers in v into int64 or float64, as settings
// hold them.
func normalize(v any) (any, error) {
	switch v := v.(type) {
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i, nil
		}
		return v.Float64()
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
			return int64(v), nil
		}
		return v, nil
	case int:
		return int64(v), nil
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			n, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			n, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	}
	return v, nil
}

func isScalar(v any) bool {
	switch v.(type) {
	case nil, bool, string, int64, float64:
		return true
	}
	return false
}

// sameValue reports whether a and b are the same setting, a whole
// float matching the integer it equals.
func sameValue(a, b any) bool {
	a, _ = normalize(a)
	b, _ = normalize(b)
	return a == b
}

// scalarText is a scalar setting as a text field shows it.
func scalarText(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return fmt.Sprint(v)
}

// valueFits reports whether v can be shown in a field of kind k without
// losing it: a value of another type is edited in YAML instead.
func valueFits(k fieldKind, f *fieldSpec, v any) bool {
	switch k {
	case kindText:
		_, ok := v.(string)
		return ok
	case kindInteger:
		_, ok := v.(int64)
		return ok
	case kindNumber:
		switch v.(type) {
		case int64, float64:
			return true
		}
	case kindBool:
		_, ok := v.(bool)
		return ok
	case kindEnum:
		return slices.ContainsFunc(f.enum, func(e any) bool { return sameValue(e, v) })
	case kindList:
		items, ok := v.([]any)
		return ok && !slices.ContainsFunc(items, func(e any) bool { return !valueFits(f.item.kind, f.item, e) })
	case kindObjects:
		items, ok := v.([]any)
		if !ok {
			return false
		}
		for _, e := range items {
			m, ok := e.(map[string]any)
			if !ok {
				return false
			}
			for k, pv := range m {
				i := slices.IndexFunc(f.props, func(p *fieldSpec) bool { return p.name == k })
				if i < 0 || !valueFits(f.props[i].kind, f.props[i], pv) {
					return false
				}
			}
		}
		return true
	case kindRaw:
		return true
	}
	return false
}

// parseScalar reads a text field of kind k: the value, whether it is
// set (blank is not), and what is wrong with it.
func parseScalar(k fieldKind, text string) (any, bool, string) {
	if k != kindText {
		text = strings.TrimSpace(text)
	}
	if text == "" {
		return nil, false, ""
	}
	switch k {
	case kindInteger:
		i, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, true, "must be a whole number"
		}
		return i, true, ""
	case kindNumber:
		if i, err := strconv.ParseInt(text, 10, 64); err == nil {
			return i, true, ""
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, true, "must be a number"
		}
		return f, true, ""
	}
	return text, true, ""
}

// extraSettings is the settings no field shows, which saving keeps.
func extraSettings(fields []*fieldSpec, settings config.Settings) map[string]any {
	extra := map[string]any{}
	for k, v := range settings {
		if !slices.ContainsFunc(fields, func(f *fieldSpec) bool { return f.name == k }) {
			extra[k] = v
		}
	}
	return extra
}

// withExtra is settings with extra added, nil if it holds none.
func withExtra(settings config.Settings, extra map[string]any) config.Settings {
	out := config.Settings{}
	maps.Copy(out, extra)
	maps.Copy(out, settings)
	if len(out) == 0 {
		return nil
	}
	return out
}
