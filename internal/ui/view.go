// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"image/color"
	"slices"
	"sort"
	"strconv"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/image/math/fixed"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// viewState draws a panel's view, schema v1, and keeps what must last
// from one view to the next: scroll positions, row clickables, the row
// last activated, and the visible ranges reported to the plugin.
type viewState struct {
	lists    map[string]*layout.List            // by "id:" and the widget id, or "#n" for the nth unnamed table or list
	rows     map[string]*widget.Clickable       // by action_id
	sent     map[string][2]int                  // by widget id: the visible range last reported
	loading  bool                               // inside a loading stack: everything is a placeholder
	narrow   bool                               // at natural width in a row: a gauge takes its dial's width, not all there is
	selected string                             // the action_id last activated
	history  []activation                       // the selection before this frame's activations, then each of them, for those the plugin does not get
	digits   map[digitKey]int                   // the widest digit's advance, by face and size
	digitIDs map[digitKey]map[text.GlyphID]bool // the digits' glyphs, by face and size

	// For the view being drawn. A view is never changed once published,
	// so owners and keys are built once per view, not every frame.
	indexed *tesserav1.View
	owners  map[string]any // each widget id and action_id, to its first owner
	keys    map[*tesserav1.Widget]string
	used    map[string]bool // the lists and rows drawn
	events  []*tesserav1.HandleEventRequest
	cut     bool // some of the view did not fit
}

// activation is a row or item activated, and the event that told the
// plugin; the selection from before a frame has no event.
type activation struct {
	id    string
	event *tesserav1.HandleEventRequest
}

type digitKey struct {
	font   font.Font
	size   unit.Sp
	px     float32 // PxPerSp, which the advance scales with
	locale system.Locale
}

// Layout draws view's body over gtx's constraints, clipped to them, and
// returns the input the user gave it, for the plugin. Where a widget
// does not fit, it fades out into the tile at the edge it is cut at.
func (v *viewState) Layout(gtx layout.Context, th *theme.Theme, view *tesserav1.View) []*tesserav1.HandleEventRequest {
	if v.lists == nil {
		v.lists = map[string]*layout.List{}
		v.rows = map[string]*widget.Clickable{}
		v.sent = map[string][2]int{}
		v.digits = map[digitKey]int{}
		v.digitIDs = map[digitKey]map[text.GlyphID]bool{}
	}
	if v.owners == nil || view != v.indexed {
		v.owners = map[string]any{}
		v.keys = map[*tesserav1.Widget]string{}
		v.index(view.GetBody(), new(counts))
		v.indexed = view
	}
	v.used = map[string]bool{}
	v.events = nil
	v.history = v.history[:0]
	v.cut = false

	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
	gtx.Constraints.Min = image.Point{}
	v.widget(gtx, th, view.GetBody())

	for k := range v.lists {
		if !v.used[k] {
			delete(v.lists, k)
			delete(v.sent, k)
		}
	}
	for k := range v.rows {
		if !v.used["row:"+k] {
			delete(v.rows, k)
		}
	}
	return v.events
}

// index walks the tree depth first, as the protocol orders it: the
// first widget with an id, and the first row or item with an action_id,
// keep it; later ones have none. Each table and list gets the key its
// scroll position is kept by.
func (v *viewState) index(w *tesserav1.Widget, n *counts) {
	if w == nil {
		return
	}
	if id := w.GetId(); id != "" {
		if _, ok := v.owners["id:"+id]; !ok {
			v.owners["id:"+id] = w
		}
	}
	claim := func(id string, owner any) {
		if id == "" {
			return
		}
		if _, ok := v.owners["action:"+id]; !ok {
			v.owners["action:"+id] = owner
		}
	}
	switch k := w.GetKind().(type) {
	case *tesserav1.Widget_Table:
		for _, r := range k.Table.GetRows() {
			claim(r.GetActionId(), r)
		}
	case *tesserav1.Widget_List:
		for _, it := range k.List.GetItems() {
			claim(it.GetActionId(), it)
		}
	case *tesserav1.Widget_Stack:
		for _, it := range k.Stack.GetItems() {
			v.index(it.GetWidget(), n)
		}
		return
	default:
		return
	}
	// Ids and places have their own prefixes, so a plugin's id never
	// names an unnamed widget's state.
	// A widget whose id an earlier one took has its own count, so it
	// does not renumber the unnamed widgets after it.
	switch {
	case v.id(w) != "":
		v.keys[w] = "id:" + v.id(w)
	case w.GetId() != "":
		n.dups++
		v.keys[w] = "dup#" + strconv.Itoa(n.dups)
	default:
		n.unnamed++
		v.keys[w] = "#" + strconv.Itoa(n.unnamed)
	}
}

// counts numbers the tables and lists without an id of their own.
type counts struct{ unnamed, dups int }

// id is w's id, if no widget before it has it.
func (v *viewState) id(w *tesserav1.Widget) string {
	if id := w.GetId(); id != "" && v.owners["id:"+id] == w {
		return id
	}
	return ""
}

// action is the action_id of owner, a row or item, if none before it
// has it.
func (v *viewState) action(id string, owner any) string {
	if id != "" && v.owners["action:"+id] == owner {
		return id
	}
	return ""
}

// widget draws w at its natural size within gtx's constraints.
func (v *viewState) widget(gtx layout.Context, th *theme.Theme, w *tesserav1.Widget) layout.Dimensions {
	if w == nil {
		return layout.Dimensions{}
	}
	// Each widget takes its natural size; a weighted stack item gets
	// its share from the stack, whatever the child takes.
	gtx.Constraints.Min = image.Point{}
	// A loading stack draws each of its descendants as its placeholder,
	// in the stack's layout.
	if loading := w.GetLoading() || v.loading; loading && w.GetStack() != nil {
		was := v.loading
		v.loading = true
		defer func() { v.loading = was }()
	} else if loading {
		// A table or list keeps its scroll position through a loading
		// view.
		if k, ok := v.keys[w]; ok {
			v.used[k] = true
		}
		dims := v.skeleton(gtx, th, w)
		if room := gtx.Constraints.Max.Y; dims.Size.Y > room {
			v.cut = true
			dims.Size.Y = room
			fadeOut(gtx, th, image.Rectangle{Max: dims.Size}, false)
		}
		return dims
	}
	switch k := w.GetKind().(type) {
	case *tesserav1.Widget_Text:
		return v.whole(gtx, th, func(gtx layout.Context) layout.Dimensions { return v.text(gtx, th, k.Text) })
	case *tesserav1.Widget_Stat:
		return v.whole(gtx, th, func(gtx layout.Context) layout.Dimensions { return v.stat(gtx, th, k.Stat) })
	case *tesserav1.Widget_Gauge:
		return v.gauge(gtx, th, k.Gauge)
	case *tesserav1.Widget_Sparkline:
		return v.whole(gtx, th, func(gtx layout.Context) layout.Dimensions { return v.sparkline(gtx, th, k.Sparkline) })
	case *tesserav1.Widget_Table:
		return v.table(gtx, th, w, k.Table)
	case *tesserav1.Widget_List:
		return v.list(gtx, th, w, k.List)
	case *tesserav1.Widget_Stack:
		return v.stack(gtx, th, k.Stack)
	}
	return layout.Dimensions{}
}

// whole draws w, whose height does not depend on the room it gets, at
// its full height, and cuts it at gtx's, fading out there: a label told
// its room would stop drawing there unnoticed.
func (v *viewState) whole(gtx layout.Context, th *theme.Theme, w layout.Widget) layout.Dimensions {
	room := gtx.Constraints.Max.Y
	gtx.Constraints.Max.Y = 1 << 24
	cl := clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, room)}.Push(gtx.Ops)
	dims := w(gtx)
	cl.Pop()
	if over := dims.Size.Y - room; over > 0 {
		v.cut = true
		dims.Size.Y, dims.Baseline = room, max(dims.Baseline-over, 0)
		fadeOut(gtx, th, image.Rectangle{Max: dims.Size}, false)
	}
	return dims
}

// fadeHeight is how far a view that does not fit fades out.
const fadeHeight = theme.Space24

// fadeOut fades the end of r, its bottom or, across, its right edge,
// into the tile's surface, where what r holds is cut off, so the cut
// shows.
func fadeOut(gtx layout.Context, th *theme.Theme, r image.Rectangle, across bool) {
	transparent := th.Surface
	transparent.A = 0
	f := min(gtx.Dp(fadeHeight), r.Dx(), r.Dy())
	from, to := f32.Pt(0, float32(r.Max.Y-f)), f32.Pt(0, float32(r.Max.Y))
	if across {
		from, to = f32.Pt(float32(r.Max.X-f), 0), f32.Pt(float32(r.Max.X), 0)
		r.Min.X = r.Max.X - f
	} else {
		r.Min.Y = r.Max.Y - f
	}
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	paint.LinearGradientOp{Stop1: from, Color1: transparent, Stop2: to, Color2: th.Surface}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}

// textSize is the type scale step for size.
func textSize(size tesserav1.TextSize) unit.Sp {
	switch size {
	case tesserav1.TextSize_TEXT_SIZE_SMALL:
		return theme.TextMin
	case tesserav1.TextSize_TEXT_SIZE_LARGE:
		return theme.TextBody
	case tesserav1.TextSize_TEXT_SIZE_DISPLAY:
		return theme.TextValue
	}
	return theme.TextSmall
}

// viewTone is a view tone's color in th, or def for the default tone.
func viewTone(th *theme.Theme, tone tesserav1.Tone, def color.NRGBA) color.NRGBA {
	switch tone {
	case tesserav1.Tone_TONE_MUTED:
		return th.Muted
	case tesserav1.Tone_TONE_ACCENT:
		return th.Accent
	case tesserav1.Tone_TONE_OK:
		return th.Ok
	case tesserav1.Tone_TONE_WARN:
		return th.Warn
	case tesserav1.Tone_TONE_ERROR:
		return th.Error
	}
	return def
}

func (v *viewState) text(gtx layout.Context, th *theme.Theme, t *tesserav1.Text) layout.Dimensions {
	f := theme.Body
	if t.GetMonospace() {
		f = theme.Mono
	}
	return material.LabelStyle{
		Text: cut(t.GetValue(), maxText), TextSize: textSize(t.GetSize()), Color: viewTone(th, t.GetTone(), th.Fg),
		Font: f, Shaper: th.Shaper,
	}.Layout(gtx)
}

// compactStat is the width below which a stat uses the compact value
// size, as the mockup's narrow tiles do.
const compactStat unit.Dp = 200

// stat draws a label, the value with its unit, and a caption, as the
// mockup's stat. A caption with no tone is muted, as the mockup's.
func (v *viewState) stat(gtx layout.Context, th *theme.Theme, s *tesserav1.Stat) layout.Dimensions {
	size := theme.TextValue
	if widthDp(gtx) < compactStat {
		size = theme.TextStat
	}
	value := s.GetValue()
	if value == "" {
		value = "—"
	}
	var parts []recorded
	var gaps []int
	if s.GetLabel() != "" {
		parts = append(parts, record(gtx, wrapped(th, s.GetLabel(), theme.TextMin, th.Muted, theme.Body).Layout))
		gaps = append(gaps, gtx.Dp(6))
	}
	parts = append(parts, record(gtx, func(gtx layout.Context) layout.Dimensions {
		return v.valueWithUnit(gtx, th, value, size, s.GetUnit(), theme.TextBody)
	}))
	if s.GetCaption() != "" {
		gaps = append(gaps, gtx.Dp(7))
		parts = append(parts, record(gtx, wrapped(th, s.GetCaption(), theme.TextMin, viewTone(th, s.GetCaptionTone(), th.Muted), theme.Mono).Layout))
	}
	return column(gtx, parts, gaps)
}

// valueWithUnit draws value in tabular digits and unit after it, muted
// and smaller, on the same baseline.
func (v *viewState) valueWithUnit(gtx layout.Context, th *theme.Theme, value string, size unit.Sp, unitText string, unitSize unit.Sp) layout.Dimensions {
	face := font.Font{Typeface: theme.Body.Typeface, Weight: font.Medium}
	if unitText == "" {
		return v.tabular(gtx, th, value, size, th.Fg, face)
	}
	// The unit is measured first, so a value too wide is the one cut
	// off: a number without its unit would read as another number. A
	// unit takes at most half the width, so the value keeps some too.
	gap := gtx.Dp(3)
	ugtx := gtx
	ugtx.Constraints.Max.X = max((gtx.Constraints.Max.X-gap)/2, 0)
	u := recordLabel(ugtx, th, unitText, unitSize, th.Muted, theme.Body)
	vgtx := gtx
	vgtx.Constraints.Max.X = max(gtx.Constraints.Max.X-u.dims.Size.X-gap, 0)
	val := record(vgtx, func(gtx layout.Context) layout.Dimensions {
		return v.tabular(gtx, th, value, size, th.Fg, face)
	})
	// Baselines, from the top.
	vb, ub := val.dims.Size.Y-val.dims.Baseline, u.dims.Size.Y-u.dims.Baseline
	top := max(vb, ub)
	val.at(gtx, image.Pt(0, top-vb))
	u.at(gtx, image.Pt(val.dims.Size.X+gap, top-ub))
	h := max(top-vb+val.dims.Size.Y, top-ub+u.dims.Size.Y)
	return layout.Dimensions{Size: image.Pt(val.dims.Size.X+gap+u.dims.Size.X, h), Baseline: h - top}
}

// wrapped is a label that wraps to the width it gets.
func wrapped(th *theme.Theme, text string, size unit.Sp, c color.NRGBA, f font.Font) material.LabelStyle {
	return material.LabelStyle{Text: cut(text, maxText), TextSize: size, Color: c, Font: f, Shaper: th.Shaper}
}

// column places parts top to bottom, gaps[i] apart before parts[i+1].
func column(gtx layout.Context, parts []recorded, gaps []int) layout.Dimensions {
	y, w := 0, 0
	for i, p := range parts {
		if i > 0 {
			y += gaps[i-1]
		}
		p.at(gtx, image.Pt(0, y))
		y += p.dims.Size.Y
		w = max(w, p.dims.Size.X)
	}
	return layout.Dimensions{Size: image.Pt(w, y)}
}

// maxTabular is the most of a value tabular shapes: more could not fit
// in any tile, and a plugin's huge value must not cost a frame.
const maxTabular = 1024

// maxText is the most of any other text a label shapes, for the same
// reasons.
const maxText = 16 << 10

// cut returns s, or its first n bytes or fewer, on a rune boundary, and
// an ellipsis. The ellipsis shows the cut even when what is left draws
// nothing.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// tabular draws s on one line with every digit glyph in a cell as wide
// as the widest digit, so a value that changes keeps its width: Gio
// cannot turn on a font's tabular figures. It shapes s once and widens
// the digits where the shaper put them, so right-to-left runs keep
// their order and s is one label for a screen reader. A value too wide
// is cut off with an ellipsis, as a label is, at the width it has as
// plain text.
func (v *viewState) tabular(gtx layout.Context, th *theme.Theme, s string, size unit.Sp, c color.NRGBA, f font.Font) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	s = cut(s, maxTabular)
	cell := fixed.I(v.digitWidth(gtx, th, size, f))
	digits := v.digitGlyphs(gtx, th, size, f)
	th.Shaper.LayoutString(text.Parameters{
		Font: f, PxPerEm: fixed.I(gtx.Sp(size)), MaxLines: 1, MaxWidth: 1 << 24, Locale: gtx.Locale,
	}, s)
	var glyphs []text.Glyph
	var ascent, descent fixed.Int26_6
	for g, ok := th.Shaper.NextGlyph(); ok; g, ok = th.Shaper.NextGlyph() {
		ascent, descent = max(ascent, g.Ascent), max(descent, g.Descent)
		glyphs = append(glyphs, g)
	}
	left, w := inCells(glyphs, digits, cell)
	width, top := w.Ceil(), ascent.Ceil()
	if width > gtx.Constraints.Max.X {
		return label(th, s, size, c, f).Layout(gtx)
	}
	height := top + descent.Ceil()
	defer clip.Rect{Max: image.Pt(width, height)}.Push(gtx.Ops).Pop()
	semantic.LabelOp(s).Add(gtx.Ops)
	if len(glyphs) > 0 {
		// Shape and Bitmaps place the glyphs from the first one's position.
		at := op.Affine(f32.AffineId().Offset(f32.Pt(float32(glyphs[0].X-left)/64, float32(top)))).Push(gtx.Ops)
		paint.FillShape(gtx.Ops, c, clip.Outline{Path: th.Shaper.Shape(glyphs)}.Op())
		if call := th.Shaper.Bitmaps(glyphs); call != (op.CallOp{}) {
			call.Add(gtx.Ops)
		}
		at.Pop()
	}
	return layout.Dimensions{Size: image.Pt(width, height), Baseline: height - top}
}

// inCells moves glyphs, as shaped, so each digit glyph sits centred in
// a cell as wide as cell and the rest move over by what the cells left
// of them add. It returns the leftmost glyph's x and the width.
func inCells(glyphs []text.Glyph, digits map[text.GlyphID]bool, cell fixed.Int26_6) (left, width fixed.Int26_6) {
	type widened struct{ x, end, by fixed.Int26_6 }
	var cells []widened // each digit's shaped x, and how much its cell adds
	var right fixed.Int26_6
	for i, g := range glyphs {
		if i == 0 || g.X < left {
			left = g.X
		}
		right = max(right, g.X+g.Advance)
		if digits[g.ID] {
			cells = append(cells, widened{g.X, g.X + g.Advance, cell - g.Advance})
		}
	}
	slices.SortFunc(cells, func(a, b widened) int { return int(a.x - b.x) })
	// shift[i] is what the cells left of the ith digit add.
	shift := make([]fixed.Int26_6, len(cells)+1)
	for i, w := range cells {
		shift[i+1] = shift[i] + w.by
	}
	for i := range glyphs {
		g := &glyphs[i]
		n := sort.Search(len(cells), func(j int) bool { return cells[j].x >= g.X })
		by := shift[n]
		switch {
		case digits[g.ID]:
			by += cells[n].by / 2
		case g.Advance == 0 && n > 0 && cells[n-1].end == g.X:
			// A mark on the digit before it moves with that digit.
			by = shift[n-1] + cells[n-1].by/2
		}
		g.X += by
	}
	return left, right - left + shift[len(cells)]
}

// digitGlyphs is the set of glyphs f has for the digits 0 to 9 at size.
func (v *viewState) digitGlyphs(gtx layout.Context, th *theme.Theme, size unit.Sp, f font.Font) map[text.GlyphID]bool {
	k := digitKey{f, size, gtx.Metric.PxPerSp, gtx.Locale}
	if ids, ok := v.digitIDs[k]; ok {
		return ids
	}
	th.Shaper.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.I(gtx.Sp(size)), MaxLines: 1, MaxWidth: 1 << 24, Locale: gtx.Locale}, "0123456789")
	ids := map[text.GlyphID]bool{}
	for g, ok := th.Shaper.NextGlyph(); ok; g, ok = th.Shaper.NextGlyph() {
		if g.Runes == 1 && g.Advance > 0 {
			ids[g.ID] = true
		}
	}
	v.digitIDs[k] = ids
	return ids
}

// digitWidth is the advance of the widest digit in f at size.
func (v *viewState) digitWidth(gtx layout.Context, th *theme.Theme, size unit.Sp, f font.Font) int {
	k := digitKey{f, size, gtx.Metric.PxPerSp, gtx.Locale}
	if w, ok := v.digits[k]; ok {
		return w
	}
	mgtx := gtx
	mgtx.Constraints = layout.Constraints{Max: image.Pt(1<<20, 1<<20)}
	w := 0
	for d := '0'; d <= '9'; d++ {
		w = max(w, recordLabel(mgtx, th, string(d), size, th.Fg, f).dims.Size.X)
	}
	v.digits[k] = w
	return w
}

// spacing is the gap a Spacing step names.
func spacing(s tesserav1.Spacing) unit.Dp {
	switch s {
	case tesserav1.Spacing_SPACING_NONE:
		return 0
	case tesserav1.Spacing_SPACING_EXTRA_SMALL:
		return theme.Space4
	case tesserav1.Spacing_SPACING_SMALL:
		return theme.Space8
	case tesserav1.Spacing_SPACING_LARGE:
		return theme.Space16
	case tesserav1.Spacing_SPACING_EXTRA_LARGE:
		return theme.Space24
	}
	return theme.Space12
}

// setNarrow sets v.narrow, and returns what puts it back.
func (v *viewState) setNarrow(narrow bool) (restore func()) {
	was := v.narrow
	v.narrow = narrow
	return func() { v.narrow = was }
}

// stack lays its items out along its axis: weight 0 at natural size,
// the weighted ones sharing what is left in proportion, with the gap
// between them and a divider centred in each gap if asked for.
func (v *viewState) stack(gtx layout.Context, th *theme.Theme, s *tesserav1.Stack) layout.Dimensions {
	items := s.GetItems()
	if len(items) == 0 {
		return layout.Dimensions{}
	}
	horizontal := s.GetAxis() == tesserav1.Axis_AXIS_HORIZONTAL
	// main and cross pick a point's coordinates along and across the axis.
	main := func(p image.Point) int {
		if horizontal {
			return p.X
		}
		return p.Y
	}
	cross := func(p image.Point) int {
		if horizontal {
			return p.Y
		}
		return p.X
	}
	pt := func(m, c int) image.Point {
		if horizontal {
			return image.Pt(m, c)
		}
		return image.Pt(c, m)
	}
	gap := gtx.Dp(spacing(s.GetGap()))
	line := gtx.Dp(1)
	if s.GetDividers() {
		gap = max(gap, line)
	}
	crossMax := cross(gtx.Constraints.Max)

	parts := make([]recorded, len(items))
	shown := make([]bool, len(items)) // false for an item with no room
	short := false                    // an item that draws had no room
	used, drawn := 0, 0
	for i, it := range items {
		if it.GetWeight() > 0 {
			continue
		}
		// It has what the natural items before it and their gaps left;
		// the weighted ones share whatever remains after.
		room := main(gtx.Constraints.Max) - used - gap*drawn
		if room <= 0 {
			v.hidden(it.GetWidget())
			short = v.left(it.GetWidget()) || short
			continue
		}
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Max: pt(room, crossMax)}
		parts[i] = record(cgtx, func(gtx layout.Context) layout.Dimensions {
			defer v.setNarrow(v.narrow || horizontal)()
			return v.widget(gtx, th, it.GetWidget())
		})
		if main(parts[i].dims.Size) == 0 {
			continue // nothing, such as an unset widget: no gap or divider either
		}
		shown[i] = true
		used += main(parts[i].dims.Size)
		drawn++
	}
	// The weighted items share what the others and the gaps between the
	// items that show leave. One whose share rounds to nothing does not
	// show, so the shares are worked out again without it and its gap.
	shares := make([]int, len(items)) // -1 for one left out
	divide := func() (dropped bool) {
		n, left := 0, 0
		for i, it := range items {
			if shown[i] || it.GetWeight() > 0 && shares[i] >= 0 {
				n++
			}
			if it.GetWeight() > 0 && shares[i] >= 0 {
				left += int(it.GetWeight())
			}
		}
		rest := max(main(gtx.Constraints.Max)-used-gap*max(n-1, 0), 0)
		for i, it := range items {
			w := int(it.GetWeight())
			if w == 0 || shares[i] < 0 {
				continue
			}
			shares[i] = rest * w / left
			rest, left = rest-shares[i], left-w
			if shares[i] == 0 {
				shares[i], dropped = -1, true
			}
		}
		return dropped
	}
	if divide() {
		divide()
	}
	for i, it := range items {
		if it.GetWeight() == 0 {
			continue
		}
		share := shares[i]
		if share <= 0 {
			v.hidden(it.GetWidget())
			short = v.left(it.GetWidget()) || short
			continue
		}
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Min: pt(share, 0), Max: pt(share, crossMax)}
		var child layout.Dimensions
		parts[i] = record(cgtx, func(gtx layout.Context) layout.Dimensions {
			defer v.setNarrow(v.narrow && !horizontal)()
			child = v.widget(gtx, th, it.GetWidget())
			return child
		})
		// It takes its whole share along the axis, and its own extent
		// across it.
		parts[i].dims.Size = pt(share, cross(child.Size))
		shown[i] = true
	}

	size, at := 0, 0
	for _, p := range parts {
		size = max(size, cross(p.dims.Size))
	}
	var dividers []int
	first := true
	for i, p := range parts {
		// An item with no room takes no gap or divider either.
		if !shown[i] {
			continue
		}
		if !first {
			dividers = append(dividers, at+(gap-line)/2)
			at += gap
		}
		first = false
		// A child that draws past its share, as a gauge at its least
		// size can, is cut off there rather than over its neighbours.
		cl := clip.Rect{Min: pt(at, 0), Max: pt(at+main(p.dims.Size), size)}.Push(gtx.Ops)
		p.at(gtx, pt(at, 0))
		cl.Pop()
		at += main(p.dims.Size)
	}
	if s.GetDividers() {
		for _, d := range dividers {
			fillRect(gtx, image.Rectangle{Min: pt(d, 0), Max: pt(d+line, size)}, th.Border)
		}
	}
	dims := layout.Dimensions{Size: pt(min(at, main(gtx.Constraints.Max)), min(size, crossMax))}
	if short { // the stack's end shows that items after it are cut
		fadeOut(gtx, th, image.Rectangle{Max: dims.Size}, horizontal)
	}
	return dims
}

// left notes w, a stack item with no room, as a cut, and reports it,
// unless it would draw nothing anyway.
func (v *viewState) left(w *tesserav1.Widget) bool {
	if !v.loading && blank(w) { // a loading one draws its placeholder
		return false
	}
	v.cut = true
	return true
}

// blank reports whether w draws nothing: it is unset, an empty text, or
// a stack of such widgets. A loading one draws its placeholder.
func blank(w *tesserav1.Widget) bool {
	if w.GetLoading() {
		return false
	}
	switch k := w.GetKind().(type) {
	case nil:
		return true
	case *tesserav1.Widget_Text:
		return k.Text.GetValue() == ""
	case *tesserav1.Widget_Stack:
		for _, it := range k.Stack.GetItems() {
			if !blank(it.GetWidget()) {
				return false
			}
		}
		return true
	}
	return false
}

// hidden keeps the state of a widget with no room to draw in, without
// drawing it: its tables and lists keep their scroll positions and
// report that none of their rows is visible.
func (v *viewState) hidden(w *tesserav1.Widget) {
	switch k := w.GetKind().(type) {
	case *tesserav1.Widget_Table, *tesserav1.Widget_List:
		v.report(w, v.scrollList(w).Position.First, 0)
	case *tesserav1.Widget_Stack:
		for _, it := range k.Stack.GetItems() {
			v.hidden(it.GetWidget())
		}
	}
}

// skeleton is a loading widget's placeholder, in its shape: bars for
// text and stats, the bare track for a gauge, a dashed band for a
// sparkline, and placeholder rows for tables and lists. It is static.
func (v *viewState) skeleton(gtx layout.Context, th *theme.Theme, w *tesserav1.Widget) layout.Dimensions {
	width := gtx.Constraints.Max.X
	bar := func(y, w, h int) {
		paint := clip.UniformRRect(image.Rect(0, y, w, y+h), gtx.Dp(2))
		fillShape(gtx, th.Border, paint)
	}
	switch k := w.GetKind().(type) {
	case *tesserav1.Widget_Text:
		// As wide as the bar, not the space, so siblings in a row
		// keep theirs.
		h := gtx.Sp(textSize(k.Text.GetSize()))
		bar(0, width*3/5, h)
		return layout.Dimensions{Size: image.Pt(width*3/5, h)}
	case *tesserav1.Widget_Stat:
		// The label and the value, at the sizes a stat draws them.
		value := theme.TextValue
		if widthDp(gtx) < compactStat {
			value = theme.TextStat
		}
		lh, vh := gtx.Sp(theme.TextMin), gtx.Sp(value)
		bar(0, width/3, lh)
		top := lh + gtx.Dp(6)
		bar(top, width*42/100, vh)
		return layout.Dimensions{Size: image.Pt(width*42/100, top+vh)}
	case *tesserav1.Widget_Gauge:
		return v.gaugeTrack(gtx, th, k.Gauge)
	case *tesserav1.Widget_Sparkline:
		h, on, off := gtx.Dp(34), gtx.Dp(8), gtx.Dp(5)
		faint := th.Border
		faint.A = uint8(int(faint.A) * 55 / 100)
		for x := 0; x < width; x += on + off {
			fillRect(gtx, image.Rect(x, 0, min(x+on, width), h), faint)
		}
		return layout.Dimensions{Size: image.Pt(width, h)}
	case *tesserav1.Widget_Table, *tesserav1.Widget_List:
		y := 0
		for i := range 3 {
			if i > 0 {
				y += gtx.Dp(theme.Space16)
			}
			bar(y, width*(9-2*i)/10, gtx.Dp(8))
			y += gtx.Dp(8)
		}
		return layout.Dimensions{Size: image.Pt(width, y)}
	}
	return layout.Dimensions{}
}

// fillShape paints the area of r.
func fillShape(gtx layout.Context, c color.NRGBA, r clip.RRect) {
	defer r.Push(gtx.Ops).Pop()
	fillRect(gtx, r.Rect, c)
}
