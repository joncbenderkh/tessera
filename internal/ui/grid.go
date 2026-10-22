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

// Columns is how many grid columns a window width dp wide shows: the
// configured count, at most 2 at Compact widths and 1 at Narrow ones,
// and never so many that a column is narrower than minColumn.
func Columns(width unit.Dp, configured int) int {
	switch {
	case width <= Narrow:
		return 1
	case width <= Compact:
		configured = min(configured, 2)
	}
	content := min(width-2*contentPadding(width), maxContent)
	return max(min(configured, int((content+gridGap)/(minColumn+gridGap))), 1)
}

// Arrange places the enabled panels on a grid columns wide, for a config
// whose grid is configured columns wide. Each panel keeps its column and
// rises into rows left free above it, by disabled or removed panels, in
// the order of its row and then its column; it stops below the first
// panel in its way, so it never passes one. On a grid narrower than the
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
		// It rises to just below the panels placed before it in its
		// columns, all of which end by its own row: they come no lower
		// in the config, enabled panels do not overlap, and they only
		// moved up.
		c := Cell{ID: p.ID, X: p.X, W: p.W, H: p.H}
		for _, d := range cells {
			if c.X < d.X+d.W && d.X < c.X+c.W {
				c.Y = max(c.Y, d.Y+d.H)
			}
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
	sizes  tileSizes                  // minTile's last result
}

// tileSizes caches minTile for a text size and head.
type tileSizes struct {
	metric     unit.Metric
	th         *theme.Theme
	narrow, ok bool
	least      image.Point
	stacked    int
}

// minTile is minTile, measured again only when the text size, the
// theme or the head changes.
func (g *Grid) minTile(gtx layout.Context, th *theme.Theme, narrow bool) (image.Point, int) {
	if s := g.sizes; !s.ok || s.metric != gtx.Metric || s.th != th || s.narrow != narrow {
		least, stacked := minTile(gtx, th, narrow)
		g.sizes = tileSizes{metric: gtx.Metric, th: th, narrow: narrow, ok: true, least: least, stacked: stacked}
	}
	return g.sizes.least, g.sizes.stacked
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

const (
	gridGap    unit.Dp = 14   // the space between tiles
	maxContent unit.Dp = 1600 // the widest the grid gets
	// minColumn is the narrowest a column gets; large text widens it
	// further, to fit a failed panel's buttons (minTile).
	minColumn unit.Dp = 200
)

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
	content := max(min(area.X-2*pad, gtx.Dp(maxContent)), 0)
	// Large text takes fewer columns and taller rows: a tile never gets
	// too small for a failed panel's buttons.
	least, stacked := g.minTile(gtx, th, width <= Narrow)
	gap := gtx.Dp(gridGap)
	columns := max(min(Columns(width, cfg.Grid.Columns), (content+gap)/(least.X+gap)), 1)
	cells := Arrange(cfg.Panels, cfg.Grid.Columns, columns)
	colW := max((content-gap*(columns-1))/columns, 0)
	rowH := max(gtx.Dp(unit.Dp(cfg.Grid.RowHeight)), least.Y)
	if colW < least.X {
		rowH = max(rowH, stacked)
	}
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
	var opened *tile // the tile whose menu opened this frame
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
		// Tiles out of sight are not laid out: a grid can be far taller
		// than the window. One that was open closes.
		if !r.Overlaps(image.Rectangle{Max: area}) {
			t.open = false
			continue
		}
		t.view = image.Rectangle{Max: area}.Sub(r.Min)
		tgtx := gtx
		tgtx.Constraints = layout.Exact(r.Size())
		st := op.Offset(r.Min).Push(gtx.Ops)
		wasOpen := t.open
		for _, k := range t.Layout(tgtx, th, s, width <= Narrow) {
			actions = append(actions, Action{Kind: k, Panel: c.ID})
		}
		st.Pop()
		if t.open && !wasOpen {
			opened = t
		}
	}
	// One menu at a time: the keyboard can reach another tile's ⋯ past
	// an open menu's backdrop.
	if opened != nil {
		for _, t := range seen {
			t.open = t == opened
		}
	}
	g.tiles = seen
	return layout.Dimensions{Size: area}, actions
}
