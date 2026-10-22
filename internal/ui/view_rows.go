// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"strings"

	"gioui.org/f32"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// Table and list metrics, as the mockup's.
const (
	tableHeadH unit.Dp = 30
	tableRowH  unit.Dp = 43
	cellGap    unit.Dp = 12 // between table columns
	itemPadY   unit.Dp = 12 // above and below a list item
	leadingW   unit.Dp = 40 // the least width of a list item's leading text
	leadingGap unit.Dp = 13
)

// scrollList is the list state of w, kept by its id, or by its place
// among the unnamed tables and lists.
func (v *viewState) scrollList(w *tesserav1.Widget) *layout.List {
	k := v.keys[w]
	v.used[k] = true
	l := v.lists[k]
	if l == nil {
		l = &layout.List{Axis: layout.Vertical}
		v.lists[k] = l
	}
	return l
}

// scrolled reports the visible range of w, laid out with l, to the
// plugin when it has an id and the range changed since it last did.
func (v *viewState) scrolled(w *tesserav1.Widget, l *layout.List) {
	id := v.id(w)
	if id == "" {
		return
	}
	r := [2]int{l.Position.First, l.Position.Count}
	if last, ok := v.sent[v.keys[w]]; ok && last == r {
		return
	}
	v.sent[v.keys[w]] = r
	v.events = append(v.events, &tesserav1.HandleEventRequest{Event: &tesserav1.HandleEventRequest_Scroll{
		Scroll: &tesserav1.ScrollEvent{WidgetId: id, FirstVisible: uint32(r[0]), VisibleCount: uint32(r[1])},
	}})
}

// clickable is the clickable of the row or item with action id.
func (v *viewState) clickable(id string) *widget.Clickable {
	v.used["row:"+id] = true
	c := v.rows[id]
	if c == nil {
		c = new(widget.Clickable)
		v.rows[id] = c
	}
	return c
}

// actionable lays out a row or item: with an action id, as a button
// that is highlighted when hovered, focused, or last activated, and
// sends the plugin an ActivateEvent when clicked or entered. draw gets
// whether to highlight.
func (v *viewState) actionable(gtx layout.Context, th *theme.Theme, id, text string, draw func(gtx layout.Context, lit bool) layout.Dimensions) layout.Dimensions {
	if id == "" {
		return draw(gtx, false)
	}
	c := v.clickable(id)
	for c.Clicked(gtx) {
		v.selected = id
		v.events = append(v.events, &tesserav1.HandleEventRequest{Event: &tesserav1.HandleEventRequest_Activate{
			Activate: &tesserav1.ActivateEvent{ActionId: id},
		}})
	}
	focused := gtx.Focused(c)
	dims := c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.Button.Add(gtx.Ops)
		semantic.LabelOp(text).Add(gtx.Ops)
		semantic.SelectedOp(v.selected == id).Add(gtx.Ops)
		return draw(gtx, c.Hovered() || focused || v.selected == id)
	})
	if focused {
		ring := gtx.Dp(2)
		strokeRRect(gtx, image.Rectangle{Max: dims.Size}, gtx.Dp(theme.RadiusSmall), ring, th.Accent)
	}
	return dims
}

// columnBounds splits width among cols by weight, zero counting as 1,
// with cellGap between columns. Each entry is a column's start and end.
func columnBounds(gtx layout.Context, cols []*tesserav1.TableColumn, width int) [][2]int {
	if len(cols) == 0 {
		return nil
	}
	gap := gtx.Dp(cellGap)
	total := 0
	for _, c := range cols {
		total += max(int(c.GetWeight()), 1)
	}
	avail := max(width-gap*(len(cols)-1), 0)
	bounds := make([][2]int, len(cols))
	x, left := 0, total
	for i, c := range cols {
		w := max(int(c.GetWeight()), 1)
		share := avail * w / left
		avail, left = avail-share, left-w
		bounds[i] = [2]int{x, x + share}
		x += share + gap
	}
	return bounds
}

// table draws the header, with the sort indicator, and the rows in a
// list that scrolls when they do not fit.
func (v *viewState) table(gtx layout.Context, th *theme.Theme, w *tesserav1.Widget, t *tesserav1.Table) layout.Dimensions {
	width := gtx.Constraints.Max.X
	cols := t.GetColumns()
	bounds := columnBounds(gtx, cols, width)
	line := gtx.Dp(1)

	// The header.
	sorted := -1
	if t.GetSortOrder() != tesserav1.SortOrder_SORT_ORDER_UNSPECIFIED && int(t.GetSortColumn()) < len(cols) {
		sorted = int(t.GetSortColumn())
	}
	tgtx := gtx
	tgtx.Constraints.Min = image.Point{}
	headH := gtx.Dp(tableHeadH)
	var titles []recorded
	for i, c := range cols {
		cgtx := tgtx
		cgtx.Constraints.Max.X = bounds[i][1] - bounds[i][0]
		if i == sorted {
			cgtx.Constraints.Max.X = max(cgtx.Constraints.Max.X-gtx.Dp(theme.Space12), 0)
		}
		r := recordLabel(cgtx, th, c.GetTitle(), theme.TextMin, th.Muted, theme.Mono)
		titles = append(titles, r)
		headH = max(headH, r.dims.Size.Y+gtx.Dp(theme.Space8))
	}
	for i, r := range titles {
		x := bounds[i][0]
		end := cols[i].GetAlign() == tesserav1.Align_ALIGN_END
		arrow := gtx.Dp(theme.Space12)
		if end {
			x = bounds[i][1] - r.dims.Size.X
			if i == sorted {
				x -= arrow
			}
		}
		r.at(gtx, image.Pt(x, (headH-r.dims.Size.Y)/2))
		if i == sorted {
			sortArrow(gtx, th, image.Pt(x+r.dims.Size.X+arrow/2, headH/2), t.GetSortOrder() == tesserav1.SortOrder_SORT_ORDER_ASCENDING)
		}
	}
	if len(cols) > 0 {
		fillRect(gtx, image.Rect(0, headH-line, width, headH), th.Border)
	}

	rows := t.GetRows()
	if len(rows) == 0 {
		if t.GetEmptyText() == "" {
			return layout.Dimensions{Size: image.Pt(width, headH)}
		}
		e := record(tgtx, wrapped(th, t.GetEmptyText(), theme.TextSmall, th.Muted, theme.Body).Layout)
		e.at(gtx, image.Pt(0, headH+gtx.Dp(theme.Space12)))
		return layout.Dimensions{Size: image.Pt(width, headH+gtx.Dp(theme.Space12)+e.dims.Size.Y)}
	}

	l := v.scrollList(w)
	lgtx := gtx
	lgtx.Constraints = layout.Constraints{Max: image.Pt(width, max(gtx.Constraints.Max.Y-headH, 0))}
	st := clip.Rect{Min: image.Pt(0, headH), Max: image.Pt(width, gtx.Constraints.Max.Y)}.Push(gtx.Ops)
	off := op.Offset(image.Pt(0, headH)).Push(gtx.Ops)
	dims := l.Layout(lgtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		return v.tableRow(gtx, th, rows[i], cols, bounds)
	})
	off.Pop()
	st.Pop()
	v.scrolled(w, l)
	return layout.Dimensions{Size: image.Pt(width, headH+dims.Size.Y)}
}

// sortArrow draws a small triangle centred on c, pointing up for an
// ascending sort and down for a descending one.
func sortArrow(gtx layout.Context, th *theme.Theme, c image.Point, up bool) {
	h := float32(gtx.Dp(theme.Space4))
	cx, cy := float32(c.X), float32(c.Y)
	dir := float32(1)
	if up {
		dir = -1
	}
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(cx-h, cy-dir*h/2))
	p.LineTo(f32.Pt(cx+h, cy-dir*h/2))
	p.LineTo(f32.Pt(cx, cy+dir*h/2))
	p.Close()
	paint.FillShape(gtx.Ops, th.Muted, clip.Outline{Path: p.End()}.Op())
}

// tableRow draws one row: a cell per column, cut to its width, and a
// border under it.
func (v *viewState) tableRow(gtx layout.Context, th *theme.Theme, r *tesserav1.TableRow, cols []*tesserav1.TableColumn, bounds [][2]int) layout.Dimensions {
	width := gtx.Constraints.Max.X
	cells := r.GetCells()
	id := v.action(r.GetActionId(), r)
	return v.actionable(gtx, th, id, strings.Join(cells, ", "), func(gtx layout.Context, lit bool) layout.Dimensions {
		tgtx := gtx
		tgtx.Constraints.Min = image.Point{}
		var texts []recorded
		h := gtx.Dp(tableRowH)
		for i := range cols {
			if i >= len(cells) {
				break
			}
			cgtx := tgtx
			cgtx.Constraints.Max.X = bounds[i][1] - bounds[i][0]
			var r recorded
			if cols[i].GetAlign() == tesserav1.Align_ALIGN_END {
				r = record(cgtx, func(gtx layout.Context) layout.Dimensions {
					return v.tabular(gtx, th, cells[i], theme.TextSmall, th.Fg, theme.Body)
				})
			} else {
				r = recordLabel(cgtx, th, cells[i], theme.TextSmall, th.Fg, theme.Body)
			}
			texts = append(texts, r)
			h = max(h, r.dims.Size.Y+gtx.Dp(theme.Space16))
		}
		if lit {
			fillRect(gtx, image.Rect(0, 0, width, h), th.Hover)
		}
		for i, t := range texts {
			x := bounds[i][0]
			if cols[i].GetAlign() == tesserav1.Align_ALIGN_END {
				x = bounds[i][1] - t.dims.Size.X
			}
			t.at(gtx, image.Pt(x, (h-t.dims.Size.Y)/2))
		}
		fillRect(gtx, image.Rect(0, h-gtx.Dp(1), width, h), th.Border)
		return layout.Dimensions{Size: image.Pt(width, h)}
	})
}

// list draws the items in a list that scrolls when they do not fit.
func (v *viewState) list(gtx layout.Context, th *theme.Theme, w *tesserav1.Widget, li *tesserav1.List) layout.Dimensions {
	width := gtx.Constraints.Max.X
	items := li.GetItems()
	if len(items) == 0 {
		if li.GetEmptyText() == "" {
			return layout.Dimensions{}
		}
		tgtx := gtx
		tgtx.Constraints.Min = image.Point{}
		return wrapped(th, li.GetEmptyText(), theme.TextMin, th.Muted, theme.Body).Layout(tgtx)
	}
	l := v.scrollList(w)
	lgtx := gtx
	lgtx.Constraints = layout.Constraints{Max: gtx.Constraints.Max}
	dims := l.Layout(lgtx, len(items), func(gtx layout.Context, i int) layout.Dimensions {
		return v.listItem(gtx, th, items[i], width)
	})
	v.scrolled(w, l)
	return layout.Dimensions{Size: image.Pt(width, dims.Size.Y)}
}

// listItem draws one item: a border above, the leading text, the title
// with the detail under it, and a chevron if it can be activated.
func (v *viewState) listItem(gtx layout.Context, th *theme.Theme, it *tesserav1.ListItem, width int) layout.Dimensions {
	id := v.action(it.GetActionId(), it)
	text := strings.Join(nonEmpty(it.GetLeading(), it.GetTitle(), it.GetDetail()), ", ")
	return v.actionable(gtx, th, id, text, func(gtx layout.Context, lit bool) layout.Dimensions {
		tgtx := gtx
		tgtx.Constraints.Min = image.Point{}
		pad := gtx.Dp(itemPadY)
		x := 0
		var lead recorded
		if it.GetLeading() != "" {
			lead = recordLabel(tgtx, th, it.GetLeading(), theme.TextMin, th.Muted, theme.Mono)
			x = max(gtx.Dp(leadingW), lead.dims.Size.X) + gtx.Dp(leadingGap)
		}
		var chevron recorded
		end := width
		if id != "" {
			chevron = recordLabel(tgtx, th, "›", theme.TextBody, th.Muted, theme.Body)
			end -= chevron.dims.Size.X + gtx.Dp(theme.Space8)
		}
		cgtx := tgtx
		cgtx.Constraints.Max.X = max(end-x, 0)
		var parts []recorded
		var gaps []int
		if it.GetTitle() != "" {
			parts = append(parts, record(cgtx, wrapped(th, it.GetTitle(), theme.TextSmall, th.Fg, theme.Body).Layout))
		}
		if it.GetDetail() != "" {
			if len(parts) > 0 {
				gaps = append(gaps, gtx.Dp(theme.Space4))
			}
			parts = append(parts, record(cgtx, wrapped(th, it.GetDetail(), theme.TextMin, th.Muted, theme.Body).Layout))
		}
		body := record(cgtx, func(gtx layout.Context) layout.Dimensions { return column(gtx, parts, gaps) })
		h := 2*pad + max(body.dims.Size.Y, lead.dims.Size.Y, chevron.dims.Size.Y)
		if lit {
			fillRect(gtx, image.Rect(0, 0, width, h), th.Hover)
		}
		fillRect(gtx, image.Rect(0, 0, width, gtx.Dp(1)), th.Border)
		lead.at(gtx, image.Pt(0, pad))
		body.at(gtx, image.Pt(x, pad))
		if id != "" {
			chevron.at(gtx, image.Pt(width-chevron.dims.Size.X-gtx.Dp(theme.Space4), (h-chevron.dims.Size.Y)/2))
		}
		return layout.Dimensions{Size: image.Pt(width, h)}
	})
}

func nonEmpty(s ...string) []string {
	var out []string
	for _, x := range s {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}
