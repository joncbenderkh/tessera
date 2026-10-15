// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads, validates, and saves the user's tessera config: the
// grid, the placement of each panel, and each panel's plugin settings.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"
)

// SchemaVersion is the config schema version this build reads and writes.
const SchemaVersion = 1

// FileName is the config file's name inside the tessera config directory.
const FileName = "config.yaml"

// Config is the whole user config file.
type Config struct {
	Version int     `yaml:"version"`
	Grid    Grid    `yaml:"grid"`
	Panels  []Panel `yaml:"panels"`
}

// Grid sets the shape of the panel grid.
type Grid struct {
	Columns   int `yaml:"columns"`
	RowHeight int `yaml:"row_height"`
}

// Panel places one plugin instance on the grid. X and Y are the zero-based
// column and row of its top-left cell; W and H are its span in cells.
type Panel struct {
	ID       string         `yaml:"id"`
	Plugin   string         `yaml:"plugin"`
	Enabled  *bool          `yaml:"enabled,omitempty"`
	X        int            `yaml:"x"`
	Y        int            `yaml:"y"`
	W        int            `yaml:"w"`
	H        int            `yaml:"h"`
	Settings map[string]any `yaml:"settings,omitempty"`
}

// IsEnabled reports whether the panel is shown; panels are enabled unless
// the config says otherwise.
func (p Panel) IsEnabled() bool {
	return p.Enabled == nil || *p.Enabled
}

// Default is the config written on first run.
func Default() Config {
	return Config{
		Version: SchemaVersion,
		Grid:    Grid{Columns: 4, RowHeight: 180},
		Panels:  []Panel{},
	}
}

// DefaultPath is config.yaml in the tessera directory under the user config
// directory ($XDG_CONFIG_HOME, or ~/.config, on Linux).
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tessera", FileName), nil
}

// Parse decodes and validates a config. Unknown fields, fractional numbers
// in integer fields, and extra YAML documents are errors, so mistakes
// surface instead of being silently ignored or truncated.
func Parse(data []byte) (Config, error) {
	file, err := parser.ParseBytes(data, 0)
	if err != nil {
		return Config{}, err
	}
	if len(file.Docs) > 1 {
		return Config{}, fmt.Errorf("found %d YAML documents, want one", len(file.Docs))
	}
	var settingsErr error
	for _, doc := range file.Docs {
		ast.Walk(settingsCheck{err: &settingsErr}, doc)
	}
	if settingsErr != nil {
		return Config{}, settingsErr
	}
	var c Config
	if err := yaml.UnmarshalWithOptions(data, &c,
		yaml.Strict(), yaml.DisallowUnknownField(), yaml.CustomUnmarshaler(decodeInt),
	); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// decodeInt accepts only YAML integers that fit an int. By default the
// decoder truncates floats, so "x: -0.5" would silently become 0.
func decodeInt(dst *int, data []byte) error {
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return err
	}
	switch n := v.(type) {
	case int64:
		if n < math.MinInt || n > math.MaxInt {
			return fmt.Errorf("%d is out of range", n)
		}
		*dst = int(n)
	case uint64:
		if n > math.MaxInt {
			return fmt.Errorf("%d is out of range", n)
		}
		*dst = int(n)
	default:
		return fmt.Errorf("%s is not an integer", bytes.TrimSpace(data))
	}
	return nil
}

// yamlNumber matches the plain scalars YAML 1.2's core schema reads as
// numbers.
var yamlNumber = regexp.MustCompile(`^([-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?|0o[0-7]+|0x[0-9a-fA-F]+)$`)

// settingsCheck holds settings to YAML 1.2's core schema, so a plugin
// receives exactly the types the file shows. Inside settings it rejects
// tags other than !!str, aliases, and plain scalars the decoder reads
// differently from the core schema: numbers it keeps as strings (beyond
// 64 bits or a double, or 1e5 without a decimal point) and YAML 1.1
// numbers it reads as numbers (1_000, 0b101).
type settingsCheck struct {
	inSettings bool
	err        *error
}

func (v settingsCheck) Visit(n ast.Node) ast.Visitor {
	if *v.err != nil {
		return nil
	}
	fail := func(format string, args ...any) ast.Visitor {
		*v.err = fmt.Errorf("line %d: "+format, append([]any{n.GetToken().Position.Line}, args...)...)
		return nil
	}
	switch n := n.(type) {
	case *ast.MappingValueNode:
		inner := v
		inner.inSettings = v.inSettings || isKey(n.Key, "settings")
		ast.Walk(inner, n.Value)
		return nil
	case *ast.TagNode:
		if !v.inSettings {
			return v
		}
		if tag := n.Start.Value; tag != "!!str" {
			return fail("settings may not use the %s tag; quote the value to pass a string", tag)
		}
		return nil
	case *ast.AliasNode:
		if v.inSettings {
			return fail("settings may not use aliases")
		}
	case *ast.StringNode:
		if v.inSettings && n.Token.Type == token.StringType && yamlNumber.MatchString(n.Value) {
			return fail("%s is out of range or has an exponent without a decimal point (write 1.0e5); quote it to pass a string", n.Value)
		}
	case *ast.IntegerNode, *ast.FloatNode:
		if text := n.GetToken().Value; v.inSettings && !yamlNumber.MatchString(text) {
			return fail("%s is not a YAML 1.2 number; write it in decimal, or quote it to pass a string", text)
		}
	}
	return v
}

// isKey reports whether a mapping key is the plain or quoted string name.
func isKey(key ast.MapKeyNode, name string) bool {
	if k, ok := key.(*ast.MappingKeyNode); ok {
		s, ok := k.Value.(*ast.StringNode)
		return ok && s.Value == name
	}
	s, ok := key.(*ast.StringNode)
	return ok && s.Value == name
}

// Marshal encodes a config as YAML.
func Marshal(c Config) ([]byte, error) {
	return yaml.MarshalWithOptions(c, yaml.Indent(2), yaml.IndentSequence(true))
}

// Load reads the config at path. If the file does not exist, it writes
// Default there and returns it.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		c := Default()
		return c, Save(path, c)
	}
	if err != nil {
		return Config{}, err
	}
	c, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Save validates c and writes it to path atomically: readers see either the
// old file or the complete new one, never a partial write.
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := Marshal(c)
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// writeAtomic keeps the permissions of the file it replaces. A new file is
// private (0600), since plugin settings may hold credentials.
func writeAtomic(path string, data []byte) (err error) {
	perm := fs.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
