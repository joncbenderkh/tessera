// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// grid is a config two columns wide holding panels given as
// "id x y w h", with a "-" after the id for a disabled one.
func grid(t *testing.T, panels ...string) Config {
	t.Helper()
	c := Default()
	c.Grid.Columns = 2
	for _, s := range panels {
		var p Panel
		f := strings.Fields(s)
		if f[1] == "-" {
			off := false
			p.Enabled = &off
			f = append(f[:1], f[2:]...)
		}
		if _, err := fmt.Sscan(strings.Join(f, " "), &p.ID, &p.X, &p.Y, &p.W, &p.H); err != nil {
			t.Fatal(err)
		}
		p.Plugin = "clock"
		c.Panels = append(c.Panels, p)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

// places is the panels as "id x y w h", in the config's order, with a
// "-" after a disabled one's id.
func places(c Config) string {
	var out []string
	for _, p := range c.Panels {
		id := p.ID
		if !p.IsEnabled() {
			id += " -"
		}
		out = append(out, fmt.Sprintf("%s %d %d %d %d", id, p.X, p.Y, p.W, p.H))
	}
	return strings.Join(out, ", ")
}

func TestCompactRaisesIntoFreeRows(t *testing.T) {
	c := grid(t, "a 0 3 1 1", "off - 0 0 2 2", "b 1 5 1 2", "c 0 9 2 1")
	Compact(c.Panels)
	if got, want := places(c), "a 0 0 1 1, off - 0 0 2 2, b 1 0 1 2, c 0 2 2 1"; got != want {
		t.Errorf("Compact = %s, want %s", got, want)
	}
}

func TestPlace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		panels     []string
		id         string
		x, y, w, h int
		want       string
	}{
		{"up trades places", []string{"a 0 0 1 1", "b 0 1 1 1"}, "b", 0, 0, 1, 1, "a 0 1 1 1, b 0 0 1 1"},
		{"down trades places", []string{"a 0 0 1 1", "b 0 1 1 1"}, "a", 0, 1, 1, 1, "a 0 1 1 1, b 0 0 1 1"},
		{"down past a tall panel", []string{"a 0 0 1 1", "b 0 1 1 3", "c 0 4 1 1"}, "a", 0, 2, 1, 1, "a 0 3 1 1, b 0 0 1 3, c 0 4 1 1"},
		{"across pushes down", []string{"a 0 0 1 1", "b 1 0 1 1"}, "a", 1, 0, 1, 1, "a 1 0 1 1, b 1 1 1 1"},
		{"into a free column", []string{"a 0 0 1 1", "b 0 1 1 1"}, "b", 1, 0, 1, 1, "a 0 0 1 1, b 1 0 1 1"},
		{"below everything rises", []string{"a 0 0 1 1"}, "a", 0, 7, 1, 1, "a 0 0 1 1"},
		{"wider pushes down", []string{"a 0 0 1 1", "b 1 0 1 1"}, "a", 0, 0, 2, 1, "a 0 0 2 1, b 1 1 1 1"},
		{"clamped to the grid", []string{"a 0 0 1 1"}, "a", 5, 0, 9, 0, "a 0 0 2 1"},
		{"compacts the rest", []string{"a 0 0 1 1", "b 1 4 1 1"}, "a", 0, 0, 1, 2, "a 0 0 1 2, b 1 0 1 1"},
		{"up onto a tall panel's lower rows", []string{"t 0 0 1 3", "a 0 3 1 1"}, "a", 0, 2, 1, 1, "t 0 1 1 3, a 0 0 1 1"},
		{"disabled stay", []string{"off - 0 0 2 2", "a 0 2 1 1"}, "a", 1, 0, 1, 1, "off - 0 0 2 2, a 1 0 1 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := grid(t, tc.panels...)
			if err := c.Place(tc.id, tc.x, tc.y, tc.w, tc.h); err != nil {
				t.Fatal(err)
			}
			if got := places(c); got != tc.want {
				t.Errorf("Place = %s, want %s", got, tc.want)
			}
			if err := c.Validate(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestPlaceRefusesMissingAndDisabledPanels(t *testing.T) {
	c := grid(t, "off - 0 0 1 1")
	for _, id := range []string{"off", "gone"} {
		if err := c.Place(id, 0, 0, 1, 1); !errors.Is(err, ErrNoPanel) {
			t.Errorf("Place(%q) = %v, want ErrNoPanel", id, err)
		}
	}
}

func TestPlaceRefusesPastTheLastRow(t *testing.T) {
	c := grid(t, "a 0 0 1 1", "b 0 1 1 1")
	if err := c.Place("a", 0, 0, 1, maxRows); err == nil {
		t.Errorf("Place = %s, want an error", places(c))
	}
}

func TestStep(t *testing.T) {
	for _, tc := range []struct {
		name   string
		panels []string
		dx, dy int
		want   string
	}{
		{"down", []string{"a 0 0 1 1", "b 0 1 1 2", "c 0 3 1 1"}, 0, 1, "a 0 2 1 1, b 0 0 1 2, c 0 3 1 1"},
		{"up", []string{"b 0 0 1 2", "a 0 2 1 1"}, 0, -1, "b 0 1 1 2, a 0 0 1 1"},
		{"down at the bottom stays", []string{"a 0 0 1 1"}, 0, 1, "a 0 0 1 1"},
		{"up at the top stays", []string{"a 0 0 1 1", "b 1 0 1 1"}, 0, -1, "a 0 0 1 1, b 1 0 1 1"},
		{"right", []string{"a 0 0 1 1"}, 1, 0, "a 1 0 1 1"},
		{"left at the edge stays", []string{"a 0 0 1 1"}, -1, 0, "a 0 0 1 1"},
		{"right goes below a tall panel above it", []string{"b 0 0 1 1", "a 0 1 1 1", "t 1 0 1 3"}, 1, 0, "b 0 0 1 1, a 1 3 1 1, t 1 0 1 3"},
		{"only its columns count", []string{"a 0 0 1 1", "b 1 0 1 1"}, 0, 1, "a 0 0 1 1, b 1 0 1 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := grid(t, tc.panels...)
			if err := c.Step("a", tc.dx, tc.dy); err != nil {
				t.Fatal(err)
			}
			if got := places(c); got != tc.want {
				t.Errorf("Step = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestResize(t *testing.T) {
	for _, tc := range []struct {
		name   string
		panels []string
		dw, dh int
		want   string
	}{
		{"wider pushes down", []string{"a 0 0 1 1", "b 1 0 1 1"}, 1, 0, "a 0 0 2 1, b 1 1 1 1"},
		{"taller", []string{"a 0 0 1 1", "b 0 1 1 1"}, 0, 1, "a 0 0 1 2, b 0 2 1 1"},
		{"wider goes below a tall panel above it", []string{"b 0 0 1 1", "a 0 1 1 1", "t 1 0 1 3"}, 1, 0, "b 0 0 1 1, a 0 3 2 1, t 1 0 1 3"},
		{"no wider than the grid's edge", []string{"a 1 0 1 1"}, 1, 0, "a 1 0 1 1"},
		{"no smaller than a cell", []string{"a 0 0 1 1"}, -1, -1, "a 0 0 1 1"},
		{"from where it is now", []string{"a 0 3 1 1"}, 1, 0, "a 0 0 2 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := grid(t, tc.panels...)
			if err := c.Resize("a", tc.dw, tc.dh); err != nil {
				t.Fatal(err)
			}
			if got := places(c); got != tc.want {
				t.Errorf("Resize = %s, want %s", got, tc.want)
			}
		})
	}
	c := grid(t, "off - 0 0 1 1")
	if err := c.Resize("off", 1, 0); !errors.Is(err, ErrNoPanel) {
		t.Errorf("Resize(off) = %v, want ErrNoPanel", err)
	}
}

func TestSetEnabled(t *testing.T) {
	c := grid(t, "a 0 0 1 1", "b 0 1 1 1")
	if err := c.SetEnabled("a", false); err != nil {
		t.Fatal(err)
	}
	if got, want := places(c), "a - 0 0 1 1, b 0 1 1 1"; got != want {
		t.Errorf("disabled: %s, want %s", got, want)
	}
	// b rose meanwhile, as the window shows it; a goes back above it.
	Compact(c.Panels)
	if err := c.SetEnabled("a", true); err != nil {
		t.Fatal(err)
	}
	if got, want := places(c), "a 0 0 1 1, b 0 1 1 1"; got != want {
		t.Errorf("enabled again: %s, want %s", got, want)
	}
	if c.Panels[0].Enabled != nil {
		t.Errorf("enabled: %v, want the field dropped", *c.Panels[0].Enabled)
	}
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
	// Before a panel listed ahead of it that rose into its row too.
	c = grid(t, "b 0 1 1 1", "a 0 0 1 1")
	if err := c.SetEnabled("a", false); err != nil {
		t.Fatal(err)
	}
	Compact(c.Panels)
	if err := c.SetEnabled("a", true); err != nil {
		t.Fatal(err)
	}
	if got, want := places(c), "b 0 1 1 1, a 0 0 1 1"; got != want {
		t.Errorf("enabled after b: %s, want %s", got, want)
	}
	// Between the panels around it in a file that leaves rows free.
	c = grid(t, "a 0 0 1 1", "b 0 10 1 1", "c 0 20 1 1")
	if err := c.SetEnabled("b", false); err != nil {
		t.Fatal(err)
	}
	if err := c.SetEnabled("b", true); err != nil {
		t.Fatal(err)
	}
	if got, want := places(c), "a 0 0 1 1, b 0 1 1 1, c 0 2 1 1"; got != want {
		t.Errorf("enabled between: %s, want %s", got, want)
	}
	if err := c.SetEnabled("gone", true); !errors.Is(err, ErrNoPanel) {
		t.Errorf("SetEnabled(gone) = %v, want ErrNoPanel", err)
	}
}

func TestAdd(t *testing.T) {
	c := grid(t, "clock 0 0 1 2", "b 1 0 1 1")
	id := c.NewID("clock")
	if id != "clock-2" {
		t.Errorf("NewID = %q, want clock-2", id)
	}
	if err := c.Add(id, "clock"); err != nil {
		t.Fatal(err)
	}
	if got, want := places(c), "clock 0 0 1 2, b 1 0 1 1, clock-2 1 1 1 1"; got != want {
		t.Errorf("Add = %s, want %s", got, want)
	}
	if err := c.Add(id, "clock"); err == nil {
		t.Error("Add of an existing id succeeded")
	}
	if id := c.NewID("notes"); id != "notes" {
		t.Errorf("NewID = %q, want notes", id)
	}
	// A full grid gets a new row.
	if err := c.Add("full", "clock"); err != nil {
		t.Fatal(err)
	}
	if got, want := places(c), "clock 0 0 1 2, b 1 0 1 1, clock-2 1 1 1 1, full 0 2 1 1"; got != want {
		t.Errorf("Add to a full grid = %s, want %s", got, want)
	}
}
