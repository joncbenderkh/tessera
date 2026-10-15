// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
)

// Validate checks the config's internal consistency. Plugin settings are
// not checked here: that needs each plugin's config schema from its
// handshake.
func (c Config) Validate() error {
	var errs []error
	if c.Version != SchemaVersion {
		errs = append(errs, fmt.Errorf("version %d is not supported (want %d)", c.Version, SchemaVersion))
	}
	if c.Grid.Columns < 1 {
		errs = append(errs, fmt.Errorf("grid.columns must be at least 1, got %d", c.Grid.Columns))
	}
	if c.Grid.RowHeight < 1 {
		errs = append(errs, fmt.Errorf("grid.row_height must be at least 1, got %d", c.Grid.RowHeight))
	}

	seen := make(map[string]bool, len(c.Panels))
	occupied := make(map[[2]int]string)
	for i, p := range c.Panels {
		name := fmt.Sprintf("panels[%d]", i)
		if p.ID != "" {
			name = fmt.Sprintf("panel %q", p.ID)
		}
		switch {
		case p.ID == "":
			errs = append(errs, fmt.Errorf("%s: id is required", name))
		case seen[p.ID]:
			errs = append(errs, fmt.Errorf("%s: id is not unique", name))
		}
		seen[p.ID] = true
		if p.Plugin == "" {
			errs = append(errs, fmt.Errorf("%s: plugin is required", name))
		}
		if p.W < 1 || p.H < 1 {
			errs = append(errs, fmt.Errorf("%s: w and h must be at least 1, got %dx%d", name, p.W, p.H))
			continue
		}
		if p.X < 0 || p.Y < 0 {
			errs = append(errs, fmt.Errorf("%s: x and y must not be negative, got (%d, %d)", name, p.X, p.Y))
			continue
		}
		if c.Grid.Columns >= 1 && p.X+p.W > c.Grid.Columns {
			errs = append(errs, fmt.Errorf("%s: spans columns %d-%d, but the grid has %d", name, p.X, p.X+p.W-1, c.Grid.Columns))
			continue
		}
		if !p.IsEnabled() {
			continue
		}
		if other, ok := claim(occupied, p, name); !ok {
			errs = append(errs, fmt.Errorf("%s overlaps %s", name, other))
		}
	}
	return errors.Join(errs...)
}

// claim marks the cells p covers as taken by name. If a cell is already
// taken, it stops and returns the name of the panel holding it.
func claim(occupied map[[2]int]string, p Panel, name string) (string, bool) {
	for y := p.Y; y < p.Y+p.H; y++ {
		for x := p.X; x < p.X+p.W; x++ {
			if other, ok := occupied[[2]int{x, y}]; ok {
				return other, false
			}
			occupied[[2]int{x, y}] = name
		}
	}
	return "", true
}
