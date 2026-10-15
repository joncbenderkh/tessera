// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads, validates, and saves the user's tessera config: the
// grid, the placement of each panel, and each panel's plugin settings.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
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

// Parse decodes and validates a config. Unknown fields are errors, so typos
// surface instead of being silently ignored.
func Parse(data []byte) (Config, error) {
	var c Config
	if err := yaml.UnmarshalWithOptions(data, &c, yaml.Strict(), yaml.DisallowUnknownField()); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
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

func writeAtomic(path string, data []byte) (err error) {
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
	if err = tmp.Chmod(0o644); err != nil {
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
