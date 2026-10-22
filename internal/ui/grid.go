// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"cmp"
	"image"
	"slices"

	"gioui.org/gesture"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// Cell is a panel's place on the grid, in cells.
type Cell struct {
	ID         string
	X, Y, W, H int
}

func (c Cell) overlaps(d Cell) bool {
	return c.X < d.X+d.W && d.X < c.X+c.W && c.Y < d.Y+d.H && d.Y < c.Y+c.H
}

func fits(cells []Cell, c Cell) bool {
	for _, d := range cells {
		if c.overlaps(d) {
			return false
		}
	}
	return true
}

// Columns is how many grid columns a content area width dp wide shows:
// the configured count, at most 2 at Compact widths and 1 at Narrow ones.
func Columns(width unit.Dp, configured int) int {
	switch {
	case width <= Narrow:
		return 1
	case width <= Compact:
		return min(configured, 2)
	}
	return configured
}

// Arrange places the enabled panels on a grid columns wide, for a config
// whose grid is configured columns wide. Each panel keeps its column and
// rises into rows left free above it, by disabled or removed panels, in
// the order of its row and then its column. On a grid narrower than the
// config's, the panels then flow in that reading order, as CSS grid's
// auto-placement does, each as wide as it was or as the grid, whichever
// is less.
func Arrange(panels []config.Panel, configured, columns int) []Cell {
	var order []config.Panel
	for _, p := range panels {
		if p.IsEnabled() {
			order = append(order, p)
		}
	}
	byPlace := func(a, b Cell) int { return cmp.Or(cmp.Compare(a.Y, b.Y), cmp.Compare(a.X, b.X)) }
	slices.SortStableFunc(order, func(a, b config.Panel) int {
		return byPlace(Cell{X: a.X, Y: a.Y}, Cell{X: b.X, Y: b.Y})
	})
	cells := make([]Cell, 0, len(order))
	for _, p := range order {
		c := Cell{ID: p.ID, X: p.X, W: p.W, H: p.H}
		// It fits at its own row at the latest, as panels placed before
		// it only moved up.
		for c.Y < p.Y && !fits(cells, c) {
			c.Y++
		}
		cells = append(cells, c)
	}
	if columns >= configured {
		return cells
	}
	slices.SortStableFunc(cells, byPlace)
	flowed := make([]Cell, 0, len(cells))
	row, col := 0, 0 // the auto-placement cursor, which never moves back
	for _, c := range cells {
		c.W = min(c.W, columns)
		for {
			if col+c.W > columns {
				row, col = row+1, 0
			}
			c.X, c.Y = col, row
			if fits(flowed, c) {
				break
			}
			col++
		}
		flowed = append(flowed, c)
		col += c.W
	}
	return flowed
}

// Grid lays out the panels of a config as tiles, and scrolls them when
// they are taller than the window.
type Grid struct {
	tiles  map[string]*tile
	scroll gesture.Scroll
	offset int                        // px scrolled down
	placed map[string]image.Rectangle // each tile's bounds, for tests
}

// ActionKind is what the user asked for from a tile.
type ActionKind int

const (
	// ActionConfigure is the menu's Configure… (#16).
	ActionConfigure ActionKind = iota
	// ActionDisable is the menu's Disable panel (#15).
	ActionDisable
	// ActionRestart is the error tile's Restart.
	ActionRestart
	// ActionLogs is the error tile's Logs.
	ActionLogs
)

// Action is a request from panel Panel's tile, which the host carries
// out.
type Action struct {
	Kind  ActionKind
	Panel string
}

// gridGap is the space between tiles.
const gridGap unit.Dp = 14

// contentPadding is the padding around the grid at a window width.
func contentPadding(width unit.Dp) unit.Dp {
	switch {
	case width <= Narrow:
		return 14
	case width <= Compact:
		return 20
	}
	return 28
}

// Layout draws cfg's enabled panels, with the state of each from states,
// over gtx's constraints, and returns what the user asked for from them.
func (g *Grid) Layout(gtx layout.Context, th *theme.Theme, cfg config.Config, states []pluginhost.PanelState) (layout.Dimensions, []Action) {
	area := gtx.Constraints.Max
	width := widthDp(gtx)
	pad := gtx.Dp(contentPadding(width))
	content := min(area.X-2*pad, gtx.Dp(1600))
	columns := Columns(unit.Dp(float32(content)/gtx.Metric.PxPerDp), cfg.Grid.Columns)
	cells := Arrange(cfg.Panels, cfg.Grid.Columns, columns)
	gap, rowH := gtx.Dp(gridGap), gtx.Dp(unit.Dp(cfg.Grid.RowHeight))
	colW := (content - gap*(columns-1)) / columns
	rows := 0
	for _, c := range cells {
		rows = max(rows, c.Y+c.H)
	}
	height := 2 * pad
	if rows > 0 {
		height += rows*rowH + (rows-1)*gap
	}

	overflow := max(height-area.Y, 0)
	g.offset += g.scroll.Update(gtx.Metric, gtx.Source, gtx.Now, gesture.Vertical,
		pointer.ScrollRange{}, pointer.ScrollRange{Min: -g.offset, Max: overflow - g.offset})
	g.offset = min(max(g.offset, 0), overflow)
	defer clip.Rect{Max: area}.Push(gtx.Ops).Pop()
	g.scroll.Add(gtx.Ops)

	byID := make(map[string]pluginhost.PanelState, len(states))
	for _, s := range states {
		byID[s.ID] = s
	}
	plugins := make(map[string]string, len(cfg.Panels))
	for _, p := range cfg.Panels {
		plugins[p.ID] = p.Plugin
	}
	seen := make(map[string]*tile, len(cells))
	g.placed = make(map[string]image.Rectangle, len(cells))
	var actions []Action
	origin := image.Pt((area.X-content)/2, pad-g.offset)
	for _, c := range cells {
		t := g.tiles[c.ID]
		if t == nil {
			t = &tile{}
		}
		seen[c.ID] = t
		s, ok := byID[c.ID]
		if !ok { // just added; Apply has not reported it yet
			s = pluginhost.PanelState{ID: c.ID, Plugin: plugins[c.ID], State: pluginhost.Starting}
		}
		r := image.Rectangle{
			Min: origin.Add(image.Pt(c.X*(colW+gap), c.Y*(rowH+gap))),
		}
		r.Max = r.Min.Add(image.Pt(c.W*colW+(c.W-1)*gap, c.H*rowH+(c.H-1)*gap))
		g.placed[c.ID] = r
		tgtx := gtx
		tgtx.Constraints = layout.Exact(r.Size())
		st := op.Offset(r.Min).Push(gtx.Ops)
		for _, k := range t.Layout(tgtx, th, s, width <= Narrow) {
			actions = append(actions, Action{Kind: k, Panel: c.ID})
		}
		st.Pop()
	}
	g.tiles = seen
	return layout.Dimensions{Size: area}, actions
}
