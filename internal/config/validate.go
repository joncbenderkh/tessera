// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
)

// pluginName is the grammar of plugin names, which are also executable
// file names in the plugins directory, so it admits no path separators.
var pluginName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// windowsDevice matches the names Windows reserves for devices, which
// cannot be executable names there.
var windowsDevice = regexp.MustCompile(`^(con|prn|aux|nul|com[0-9]|lpt[0-9])$`)

// Validate checks the config's internal consistency. Plugin settings are
// checked only for numbers JSON cannot carry; checking them against each
// plugin's config schema needs its handshake.
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
	var placed []placement
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
		switch {
		case p.Plugin == "":
			errs = append(errs, fmt.Errorf("%s: plugin is required", name))
		case !pluginName.MatchString(p.Plugin):
			errs = append(errs, fmt.Errorf("%s: plugin %q must be lowercase letters, digits, and hyphens, starting with a letter or digit, at most 64 characters", name, p.Plugin))
		case windowsDevice.MatchString(p.Plugin):
			errs = append(errs, fmt.Errorf("%s: plugin %q is a reserved device name on Windows", name, p.Plugin))
		}
		if path, ok := nonFinite(p.Settings, "settings"); ok {
			errs = append(errs, fmt.Errorf("%s: %s must be a finite number", name, path))
		}
		if p.W < 1 || p.H < 1 {
			errs = append(errs, fmt.Errorf("%s: w and h must be at least 1, got %dx%d", name, p.W, p.H))
			continue
		}
		if p.X < 0 || p.Y < 0 {
			errs = append(errs, fmt.Errorf("%s: x and y must not be negative, got (%d, %d)", name, p.X, p.Y))
			continue
		}
		// Compared without computing X+W or Y+H, which can overflow.
		if c.Grid.Columns >= 1 && (p.X >= c.Grid.Columns || p.W > c.Grid.Columns-p.X) {
			errs = append(errs, fmt.Errorf("%s: x %d with w %d does not fit the grid's %d columns", name, p.X, p.W, c.Grid.Columns))
			continue
		}
		if p.H > math.MaxInt-p.Y {
			errs = append(errs, fmt.Errorf("%s: y %d with h %d is out of range", name, p.Y, p.H))
			continue
		}
		if !p.IsEnabled() {
			continue
		}
		if other, ok := overlapping(placed, p); ok {
			errs = append(errs, fmt.Errorf("%s overlaps %s", name, other))
		}
		placed = append(placed, placement{p, name})
	}
	return errors.Join(errs...)
}

// nonFinite returns the path of the first infinite or NaN number in v,
// which reaches plugins as JSON-model settings and so must be finite.
func nonFinite(v any, path string) (string, bool) {
	switch v := v.(type) {
	case float64:
		return path, math.IsInf(v, 0) || math.IsNaN(v)
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(v)) {
			if p, ok := nonFinite(v[k], path+"."+k); ok {
				return p, true
			}
		}
	case []any:
		for i, e := range v {
			if p, ok := nonFinite(e, fmt.Sprintf("%s[%d]", path, i)); ok {
				return p, true
			}
		}
	}
	return "", false
}

type placement struct {
	Panel
	name string
}

// overlapping returns the name of the first placed panel whose rectangle
// intersects p's.
func overlapping(placed []placement, p Panel) (string, bool) {
	for _, q := range placed {
		if p.X < q.X+q.W && q.X < p.X+p.W && p.Y < q.Y+q.H && q.Y < p.Y+p.H {
			return q.name, true
		}
	}
	return "", false
}
