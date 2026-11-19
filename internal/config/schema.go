// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"maps"
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

// noSettings is the schema of a plugin that declares none: it takes no
// settings.
const noSettings = `{"type": "object", "maxProperties": 0}`

// schemaURL names the schema being compiled; nothing is fetched from it.
const schemaURL = "tessera:config-schema.json"

// CompileSchema compiles a plugin's config schema, a JSON document. An
// empty one means the plugin takes no settings. The schema may not
// refer to other documents: none are loaded, from the network or files.
func CompileSchema(src string) (*Schema, error) {
	if strings.TrimSpace(src) == "" {
		src = noSettings
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("config schema: %w", err)
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
	return &Schema{s}, nil
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
	// Message says what is wrong, in English.
	Message string
}

// String is the problem with its place, as in "settings.clocks[0]:
// ...".
func (p Problem) String() string {
	return PathString(p.Path) + ": " + p.Message
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
		v = map[string]any(settings)
	}
	err := s.schema.Validate(v)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Problem{{Message: err.Error()}}
	}
	var out []Problem
	leaves(ve, &out)
	slices.SortStableFunc(out, func(a, b Problem) int { return slices.Compare(a.Path, b.Path) })
	return slices.CompactFunc(out, func(a, b Problem) bool { return a.Message == b.Message && slices.Equal(a.Path, b.Path) })
}

var english = message.NewPrinter(language.English)

// leaves appends the problems e's innermost causes describe. A missing
// or unexpected property is placed at that property.
func leaves(e *jsonschema.ValidationError, out *[]Problem) {
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
			*out = append(*out, Problem{append(slices.Clip(at), name), "required"})
		}
		return
	case *kind.AdditionalProperties:
		for _, name := range k.Properties {
			*out = append(*out, Problem{append(slices.Clip(at), name), "not a known setting"})
		}
		return
	}
	*out = append(*out, Problem{at, e.ErrorKind.LocalizedString(english)})
}

// WithoutNulls is settings less those set to null, as a blank value
// in YAML is: a null setting means the plugin's default, as a missing
// one does.
func (s Settings) WithoutNulls() Settings {
	if !slices.Contains(slices.Collect(maps.Values(s)), nil) {
		return s
	}
	out := Settings{}
	for k, v := range s {
		if v != nil {
			out[k] = v
		}
	}
	return out
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
