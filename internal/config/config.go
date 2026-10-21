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
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/parser"
)

// SchemaVersion is the config schema version this build reads and writes.
const SchemaVersion = 1

// FileName is the config file's name inside the tessera config directory.
const FileName = "config.yaml"

// Config is the whole user config file.
type Config struct {
	Version int     `yaml:"version"`
	Theme   Theme   `yaml:"theme,omitempty"`
	Grid    Grid    `yaml:"grid"`
	Panels  []Panel `yaml:"panels"`
}

// Theme is the window's colour theme. The zero value means the default,
// ThemeDark.
type Theme string

// The themes a config may name.
const (
	ThemeDark  Theme = "dark"
	ThemeLight Theme = "light"
)

// Grid sets the shape of the panel grid.
type Grid struct {
	Columns   int `yaml:"columns"`
	RowHeight int `yaml:"row_height"`
}

// Settings are a panel's plugin settings, resolved by YAML 1.2's core
// schema; see extractPanels.
type Settings map[string]any

// Panel places one plugin instance on the grid. X and Y are the zero-based
// column and row of its top-left cell; W and H are its span in cells.
type Panel struct {
	ID       string   `yaml:"id"`
	Plugin   string   `yaml:"plugin"`
	Enabled  *bool    `yaml:"enabled,omitempty"`
	X        int      `yaml:"x"`
	Y        int      `yaml:"y"`
	W        int      `yaml:"w"`
	H        int      `yaml:"h"`
	Settings Settings `yaml:"settings,omitempty"`
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
	if err := rejectSharing(file); err != nil {
		return Config{}, err
	}
	fields, err := extractPanels(file)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if len(file.Docs) > 0 && file.Docs[0].Body != nil {
		if err := yaml.NodeToValue(file.Docs[0].Body, &c,
			yaml.Strict(), yaml.DisallowUnknownField(), yaml.CustomUnmarshaler(decodeInt),
		); err != nil {
			return Config{}, err
		}
	}
	if len(fields) != len(c.Panels) {
		return Config{}, errors.New("panels: syntax tree and decoded config disagree")
	}
	for i, f := range fields {
		p := &c.Panels[i]
		p.Settings = f.Settings
		if f.ID != nil {
			p.ID = *f.ID
		}
		if f.Plugin != nil {
			p.Plugin = *f.Plugin
		}
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

// Marshal encodes a config as YAML. It writes the file itself rather than
// through the YAML encoder, which quotes by YAML 1.1 rules: every key and
// string is quoted unless the core schema reads it back unchanged.
func Marshal(c Config) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "version: %d\n", c.Version)
	if c.Theme != "" {
		fmt.Fprintf(&b, "theme: %s\n", scalarString(string(c.Theme)))
	}
	fmt.Fprintf(&b, "grid:\n  columns: %d\n  row_height: %d\n", c.Grid.Columns, c.Grid.RowHeight)
	if len(c.Panels) == 0 {
		b.WriteString("panels: []\n")
		return []byte(b.String()), nil
	}
	b.WriteString("panels:\n")
	for _, p := range c.Panels {
		fmt.Fprintf(&b, "  - id: %s\n    plugin: %s\n", scalarString(p.ID), scalarString(p.Plugin))
		if p.Enabled != nil {
			fmt.Fprintf(&b, "    enabled: %t\n", *p.Enabled)
		}
		fmt.Fprintf(&b, "    x: %d\n    y: %d\n    w: %d\n    h: %d\n", p.X, p.Y, p.W, p.H)
		if len(p.Settings) > 0 {
			b.WriteString("    settings:\n")
			if err := writeMap(&b, p.Settings, 6, "settings"); err != nil {
				return nil, fmt.Errorf("panel %q: %w", p.ID, err)
			}
		}
	}
	return []byte(b.String()), nil
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
	return writeAtomic(path, data, nil)
}

// ErrChanged is Update's error when the file changed while it was being
// updated, and was left as it was.
var ErrChanged = errors.New("the file changed while it was being updated")

// Update reads the config at path, applies change to it, and saves it. If
// the file changes meanwhile, by size, modification time, or being
// replaced, Update leaves it alone and returns ErrChanged, so an edit made
// in an editor is not lost. The check runs just before the final rename,
// which narrows the window for losing such an edit but cannot close it.
func Update(path string, change func(*Config)) error {
	before, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	c, err := Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	change(&c)
	if err := c.Validate(); err != nil {
		return err
	}
	if data, err = Marshal(c); err != nil {
		return err
	}
	return writeAtomic(path, data, func() error {
		now, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !os.SameFile(before, now) || now.Size() != before.Size() || !now.ModTime().Equal(before.ModTime()) {
			return ErrChanged
		}
		return nil
	})
}

// writeAtomic keeps the permissions of the file it replaces. A new file is
// private (0600), since plugin settings may hold credentials. A symlink to
// an existing file is followed, so that file is replaced and the link
// kept; a dangling one is replaced. If check is not nil, it runs just
// before the rename, which happens only if it returns nil.
func writeAtomic(path string, data []byte, check func() error) (err error) {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
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
	if check != nil {
		if err = check(); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}
