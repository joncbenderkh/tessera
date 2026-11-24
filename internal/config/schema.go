// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Schema is a plugin's config schema, compiled: JSON Schema draft
// 2020-12, which checks a panel's settings.
type Schema struct {
	schema *jsonschema.Schema
}

// ErrNoSchema is CompileSchema's answer for an empty schema: the plugin
// declares none, so the host checks nothing and the plugin checks its
// own settings.
var ErrNoSchema = errors.New("the plugin declares no config schema")

// schemaURL names the schema being compiled; nothing is fetched from it.
const schemaURL = "tessera:config-schema.json"

// CompileSchema compiles a plugin's config schema, a JSON document. An
// empty one is ErrNoSchema: nothing to check by. The schema may not
// refer to other documents: none are loaded, from the network or files.
func CompileSchema(src string) (*Schema, error) {
	if strings.TrimSpace(src) == "" {
		return nil, ErrNoSchema
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("config schema: %w", err)
	}
	var f formats
	f.strip(doc, false)
	if oldDraft(doc) && (f.under || f.any && f.refUnder) {
		// There format is asserted, so a branch of oneOf, not, or if
		// without it may match what it did not: the host would refuse
		// settings the schema takes.
		return nil, fmt.Errorf("config schema: format under not, oneOf, if, then, or else cannot be ignored in a schema before draft 2019-09")
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(noLoader{})
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("config schema: %w", err)
	}
	s, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("config schema: %w", err)
	}
	// Some schemas compile but cannot check anything, as one whose
	// $ref refers to itself: checking reports a cycle, not a mismatch.
	if err := s.Validate(map[string]any{}); err != nil {
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) || refCycle(ve) {
			return nil, fmt.Errorf("config schema: %w", err)
		}
	}
	return &Schema{s}, nil
}

// formats is what strip found removing the format keywords from a
// schema document, as format is not checked: schemas of drafts before
// 2019-09 assert it whatever the compiler is told, and its failures
// inside anyOf would fail the whole.
type formats struct {
	any      bool // one was removed
	under    bool // one under not, oneOf, if, then, or else
	refUnder bool // a $ref is under one of those, which may reach one
}

// strip removes the format keywords from v, a schema or part of one,
// under one of not, oneOf, if, then, or else if under is set. A
// keyword's value is a string; a property named "format" has a schema,
// and the values of enum, const, default, and examples are data, so
// those stay. The keys of properties and the like are names, not
// keywords: each value there is a schema.
func (f *formats) strip(v any, under bool) {
	switch v := v.(type) {
	case map[string]any:
		if _, ok := v["format"].(string); ok {
			delete(v, "format")
			f.any = true
			f.under = f.under || under
		}
		if _, ok := v["$ref"]; ok && under {
			f.refUnder = true
		}
		for k, x := range v {
			switch k {
			case "enum", "const", "default", "examples":
				continue
			case "not", "oneOf", "if", "then", "else":
				f.strip(x, true)
				continue
			case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies":
				if named, ok := x.(map[string]any); ok {
					for _, schema := range named {
						f.strip(schema, under)
					}
					continue
				}
			}
			f.strip(x, under)
		}
	case []any:
		for _, x := range v {
			f.strip(x, under)
		}
	}
}

// oldDraft reports whether schema document doc declares a draft before
// 2019-09, which asserts format.
func oldDraft(doc any) bool {
	m, _ := doc.(map[string]any)
	draft, _ := m["$schema"].(string)
	return olderDraft.MatchString(draft)
}

var olderDraft = regexp.MustCompile(`json-schema\.org/draft-0[3-7]/`)

// refCycle reports whether e, or a cause of it, is a reference cycle.
func refCycle(e *jsonschema.ValidationError) bool {
	if _, ok := e.ErrorKind.(*kind.RefCycle); ok {
		return true
	}
	return slices.ContainsFunc(e.Causes, refCycle)
}

type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("%s: config schemas may not refer to other documents", url)
}

// Problem is one way settings fail a schema.
type Problem struct {
	// Path is where in the settings: object keys, and array indexes in
	// decimal. Empty means the settings as a whole.
	Path []string
	// Index marks the steps of Path that are array indexes, to tell
	// them from object keys made of digits; nil for none.
	Index []bool
	// Message says what is wrong, in English.
	Message string
}

// String is the problem with its place, as in "settings.clocks[0]:
// ...".
func (p Problem) String() string {
	if p.Index == nil {
		return PathString(p.Path) + ": " + p.Message
	}
	var b strings.Builder
	b.WriteString("settings")
	for i, step := range p.Path {
		switch {
		case i < len(p.Index) && p.Index[i]:
			b.WriteString("[" + step + "]")
		case step != "" && strings.IndexFunc(step, func(r rune) bool { return !isWordRune(r) }) < 0:
			b.WriteString("." + step)
		default:
			b.WriteString("[" + strconv.Quote(step) + "]")
		}
	}
	return b.String() + ": " + p.Message
}

func isWordRune(r rune) bool {
	return r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

// comparePaths orders paths step by step, indexes by number.
func comparePaths(a, b []string) int {
	for i := range min(len(a), len(b)) {
		x, errX := strconv.Atoi(a[i])
		y, errY := strconv.Atoi(b[i])
		c := strings.Compare(a[i], b[i])
		if errX == nil && errY == nil {
			c = cmp.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

// indexes marks which steps of path are array indexes in v.
func indexes(v any, path []string) []bool {
	out := make([]bool, len(path))
	for i, step := range path {
		switch x := v.(type) {
		case []any:
			out[i] = true
			if n, err := strconv.Atoi(step); err == nil && n >= 0 && n < len(x) {
				v = x[n]
				continue
			}
		case map[string]any:
			v = x[step]
			continue
		}
		v = nil
	}
	return out
}

// PathString names a place in the settings the way errors do:
// settings.key[index].key.
func PathString(path []string) string {
	var b strings.Builder
	b.WriteString("settings")
	for _, p := range path {
		if _, err := strconv.Atoi(p); err == nil {
			b.WriteString("[" + p + "]")
		} else {
			b.WriteString("." + p)
		}
	}
	return b.String()
}

// Check reports the ways settings fail s, in a stable order; none if
// they pass. No settings are an empty object.
func (s *Schema) Check(settings Settings) []Problem {
	var v any = map[string]any{}
	if settings != nil {
		// Plain JSON-model values, as the validator takes, nested
		// Settings included; WithoutNulls copies them so.
		v = plainSettings(settings)
	}
	err := s.schema.Validate(v)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Problem{{Message: err.Error()}}
	}
	if refCycle(ve) {
		// A $ref cycle these settings reach, below what compiling
		// checks: the schema cannot check them, so it checks nothing.
		return nil
	}
	var out []Problem
	leaves(ve, &out)
	for i := range out {
		out[i].Index = indexes(v, out[i].Path)
	}
	if len(out) == 0 {
		return nil
	}
	slices.SortStableFunc(out, func(a, b Problem) int { return comparePaths(a.Path, b.Path) })
	return slices.CompactFunc(out, func(a, b Problem) bool { return a.Message == b.Message && slices.Equal(a.Path, b.Path) })
}

var english = message.NewPrinter(language.English)

// leaves appends the problems e's innermost causes describe. A missing
// or unexpected property is placed at that property.
func leaves(e *jsonschema.ValidationError, out *[]Problem) {
	switch k := e.ErrorKind.(type) {
	case *kind.PropertyNames:
		// The name is wrong, whatever its schema's causes say.
		*out = append(*out, Problem{Path: append(slices.Clone(e.InstanceLocation), k.Property), Message: "not an allowed setting name"})
		return
	case *kind.AnyOf, *kind.OneOf, *kind.Contains, *kind.MinContains, *kind.MaxContains:
		// One of several would do, as one branch of anyOf or one item
		// for contains: their causes are not all required.
		*out = append(*out, Problem{Path: slices.Clone(e.InstanceLocation), Message: e.ErrorKind.LocalizedString(english)})
		return
	}
	if len(e.Causes) > 0 {
		for _, c := range e.Causes {
			leaves(c, out)
		}
		return
	}
	at := slices.Clone(e.InstanceLocation)
	switch k := e.ErrorKind.(type) {
	case *kind.Required:
		for _, name := range k.Missing {
			*out = append(*out, Problem{Path: append(slices.Clip(at), name), Message: "required"})
		}
		return
	case *kind.AdditionalProperties:
		for _, name := range k.Properties {
			*out = append(*out, Problem{Path: append(slices.Clip(at), name), Message: "not a known setting"})
		}
		return
	case *kind.FalseSchema:
		// unevaluatedProperties: false fails each key it does not
		// know with a false schema, placed at the key. The keyword is
		// the URL's last step; a setting of that name has "properties"
		// before it.
		steps := strings.Split(e.SchemaURL, "/")
		if n := len(steps); n >= 2 && (steps[n-1] == "unevaluatedProperties" || steps[n-1] == "additionalProperties") &&
			!slices.Contains([]string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"}, steps[n-2]) {
			*out = append(*out, Problem{Path: at, Message: "not a known setting"})
			return
		}
	}
	*out = append(*out, Problem{Path: at, Message: e.ErrorKind.LocalizedString(english)})
}

// WithoutNulls is settings less the members set to null, at any depth,
// as a blank value in YAML is: a null setting means the plugin's
// default, as a missing one does. Null list items stay, as their place
// in the list counts.
func (s Settings) WithoutNulls() Settings {
	if s == nil {
		return nil
	}
	return Settings(withoutNulls(map[string]any(s)).(map[string]any))
}

// plainSettings is settings as plain maps and lists, without changing
// their values: Settings nested in them become map[string]any.
func plainSettings(v any) any {
	switch v := v.(type) {
	case Settings:
		return plainSettings(map[string]any(v))
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = plainSettings(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = plainSettings(x)
		}
		return out
	}
	return v
}

func withoutNulls(v any) any {
	switch v := v.(type) {
	case Settings:
		return withoutNulls(map[string]any(v))
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			if x != nil {
				out[k] = withoutNulls(x)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = withoutNulls(x)
		}
		return out
	}
	return v
}

// SettingsError is settings that fail their plugin's schema.
type SettingsError struct {
	Problems []Problem
}

func (e *SettingsError) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.String()
	}
	return "the settings do not match the plugin's schema: " + strings.Join(lines, "; ")
}
