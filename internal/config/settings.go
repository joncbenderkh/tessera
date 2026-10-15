// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
)

// Panel settings reach plugins as a google.protobuf.Struct, so their types
// must be exactly what the file shows. The YAML decoder resolves plain
// scalars partly by YAML 1.1 rules (017 is octal, 1e5 a string, << a
// merge), so settings are rebuilt from the syntax tree here by YAML 1.2's
// core schema instead.

var (
	coreNull  = regexp.MustCompile(`^(null|Null|NULL|~|)$`)
	coreTrue  = regexp.MustCompile(`^(true|True|TRUE)$`)
	coreFalse = regexp.MustCompile(`^(false|False|FALSE)$`)
	coreInt   = regexp.MustCompile(`^[-+]?[0-9]+$`)
	coreOct   = regexp.MustCompile(`^0o[0-7]+$`)
	coreHex   = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	coreFloat = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
	coreInf   = regexp.MustCompile(`^[-+]?\.(inf|Inf|INF)$|^\.(nan|NaN|NAN)$`)
)

// rejectSharing rejects aliases anywhere, and merge keys outside
// settings, which would let settings come from a part of the tree that
// extractPanels does not read.
func rejectSharing(file *ast.File) error {
	var err error
	for _, doc := range file.Docs {
		ast.Walk(sharingCheck{err: &err}, doc)
	}
	return err
}

type sharingCheck struct {
	inSettings bool
	err        *error
}

func (v sharingCheck) Visit(n ast.Node) ast.Visitor {
	if *v.err != nil {
		return nil
	}
	switch n := n.(type) {
	case *ast.AliasNode:
		*v.err = fmt.Errorf("line %d: aliases are not supported", line(n))
		return nil
	case *ast.MappingValueNode:
		if _, ok := unwrap(n.Key).(*ast.MergeKeyNode); ok && !v.inSettings {
			*v.err = fmt.Errorf("line %d: merge keys are not supported outside settings", line(n))
			return nil
		}
		inner := v
		inner.inSettings = v.inSettings || keyText(n.Key) == "settings"
		ast.Walk(inner, n.Key)
		ast.Walk(inner, n.Value)
		return nil
	}
	return v
}

// panelFields are the parts of a panel read from the syntax tree rather
// than by the YAML decoder. ID and Plugin are nil unless written as a
// plain or quoted scalar.
type panelFields struct {
	Settings   Settings
	ID, Plugin *string
}

// extractPanels resolves each panel's settings from the syntax tree by
// the core schema and removes them from the tree, so the YAML decoder
// never sees them. It also takes id and plugin as written: the decoder
// would turn "plugin: 017" into "15". It returns one entry per panel.
func extractPanels(file *ast.File) ([]panelFields, error) {
	if len(file.Docs) == 0 || file.Docs[0].Body == nil {
		return nil, nil
	}
	panels, ok := unwrap(value(file.Docs[0].Body, "panels")).(*ast.SequenceNode)
	if !ok {
		return nil, nil
	}
	out := make([]panelFields, len(panels.Values))
	for i, entry := range panels.Values {
		m, ok := unwrap(entry).(*ast.MappingNode)
		if !ok {
			continue // a one-key or non-mapping entry; Validate reports it
		}
		for _, pair := range m.Values {
			switch keyText(pair.Key) {
			case "id":
				out[i].ID = asWritten(pair.Value)
			case "plugin":
				out[i].Plugin = asWritten(pair.Value)
			}
		}
		for j, pair := range m.Values {
			if keyText(pair.Key) != "settings" {
				continue
			}
			v, err := resolve(pair.Value)
			if err != nil {
				return nil, fmt.Errorf("panels[%d].settings: %w", i, err)
			}
			settings, ok := v.(map[string]any)
			if v != nil && !ok {
				return nil, fmt.Errorf("line %d: panels[%d].settings must be a mapping", line(pair), i)
			}
			out[i].Settings = settings
			m.Values = slices.Delete(m.Values, j, j+1)
			break
		}
	}
	return out, nil
}

// asWritten returns the text of an untagged, non-null scalar.
func asWritten(n ast.Node) *string {
	for a, ok := n.(*ast.AnchorNode); ok; a, ok = n.(*ast.AnchorNode) {
		n = a.Value
	}
	switch s := n.(type) {
	case *ast.NullNode, *ast.TagNode:
		return nil
	case ast.ScalarNode:
		text := scalarText(s)
		return &text
	}
	return nil
}

// value returns the value of key in the mapping n, or nil.
func value(n ast.Node, key string) ast.Node {
	for _, pair := range pairs(unwrap(n)) {
		if keyText(pair.Key) == key {
			return pair.Value
		}
	}
	return nil
}

func pairs(n ast.Node) []*ast.MappingValueNode {
	switch n := n.(type) {
	case *ast.MappingNode:
		return n.Values
	case *ast.MappingValueNode:
		return []*ast.MappingValueNode{n}
	}
	return nil
}

// unwrap strips explicit-key markers, anchors, and tags.
func unwrap(n ast.Node) ast.Node {
	for {
		switch m := n.(type) {
		case *ast.MappingKeyNode:
			n = m.Value
		case *ast.AnchorNode:
			n = m.Value
		case *ast.TagNode:
			n = m.Value
		default:
			return n
		}
	}
}

// keyText is a scalar key's text as written, unquoted.
func keyText(key ast.Node) string {
	switch k := unwrap(key).(type) {
	case *ast.MergeKeyNode:
		return "<<"
	case ast.ScalarNode:
		return scalarText(k)
	}
	return ""
}

func line(n ast.Node) int { return n.GetToken().Position.Line }

// checkKeyTag applies the value rule to keys: only !!str is allowed.
func checkKeyTag(key ast.Node) error {
	for n := key; n != nil; {
		switch m := n.(type) {
		case *ast.MappingKeyNode:
			n = m.Value
		case *ast.AnchorNode:
			n = m.Value
		case *ast.TagNode:
			if tag := m.Start.Value; tag != "!!str" {
				return fmt.Errorf("line %d: settings may not use the %s tag", line(m), tag)
			}
			n = m.Value
		default:
			return nil
		}
	}
	return nil
}

// resolve converts a settings node to the Go value the core schema gives
// it. It rejects tags other than !!str and numbers a double cannot carry.
func resolve(n ast.Node) (any, error) {
	for a, ok := n.(*ast.AnchorNode); ok; a, ok = n.(*ast.AnchorNode) {
		n = a.Value
	}
	switch m := n.(type) {
	case *ast.TagNode:
		if tag := m.Start.Value; tag != "!!str" {
			return nil, fmt.Errorf("line %d: settings may not use the %s tag; quote the value to pass a string", line(m), tag)
		}
		s, ok := unwrap(m.Value).(ast.ScalarNode)
		if !ok {
			return nil, fmt.Errorf("line %d: !!str applies only to scalars", line(m))
		}
		if s.GetToken().Type == token.ImplicitNullType {
			return "", nil // "{a: !!str}", or a last key ending in the tag
		}
		return scalarText(s), nil
	case *ast.MappingNode, *ast.MappingValueNode:
		out := make(map[string]any)
		for _, pair := range pairs(m) {
			if err := checkKeyTag(pair.Key); err != nil {
				return nil, err
			}
			if _, ok := unwrap(pair.Key).(ast.ScalarNode); !ok {
				return nil, fmt.Errorf("line %d: settings keys must be scalars", line(pair))
			}
			k := keyText(pair.Key)
			if _, dup := out[k]; dup {
				return nil, fmt.Errorf("line %d: duplicate key %q", line(pair), k)
			}
			v, err := resolve(pair.Value)
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	case *ast.SequenceNode:
		out := make([]any, 0, len(m.Values))
		for _, e := range m.Values {
			v, err := resolve(e)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case *ast.StringNode:
		if m.Token.Type != token.StringType {
			return m.Value, nil // quoted
		}
		return resolvePlain(m.Value, line(m))
	case *ast.LiteralNode:
		return m.Value.Value, nil
	case ast.ScalarNode:
		return resolvePlain(m.GetToken().Value, line(m))
	}
	return nil, fmt.Errorf("line %d: unsupported settings value", line(n))
}

func scalarText(s ast.ScalarNode) string {
	switch s := s.(type) {
	case *ast.StringNode:
		return s.Value
	case *ast.LiteralNode:
		return s.Value.Value
	}
	return s.GetToken().Value
}

// resolvePlain applies the core schema's rules to a plain scalar.
func resolvePlain(s string, line int) (any, error) {
	switch {
	case coreNull.MatchString(s):
		return nil, nil
	case coreTrue.MatchString(s):
		return true, nil
	case coreFalse.MatchString(s):
		return false, nil
	case coreInt.MatchString(s):
		return parseInt(s, s, 10, line)
	case coreOct.MatchString(s):
		return parseInt(s, s[2:], 8, line)
	case coreHex.MatchString(s):
		return parseInt(s, s[2:], 16, line)
	case coreFloat.MatchString(s):
		f, err := strconv.ParseFloat(s, 64)
		if math.IsInf(f, 0) {
			return nil, fmt.Errorf("line %d: %s is out of range for a double", line, s)
		}
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		if mantissa, _, _ := strings.Cut(strings.ToLower(s), "e"); f == 0 && strings.ContainsAny(mantissa, "123456789") {
			return nil, fmt.Errorf("line %d: %s is too small for a double, which would make it 0", line, s)
		}
		return f, nil
	case coreInf.MatchString(s):
		return nil, fmt.Errorf("line %d: %s must be a finite number", line, s)
	}
	return s, nil
}

// plainSafe matches strings that are safe to write unquoted: no YAML
// indicators, no leading or trailing space. resolvePlain must also read
// them back as themselves.
var plainSafe = regexp.MustCompile(`^[A-Za-z_]([A-Za-z0-9_ ./+-]*[A-Za-z0-9_./+-])?$`)

// writeMap writes m at indent; path names m in errors, the way
// checkSettings does.
func writeMap(b *strings.Builder, m map[string]any, indent int, path string) error {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if !utf8.ValidString(k) {
			return fmt.Errorf("%s: key %q is not valid UTF-8", path, k)
		}
		b.WriteString(strings.Repeat(" ", indent) + scalarString(k) + ":")
		if err := writeValue(b, m[k], indent, path+"."+k); err != nil {
			return err
		}
	}
	return nil
}

func writeValue(b *strings.Builder, v any, indent int, path string) error {
	switch v := v.(type) {
	case Settings:
		return writeValue(b, map[string]any(v), indent, path)
	case map[string]any:
		if len(v) == 0 {
			b.WriteString(" {}\n")
			return nil
		}
		b.WriteString("\n")
		return writeMap(b, v, indent+2, path)
	case []any:
		if len(v) == 0 {
			b.WriteString(" []\n")
			return nil
		}
		b.WriteString("\n")
		for i, e := range v {
			b.WriteString(strings.Repeat(" ", indent+2) + "-")
			if err := writeValue(b, e, indent+2, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	}
	text, err := scalar(v)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	b.WriteString(" " + text + "\n")
	return nil
}

// scalar writes a settings scalar as text the core schema reads back as
// the same value and type.
// It is also the check Validate applies, so whatever validates can be
// saved.
func scalar(v any) (string, error) {
	switch v := v.(type) {
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(v), nil
	case string:
		if !utf8.ValidString(v) {
			return "", fmt.Errorf("%q is not valid UTF-8", v)
		}
		return scalarString(v), nil
	case int:
		return exactInt(int64(v))
	case int64:
		return exactInt(v)
	case uint64:
		if v > maxExact {
			return "", errInexact(strconv.FormatUint(v, 10))
		}
		return strconv.FormatUint(v, 10), nil
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return "", fmt.Errorf("%v is not a finite number", v)
		}
		text := strconv.FormatFloat(v, 'g', -1, 64)
		if !strings.ContainsAny(text, ".e") {
			text += ".0" // keep it a float: 100000 would read back as an integer
		}
		return text, nil
	}
	return "", fmt.Errorf("unsupported settings value %T", v)
}

func scalarString(s string) string {
	if v, err := resolvePlain(s, 0); err == nil && v == s && plainSafe.MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}

// maxExact is the largest integer magnitude a double, and so a Struct
// number, carries exactly.
const maxExact = 1 << 53

func errInexact(text string) error {
	return fmt.Errorf("%s is beyond ±2^53, which a plugin receives inexactly; quote it to pass a string", text)
}

func exactInt(v int64) (string, error) {
	if v > maxExact || v < -maxExact {
		return "", errInexact(strconv.FormatInt(v, 10))
	}
	return strconv.FormatInt(v, 10), nil
}

func parseInt(text, digits string, base, line int) (any, error) {
	i, err := strconv.ParseInt(digits, base, 64)
	if err != nil || i > maxExact || i < -maxExact {
		return nil, fmt.Errorf("line %d: %w", line, errInexact(text))
	}
	return i, nil
}
