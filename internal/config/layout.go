// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// ErrNoPanel is the error of a layout change to a panel the config does
// not have, or does not show, as when the file changed meanwhile.
var ErrNoPanel = errors.New("no such panel")

// Compact moves each enabled panel up into the rows left free above it,
// by disabled panels, removed ones, or rows left empty, in the order of
// its row and then its column, so it never passes a panel above it. Its
// column stays. This is the grid the window shows when it has all the
// columns; see docs/config.md.
func Compact(panels []Panel) {
	order := enabled(panels)
	slices.SortStableFunc(order, func(i, j int) int { return byPlace(panels[i], panels[j]) })
	rise(panels, order)
}

func byPlace(a, b Panel) int {
	return cmp.Or(cmp.Compare(a.Y, b.Y), cmp.Compare(a.X, b.X))
}

// enabled is the indices of the enabled panels.
func enabled(panels []Panel) []int {
	var order []int
	for i, p := range panels {
		if p.IsEnabled() {
			order = append(order, i)
		}
	}
	return order
}

// rise places the panels at order, in turn, each just below the ones
// placed before it in its columns.
func rise(panels []Panel, order []int) {
	for n, i := range order {
		p := &panels[i]
		p.Y = 0
		for _, j := range order[:n] {
			q := panels[j]
			if p.X < q.X+q.W && q.X < p.X+p.W {
				p.Y = max(p.Y, q.Y+q.H)
			}
		}
	}
}

// Place moves the enabled panel id to column x and row y, with w and h
// as its size, on the compacted grid, clamped to the grid's columns. The
// panels in its way go below it, except that a panel moved down goes
// below the ones that start on the row it moves to, so that moving down
// onto a panel trades places with it. A panel moved up onto any row of
// a taller one goes above it. The grid is then compacted again.
func (c *Config) Place(id string, x, y, w, h int) error {
	i := slices.IndexFunc(c.Panels, func(p Panel) bool { return p.ID == id && p.IsEnabled() })
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrNoPanel, id)
	}
	Compact(c.Panels)
	p := &c.Panels[i]
	down := y > p.Y
	p.W = min(max(w, 1), c.Grid.Columns)
	p.X = min(max(x, 0), c.Grid.Columns-p.W)
	p.H = min(max(h, 1), maxRows)
	y = max(y, 0)
	for !down {
		// Up to the top of a panel in its columns that spans row y.
		top := slices.IndexFunc(c.Panels, func(q Panel) bool {
			return q.IsEnabled() && q.ID != id && q.Y < y && y < q.Y+q.H && p.X < q.X+q.W && q.X < p.X+p.W
		})
		if top < 0 {
			break
		}
		y = c.Panels[top].Y
	}
	var order []int
	for _, j := range enabled(c.Panels) {
		if j != i {
			order = append(order, j)
		}
	}
	slices.SortStableFunc(order, func(a, b int) int { return byPlace(c.Panels[a], c.Panels[b]) })
	at, _ := slices.BinarySearchFunc(order, y, func(j, y int) int {
		switch q := c.Panels[j]; {
		case q.Y < y, down && q.Y == y:
			return -1
		}
		return 1
	})
	rise(c.Panels, slices.Insert(order, at, i))
	return c.fits()
}

// fits reports a panel pushed past the last row.
func (c *Config) fits() error {
	for _, p := range c.Panels {
		if p.IsEnabled() && p.Y+p.H > maxRows {
			return fmt.Errorf("panel %q would end below row %d", p.ID, maxRows)
		}
	}
	return nil
}

// SetEnabled shows or hides panel id. A panel shown again goes back to
// its place: ahead of the panels at or after its row and column in the
// file, which move below it if they are in its way. The grid is not
// compacted first, so rows the file leaves free keep the panels' order.
func (c *Config) SetEnabled(id string, on bool) error {
	i := slices.IndexFunc(c.Panels, func(p Panel) bool { return p.ID == id })
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrNoPanel, id)
	}
	p := &c.Panels[i]
	if p.IsEnabled() == on {
		return nil
	}
	if !on {
		p.Enabled = &on
		return nil
	}
	p.Enabled = nil
	var order []int
	for _, j := range enabled(c.Panels) {
		if j != i {
			order = append(order, j)
		}
	}
	slices.SortStableFunc(order, func(a, b int) int { return byPlace(c.Panels[a], c.Panels[b]) })
	at, _ := slices.BinarySearchFunc(order, i, func(j, i int) int {
		if byPlace(c.Panels[j], c.Panels[i]) < 0 {
			return -1
		}
		return 1
	})
	rise(c.Panels, slices.Insert(order, at, i))
	return c.fits()
}

// NewID is an id for a new panel of plugin: the plugin's name, or that
// name with the first suffix -2, -3, and so on that no panel has.
func (c *Config) NewID(plugin string) string {
	taken := func(id string) bool {
		return slices.ContainsFunc(c.Panels, func(p Panel) bool { return p.ID == id })
	}
	id := plugin
	for n := 2; taken(id); n++ {
		id = plugin + "-" + strconv.Itoa(n)
	}
	return id
}

// Add adds a one-cell panel id of plugin in the first cell, in reading
// order, that the compacted grid leaves free.
func (c *Config) Add(id, plugin string) error {
	if slices.ContainsFunc(c.Panels, func(p Panel) bool { return p.ID == id }) {
		return fmt.Errorf("panel %q exists already", id)
	}
	Compact(c.Panels)
	taken := func(x, y int) bool {
		return slices.ContainsFunc(c.Panels, func(p Panel) bool {
			return p.IsEnabled() && p.X <= x && x < p.X+p.W && p.Y <= y && y < p.Y+p.H
		})
	}
	p := Panel{ID: id, Plugin: plugin, W: 1, H: 1}
	for !(p.X < c.Grid.Columns && !taken(p.X, p.Y)) {
		p.X++
		if p.X >= c.Grid.Columns {
			p.X, p.Y = 0, p.Y+1
		}
	}
	c.Panels = append(c.Panels, p)
	Compact(c.Panels)
	return c.fits()
}

// Step moves the enabled panel id one column left or right by dx, and
// past the next panel above or below it in its columns by the sign of
// dy, as the arrow keys do. A panel with none in the way stays.
func (c *Config) Step(id string, dx, dy int) error {
	i := slices.IndexFunc(c.Panels, func(p Panel) bool { return p.ID == id && p.IsEnabled() })
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrNoPanel, id)
	}
	Compact(c.Panels)
	p := c.Panels[i]
	y, found := p.Y, false
	for j, q := range c.Panels {
		if j == i || !q.IsEnabled() || q.X >= p.X+p.W || p.X >= q.X+q.W {
			continue
		}
		switch {
		case dy < 0 && q.Y < p.Y && (!found || q.Y > y),
			dy > 0 && q.Y > p.Y && (!found || q.Y < y):
			y, found = q.Y, true
		}
	}
	if dy != 0 && !found {
		return nil
	}
	return c.Place(id, p.X+dx, y, p.W, p.H)
}

// Resize makes the enabled panel id dw columns wider and dh rows taller,
// or narrower and shorter for negative ones, as Shift and the arrow keys
// do. Its corner stays, so it widens only up to the grid's last column.
func (c *Config) Resize(id string, dw, dh int) error {
	i := slices.IndexFunc(c.Panels, func(p Panel) bool { return p.ID == id && p.IsEnabled() })
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrNoPanel, id)
	}
	Compact(c.Panels)
	p := c.Panels[i]
	return c.Place(id, p.X, p.Y, min(max(p.W+dw, 1), c.Grid.Columns-p.X), p.H+dh)
}
