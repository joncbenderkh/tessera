// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"cmp"
	"image"
	"math"
	"slices"

	"gioui.org/f32"
	"gioui.org/gesture"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
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
// rises into rows left free above it, as config.Compact has them. On a
// grid narrower than the config's, the panels then flow in reading
// order, as CSS grid's auto-placement does, each as wide as it was or as
// the grid, whichever is less. The cells come in reading order.
func Arrange(panels []config.Panel, configured, columns int) []Cell {
	risen := slices.Clone(panels)
	config.Compact(risen)
	var cells []Cell
	for _, p := range risen {
		if p.IsEnabled() {
			cells = append(cells, Cell{ID: p.ID, X: p.X, Y: p.Y, W: p.W, H: p.H})
		}
	}
	byPlace := func(a, b Cell) int { return cmp.Or(cmp.Compare(a.Y, b.Y), cmp.Compare(a.X, b.X)) }
	slices.SortStableFunc(cells, byPlace)
	if columns >= configured {
		return cells
	}
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
	// Edit is edit mode: the tiles can be dragged by the head to move
	// them, by the corner to resize them, and moved and resized with the
	// arrow keys, while the grid shows all the config's columns.
	Edit bool
	// Covered is set while a menu over the grid takes the keys, so the
	// arrow keys move no panel behind it.
	Covered bool

	fits    bool   // the last Layout showed all the config's columns
	columns int    // the columns the last Layout showed
	wide    bool   // the last Layout's config has more columns than any window shows
	opened  bool   // the last Layout opened a tile's menu
	follow  string // the panel the keyboard moved last frame, kept in view
	tiles   map[string]*tile
	scroll  gesture.Scroll
	offset  int                        // px scrolled down
	placed  map[string]image.Rectangle // each tile's bounds, for tests
	sizes   tileSizes                  // minTile's last result
	// grow holds the panels Add panel made in this edit mode, until
	// their plugins describe themselves while the panels can move, and
	// they grow to their default and least sizes.
	grow map[string]bool
}

// tileSizes caches minTile for a text size and head.
type tileSizes struct {
	metric     unit.Metric
	th         *theme.Theme
	narrow, ok bool
	least      image.Point
	stacked    int
	head       int
}

// minTile is minTile, measured again only when the text size, the
// theme or the head changes.
func (g *Grid) minTile(gtx layout.Context, th *theme.Theme, narrow bool) (image.Point, int, int) {
	if s := g.sizes; !s.ok || s.metric != gtx.Metric || s.th != th || s.narrow != narrow {
		least, stacked, head := minTile(gtx, th, narrow)
		g.sizes = tileSizes{metric: gtx.Metric, th: th, narrow: narrow, ok: true, least: least, stacked: stacked, head: head}
	}
	return g.sizes.least, g.sizes.stacked, g.sizes.head
}

// Added tells the grid that Add panel made panel id. Once its plugin
// has described itself, while the panels can move (see Movable), the
// grid asks, with an ActionPlace, to make it as large as the plugin's
// default size and its least size, moved left if the grid's edge is in
// the way. Leaving edit mode first forgets it.
func (g *Grid) Added(id string) {
	if g.grow == nil {
		g.grow = map[string]bool{}
	}
	g.grow[id] = true
}

// ShownColumns is how many columns the last Layout showed, or 0 before
// the first.
func (g *Grid) ShownColumns() int { return g.columns }

// MinHeight is the shortest content area, at gtx's width and text size,
// that shows a whole tile of the smallest size: one whose menu, or
// failed panel's buttons, just fit. The window keeps at least this much
// room below its header.
func (g *Grid) MinHeight(gtx layout.Context, th *theme.Theme) int {
	width := widthDp(gtx)
	least, stacked, _ := g.minTile(gtx, th, width <= Narrow)
	return 2*gtx.Dp(contentPadding(width)) + max(least.Y, stacked)
}

// ActionKind is what the user asked for from a tile.
type ActionKind int

const (
	// ActionConfigure is the menu's Configure… (#16).
	ActionConfigure ActionKind = iota
	// ActionDisable is the menu's Disable panel.
	ActionDisable
	// ActionRestart is the error tile's Restart.
	ActionRestart
	// ActionLogs is the error tile's Logs.
	ActionLogs
	// ActionEvent is input for the panel's plugin, in Event.
	ActionEvent
	// ActionPlace moves or resizes the panel to Cell, from a drag in
	// edit mode, or to the size a panel Add panel made needs (see
	// Grid.Added).
	ActionPlace
	// ActionStep moves the panel by Step, from an arrow key in edit
	// mode: a column sideways, or past the next panel up or down, as
	// config.Config.Step does.
	ActionStep
	// ActionResize makes the panel wider and taller by Step, from Shift
	// and an arrow key in edit mode, as config.Config.Resize does.
	ActionResize
	// ActionEnable is the edit bar's Enable for a disabled panel.
	ActionEnable
	// ActionAdd is the edit bar's Add panel, with the plugin in Plugin.
	ActionAdd
)

// Action is a request from panel Panel's tile, or from the edit bar,
// which the host carries out.
type Action struct {
	Kind   ActionKind
	Panel  string
	Event  *tesserav1.HandleEventRequest // for ActionEvent
	Cell   Cell                          // for ActionPlace
	Step   image.Point                   // for ActionStep and ActionResize
	Plugin string                        // for ActionAdd
}

// Movable reports whether the last Layout let the panels move: it was in
// edit mode, and showed all the config's columns, so the cells it showed
// are the config's, compacted.
func (g *Grid) Movable() bool { return g.Edit && g.fits }

// MenuOpened reports whether the last Layout opened a tile's menu.
func (g *Grid) MenuOpened() bool { return g.opened }

// CloseMenus closes the tiles' menus, as when another menu opens.
func (g *Grid) CloseMenus() {
	for _, t := range g.tiles {
		t.open = false
	}
}

// EditHint is the hint for the edit bar after the last Layout: how to
// move panels, or why they cannot move. It holds for the frame edit
// mode starts in too, since the last Layout measured the columns.
func (g *Grid) EditHint() string {
	switch {
	case g.fits:
		return HintMove
	case g.wide:
		return HintWide
	}
	return HintNarrow
}

const (
	gridGap    unit.Dp = 14   // the space between tiles
	maxContent unit.Dp = 1600 // the widest the grid gets
	// minColumn is the narrowest a column gets; large text widens it
	// further, to fit a failed panel's buttons (minTile).
	minColumn unit.Dp = 200
)

// Unselect takes back the activation that sent event in panel's latest
// frame, one its plugin did not get, and highlights the last activation
// left, or the one from before the frame.
func (g *Grid) Unselect(panel string, event *tesserav1.HandleEventRequest) {
	t := g.tiles[panel]
	if t == nil {
		return
	}
	h := t.content.history
	for i := len(h) - 1; i > 0; i-- {
		if h[i].event == event {
			t.content.history = slices.Delete(h, i, i+1)
			t.content.selected = t.content.history[len(t.content.history)-1].id
			return
		}
	}
}

// Unsent makes panel's view send the visible range of its widget id
// again on the next frame, for one its plugin's queue refused.
func (g *Grid) Unsent(panel, id string) {
	if t := g.tiles[panel]; t != nil {
		delete(t.content.sent, "id:"+id)
	}
}

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
	if !g.Edit {
		g.grow = nil // panels added in edit mode grow only there
	}
	area := gtx.Constraints.Max
	width := widthDp(gtx)
	pad := gtx.Dp(contentPadding(width))
	content := max(min(area.X-2*pad, gtx.Dp(maxContent)), 0)
	// Large text takes fewer columns and taller rows: a tile never gets
	// too small for a failed panel's buttons.
	least, stacked, headH := g.minTile(gtx, th, width <= Narrow)
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
	// A panel the keyboard moved is scrolled into view, its top first,
	// so it stays laid out and keeps the focus.
	if i := slices.IndexFunc(cells, func(c Cell) bool { return c.ID == g.follow }); i >= 0 {
		c := cells[i]
		top := pad + c.Y*(rowH+gap)
		bottom := top + c.H*rowH + (c.H-1)*gap
		g.offset = min(max(g.offset, bottom+pad-area.Y), top-pad)
		g.offset = min(max(g.offset, 0), overflow)
	}
	g.follow = ""
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
	fits := columns >= cfg.Grid.Columns
	// The most columns any window shows, at this text size.
	most := max(min(Columns(2*maxContent, cfg.Grid.Columns), (gtx.Dp(maxContent)+gap)/(least.X+gap)), 1)
	wide := cfg.Grid.Columns > most
	if fits != g.fits || wide != g.wide {
		// The edit bar, drawn before the grid, says whether panels move.
		gtx.Execute(op.InvalidateCmd{})
	}
	g.fits, g.wide = fits, wide
	if area.X > 0 { // a window with no room, as while minimized, shows none
		g.columns = columns
	}
	mode := editOff
	switch {
	case g.Movable():
		mode = editMove
	case g.Edit:
		mode = editOn
	}
	step := image.Pt(colW+gap, rowH+gap)
	var growth []Action         // panels Add panel made, grown
	var preview image.Rectangle // where a dragged tile would go
	refused := false            // and it cannot go there
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
		// The least size the plugin asks for, at most the grid.
		fewest := minCells(gtx, s.Info.GetMinBody(), colW, rowH, gap, headH)
		fewest.X = min(fewest.X, cfg.Grid.Columns)
		// A panel Add panel made grows, but not while its tile is
		// dragged: the drop would place it at its old size.
		grow := func() {
			if mode == editMove && g.grow[c.ID] && s.Info != nil && t.drag == dragNone {
				if a, ok := grown(c, cfg.Grid.Columns, s.Info, fewest); ok {
					growth = append(growth, a)
				} else {
					delete(g.grow, c.ID)
				}
			}
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
			grow()
			continue
		}
		t.view = image.Rectangle{Max: area}.Sub(r.Min)
		tgtx := gtx
		tgtx.Constraints = layout.Exact(r.Size())
		st := op.Offset(r.Min).Push(gtx.Ops)
		wasOpen := t.open
		t.covered = g.Covered
		kinds, events := t.Layout(tgtx, th, s, width <= Narrow, mode)
		for _, k := range kinds {
			actions = append(actions, Action{Kind: k, Panel: c.ID})
		}
		for _, e := range events {
			actions = append(actions, Action{Kind: ActionEvent, Panel: c.ID, Event: e})
		}
		st.Pop()
		if mode == editMove {
			if t.drag != dragNone {
				var to Cell
				to, refused = landing(cfg, dragTarget(c, t.drag, t.dragBy, step, columns, fewest))
				preview = image.Rectangle{Min: origin.Add(image.Pt(to.X*step.X, to.Y*step.Y))}
				preview.Max = preview.Min.Add(image.Pt(to.W*step.X-gap, to.H*step.Y-gap))
			}
			if t.dropped != dragNone {
				if to := dragTarget(c, t.dropped, t.dragBy, step, columns, fewest); to != c {
					actions = append(actions, Action{Kind: ActionPlace, Panel: c.ID, Cell: to})
				}
			}
			g.keyActions(&actions, cfg, c, t.keys, fewest)
		}
		grow()
		if t.open && !wasOpen {
			opened = t
		}
	}
	if !preview.Empty() {
		rr := gtx.Dp(theme.RadiusLarge)
		ink := th.Accent
		if refused {
			ink = th.Error
		}
		fill := ink
		fill.A = 0x1a
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(preview, rr).Op(gtx.Ops))
		strokeRRect(gtx, preview, rr, gtx.Dp(2), ink)
	}
	// One menu at a time: the keyboard can reach another tile's ⋯ past
	// an open menu's backdrop.
	if opened != nil {
		for _, t := range seen {
			t.open = t == opened
		}
	}
	g.opened = opened != nil
	g.tiles = seen
	// Each growth is worked out from this frame's layout, so it waits
	// for a frame that moves nothing else: a drop or a key, of its own
	// tile or another, or another growth, would undo it or land off.
	// One a frame, then, and the next frame is asked for.
	moved := slices.ContainsFunc(actions, func(a Action) bool {
		return a.Kind == ActionPlace || a.Kind == ActionStep || a.Kind == ActionResize
	})
	if len(growth) > 0 && !moved {
		actions = append(actions, growth[0])
		delete(g.grow, growth[0].Panel)
		growth = growth[1:]
	}
	if len(growth) > 0 {
		gtx.Execute(op.InvalidateCmd{})
	}
	for id := range g.grow {
		if seen[id] == nil { // gone, as when its save failed
			delete(g.grow, id)
		}
	}
	return layout.Dimensions{Size: area}, actions
}

// landing is where cell to ends up once placed on cfg, with the panels
// in its way moved and the grid compacted again, or to itself, refused,
// if it cannot be placed there. It is where a dragged tile is shown to
// go.
func landing(cfg config.Config, to Cell) (_ Cell, refused bool) {
	cfg.Panels = slices.Clone(cfg.Panels)
	if cfg.Place(to.ID, to.X, to.Y, to.W, to.H) != nil {
		return to, true
	}
	i := slices.IndexFunc(cfg.Panels, func(p config.Panel) bool { return p.ID == to.ID })
	p := cfg.Panels[i]
	return Cell{ID: p.ID, X: p.X, Y: p.Y, W: p.W, H: p.H}, false
}

// keyActions adds to actions those of keys, the arrow keys pressed on
// cell c's tile this frame. They are carried out on a copy of cfg, as
// the host would, so each resize is held to fewest cells from where the
// keys before it leave the panel. A change the host would refuse, as
// past the last row, leaves the copy as it was, as it leaves the
// layout; a resize that changes nothing, so, or at the grid's edge, is
// dropped.
func (g *Grid) keyActions(actions *[]Action, cfg config.Config, c Cell, keys []Action, fewest image.Point) {
	if len(keys) == 0 {
		return
	}
	sim := cfg
	sim.Panels = slices.Clone(cfg.Panels)
	at := slices.IndexFunc(sim.Panels, func(p config.Panel) bool { return p.ID == c.ID })
	for _, a := range keys {
		a.Panel = c.ID
		was := sim.Panels[at]
		if a.Kind == ActionResize {
			// Never below the least size, or below the panel's size
			// if it is less already.
			floor := image.Pt(min(fewest.X, was.W), min(fewest.Y, was.H))
			a.Step = image.Pt(max(a.Step.X, floor.X-was.W), max(a.Step.Y, floor.Y-was.H))
		}
		if change := keyChange(a); change != nil {
			next := sim
			next.Panels = slices.Clone(sim.Panels)
			if change(&next) == nil {
				sim = next
			}
		}
		if p := sim.Panels[at]; a.Kind == ActionResize && p.W == was.W && p.H == was.H {
			continue
		}
		*actions = append(*actions, a)
		g.follow = c.ID
	}
}

// keyChange is what a, an ActionStep or ActionResize, does to a
// config, as the host carries it out.
func keyChange(a Action) func(*config.Config) error {
	switch a.Kind {
	case ActionStep:
		return func(c *config.Config) error { return c.Step(a.Panel, a.Step.X, a.Step.Y) }
	case ActionResize:
		return func(c *config.Config) error { return c.Resize(a.Panel, a.Step.X, a.Step.Y) }
	}
	return nil
}

// grown is the ActionPlace that makes cell c, on a grid columns wide,
// as large as its plugin's default size, from info, and fewest cells,
// if it is smaller in either dimension. It moves left as far as the
// grid's right edge needs.
func grown(c Cell, columns int, info *tesserav1.DescribeResponse, fewest image.Point) (Action, bool) {
	def := image.Pt(int(min(info.GetDefaultSize().GetColumns(), 24)), int(min(info.GetDefaultSize().GetRows(), 1000)))
	w := min(max(c.W, def.X, fewest.X), columns)
	h := max(c.H, def.Y, fewest.Y)
	if w == c.W && h == c.H {
		return Action{}, false
	}
	to := Cell{ID: c.ID, X: min(c.X, columns-w), Y: c.Y, W: w, H: h}
	return Action{Kind: ActionPlace, Panel: c.ID, Cell: to}, true
}

// dragTarget is where cell c goes when its tile is dragged by by px, by
// the head to move it or by the corner to resize it, on a grid columns
// wide whose cells are step px apart. A resize stops at fewest cells,
// or at c's size if that is less.
func dragTarget(c Cell, kind dragKind, by f32.Point, step image.Point, columns int, fewest image.Point) Cell {
	dx := int(math.Round(float64(by.X) / float64(max(step.X, 1))))
	dy := int(math.Round(float64(by.Y) / float64(max(step.Y, 1))))
	switch kind {
	case dragMove:
		c.X = min(max(c.X+dx, 0), columns-c.W)
		c.Y = max(c.Y+dy, 0)
	case dragResize:
		c.W = min(max(c.W+dx, min(fewest.X, c.W), 1), columns-c.X)
		c.H = max(c.H+dy, min(fewest.Y, c.H), 1)
	}
	return c
}
