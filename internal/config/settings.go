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
// extractSettings does not read.
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

// extractSettings resolves each panel's settings from the syntax tree by
// the core schema and removes them from the tree, so the YAML decoder
// never sees them. It returns one entry per panel, nil for none.
func extractSettings(file *ast.File) ([]Settings, error) {
	if len(file.Docs) == 0 || file.Docs[0].Body == nil {
		return nil, nil
	}
	panels, ok := unwrap(value(file.Docs[0].Body, "panels")).(*ast.SequenceNode)
	if !ok {
		return nil, nil
	}
	out := make([]Settings, len(panels.Values))
	for i, entry := range panels.Values {
		m, ok := unwrap(entry).(*ast.MappingNode)
		if !ok {
			continue // a one-key or non-mapping entry; the decoder reports it
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
			out[i] = settings
			m.Values = slices.Delete(m.Values, j, j+1)
			break
		}
	}
	return out, nil
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
	case *ast.StringNode:
		return k.Value
	case *ast.MergeKeyNode:
		return "<<"
	case ast.ScalarNode:
		return k.GetToken().Value
	}
	return ""
}

func line(n ast.Node) int { return n.GetToken().Position.Line }

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
		return scalarText(s), nil
	case *ast.MappingNode, *ast.MappingValueNode:
		out := make(map[string]any)
		for _, pair := range pairs(m) {
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
		return parseInt(s, 10, line)
	case coreOct.MatchString(s):
		return parseInt(s[2:], 8, line)
	case coreHex.MatchString(s):
		return parseInt(s[2:], 16, line)
	case coreFloat.MatchString(s):
		f, err := strconv.ParseFloat(s, 64)
		if math.IsInf(f, 0) {
			return nil, fmt.Errorf("line %d: %s is out of range for a double", line, s)
		}
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
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

func writeMap(b *strings.Builder, m map[string]any, indent int) error {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b.WriteString(strings.Repeat(" ", indent) + scalarString(k) + ":")
		if err := writeValue(b, m[k], indent); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	return nil
}

func writeValue(b *strings.Builder, v any, indent int) error {
	switch v := v.(type) {
	case map[string]any:
		if len(v) == 0 {
			b.WriteString(" {}\n")
			return nil
		}
		b.WriteString("\n")
		return writeMap(b, v, indent+2)
	case []any:
		if len(v) == 0 {
			b.WriteString(" []\n")
			return nil
		}
		b.WriteString("\n")
		for _, e := range v {
			b.WriteString(strings.Repeat(" ", indent+2) + "-")
			if err := writeValue(b, e, indent+2); err != nil {
				return err
			}
		}
		return nil
	}
	text, err := scalar(v)
	if err != nil {
		return err
	}
	b.WriteString(" " + text + "\n")
	return nil
}

// scalar writes a settings scalar as text the core schema reads back as
// the same value and type.
func scalar(v any) (string, error) {
	switch v := v.(type) {
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(v), nil
	case string:
		return scalarString(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint64:
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

func parseInt(s string, base, line int) (any, error) {
	if i, err := strconv.ParseInt(s, base, 64); err == nil {
		return i, nil
	}
	if u, err := strconv.ParseUint(strings.TrimPrefix(s, "+"), base, 64); err == nil {
		return u, nil
	}
	return nil, fmt.Errorf("line %d: %s is out of range for 64 bits", line, s)
}
