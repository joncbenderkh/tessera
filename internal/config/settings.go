// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

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
// decodeSettings does not read.
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

// decodeSettings replaces each panel's decoded settings with ones built
// from the syntax tree by the core schema.
func decodeSettings(file *ast.File, c *Config) error {
	if len(file.Docs) == 0 || file.Docs[0].Body == nil {
		return nil
	}
	panels, ok := unwrap(value(file.Docs[0].Body, "panels")).(*ast.SequenceNode)
	if !ok {
		return nil
	}
	if len(panels.Values) != len(c.Panels) {
		return errors.New("panels: syntax tree and decoded config disagree")
	}
	for i, entry := range panels.Values {
		node := value(entry, "settings")
		if node == nil {
			continue
		}
		v, err := resolve(node)
		if err != nil {
			return fmt.Errorf("panels[%d].settings: %w", i, err)
		}
		settings, _ := v.(map[string]any)
		c.Panels[i].Settings = settings
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

// quoteLookalikes returns a copy of v in which every string the core
// schema would not read back as that string, such as "1e5" or "08", and
// every string with a control character, is marked for quoting. The
// encoder only quotes what it resolves itself, and drops plain tabs.
func quoteLookalikes(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = quoteLookalikes(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = quoteLookalikes(e)
		}
		return out
	case string:
		if r, err := resolvePlain(v, 0); err != nil || r != v || strings.ContainsFunc(v, unicode.IsControl) {
			return quoted(v)
		}
	}
	return v
}

// quoted is a string the encoder writes double-quoted.
type quoted string

func (q quoted) MarshalYAML() ([]byte, error) {
	return []byte(strconv.Quote(string(q))), nil
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
