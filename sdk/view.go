// SPDX-License-Identifier: GPL-3.0-or-later

package sdk

import (
	"math"
	"slices"

	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// View is everything a panel shows: a title for the tile header and a
// body widget. A nil Body is an empty panel.
type View struct {
	Title string
	Body  Widget
}

// Proto returns the view as the protocol message the host receives.
func (v View) Proto() *tesserav1.View { return v.proto() }

func (v View) proto() *tesserav1.View {
	return &tesserav1.View{Title: v.Title, Body: protoOf(v.Body)}
}

// Widget is one node of a view; nil, or a nil builder, is none. Build one with [Text], [Stat], [Gauge],
// [Sparkline], [Table], [List], [Row], [Column], or [Raw]. The builders'
// methods change the widget and return it, so calls chain:
//
//	sdk.Stat("CPU", "24.8").Unit("%").Caption("4 cores")
type Widget interface {
	widget() *tesserav1.Widget
}

func protoOf(w Widget) *tesserav1.Widget {
	if w == nil {
		return nil
	}
	return w.widget()
}

// Raw wraps a widget built from the protocol messages directly, for a
// field the builders do not cover.
func Raw(w *tesserav1.Widget) Widget { return raw{w} }

type raw struct{ w *tesserav1.Widget }

func (r raw) widget() *tesserav1.Widget { return r.w }

// Tone colors text by meaning, in the host's theme.
type Tone = tesserav1.Tone

// The tones.
const (
	ToneDefault = tesserav1.Tone_TONE_UNSPECIFIED
	ToneMuted   = tesserav1.Tone_TONE_MUTED
	ToneAccent  = tesserav1.Tone_TONE_ACCENT
	ToneOK      = tesserav1.Tone_TONE_OK
	ToneWarn    = tesserav1.Tone_TONE_WARN
	ToneError   = tesserav1.Tone_TONE_ERROR
)

// TextSize is a step on the host's type scale.
type TextSize = tesserav1.TextSize

// The text sizes.
const (
	SizeBody    = tesserav1.TextSize_TEXT_SIZE_UNSPECIFIED
	SizeSmall   = tesserav1.TextSize_TEXT_SIZE_SMALL
	SizeLarge   = tesserav1.TextSize_TEXT_SIZE_LARGE
	SizeDisplay = tesserav1.TextSize_TEXT_SIZE_DISPLAY
)

// Spacing is a step on the host's spacing scale.
type Spacing = tesserav1.Spacing

// The spacing steps; SpacingMedium is a stack's default gap.
const (
	SpacingMedium     = tesserav1.Spacing_SPACING_UNSPECIFIED
	SpacingNone       = tesserav1.Spacing_SPACING_NONE
	SpacingExtraSmall = tesserav1.Spacing_SPACING_EXTRA_SMALL
	SpacingSmall      = tesserav1.Spacing_SPACING_SMALL
	SpacingLarge      = tesserav1.Spacing_SPACING_LARGE
	SpacingExtraLarge = tesserav1.Spacing_SPACING_EXTRA_LARGE
)

// SortOrder is the sort indicator on a table column.
type SortOrder = tesserav1.SortOrder

// The sort orders.
const (
	SortNone       = tesserav1.SortOrder_SORT_ORDER_UNSPECIFIED
	SortAscending  = tesserav1.SortOrder_SORT_ORDER_ASCENDING
	SortDescending = tesserav1.SortOrder_SORT_ORDER_DESCENDING
)

// TextWidget is a run of text, which wraps to the available width.
type TextWidget struct {
	w *tesserav1.Widget
	t *tesserav1.Text
}

// Text returns a run of text in the body size and default tone.
func Text(value string) *TextWidget {
	t := &tesserav1.Text{Value: value}
	return &TextWidget{&tesserav1.Widget{Kind: &tesserav1.Widget_Text{Text: t}}, t}
}

// Size sets the step on the type scale.
func (b *TextWidget) Size(s TextSize) *TextWidget { b.t.Size = s; return b }

// Tone sets the color by meaning.
func (b *TextWidget) Tone(t Tone) *TextWidget { b.t.Tone = t; return b }

// Monospace uses the monospace font.
func (b *TextWidget) Monospace() *TextWidget { b.t.Monospace = true; return b }

// ID sets the widget's ID, unique within the view.
func (b *TextWidget) ID(id string) *TextWidget { b.w.Id = id; return b }

// Loading draws a placeholder in the widget's shape instead.
func (b *TextWidget) Loading() *TextWidget { b.w.Loading = true; return b }

func (b *TextWidget) widget() *tesserav1.Widget {
	if b == nil {
		return nil
	}
	return b.w
}

// StatWidget is a headline number with its unit and context.
type StatWidget struct {
	w *tesserav1.Widget
	s *tesserav1.Stat
}

// Stat returns a headline number: value is already formatted, such as
// "24.8"; empty shows as missing.
func Stat(label, value string) *StatWidget {
	s := &tesserav1.Stat{Label: label, Value: value}
	return &StatWidget{&tesserav1.Widget{Kind: &tesserav1.Widget_Stat{Stat: s}}, s}
}

// Unit sets the unit after the value, such as "%".
func (b *StatWidget) Unit(u string) *StatWidget { b.s.Unit = u; return b }

// Caption sets the context line below the value.
func (b *StatWidget) Caption(c string) *StatWidget { b.s.Caption = c; return b }

// CaptionTone sets the caption's color; the default is muted.
func (b *StatWidget) CaptionTone(t Tone) *StatWidget { b.s.CaptionTone = t; return b }

// ID sets the widget's ID, unique within the view.
func (b *StatWidget) ID(id string) *StatWidget { b.w.Id = id; return b }

// Loading draws a placeholder in the widget's shape instead.
func (b *StatWidget) Loading() *StatWidget { b.w.Loading = true; return b }

func (b *StatWidget) widget() *tesserav1.Widget {
	if b == nil {
		return nil
	}
	return b.w
}

// GaugeWidget is a value on a 260° arc.
type GaugeWidget struct {
	w *tesserav1.Widget
	g *tesserav1.Gauge
}

// Gauge returns a value on an arc from 0 to 100.
func Gauge(label string, value float64) *GaugeWidget {
	g := &tesserav1.Gauge{Label: label, Value: value}
	return &GaugeWidget{&tesserav1.Widget{Kind: &tesserav1.Widget_Gauge{Gauge: g}}, g}
}

// Range sets the arc's bounds; hi must be greater than lo.
func (b *GaugeWidget) Range(lo, hi float64) *GaugeWidget {
	b.g.Min, b.g.Max = lo, &hi
	return b
}

// Unit sets the unit after the value, such as "%".
func (b *GaugeWidget) Unit(u string) *GaugeWidget { b.g.Unit = u; return b }

// Display sets the value as shown in the middle of the arc; by default
// the host formats it.
func (b *GaugeWidget) Display(d string) *GaugeWidget { b.g.Display = d; return b }

// WarnAt colors the arc as a warning from this value up.
func (b *GaugeWidget) WarnAt(v float64) *GaugeWidget { b.g.WarnAt = &v; return b }

// ErrorAt colors the arc as an error from this value up, before WarnAt.
func (b *GaugeWidget) ErrorAt(v float64) *GaugeWidget { b.g.ErrorAt = &v; return b }

// Caption sets the context line below the gauge.
func (b *GaugeWidget) Caption(c string) *GaugeWidget { b.g.Caption = c; return b }

// ID sets the widget's ID, unique within the view.
func (b *GaugeWidget) ID(id string) *GaugeWidget { b.w.Id = id; return b }

// Loading draws a placeholder in the widget's shape instead.
func (b *GaugeWidget) Loading() *GaugeWidget { b.w.Loading = true; return b }

func (b *GaugeWidget) widget() *tesserav1.Widget {
	if b == nil {
		return nil
	}
	return b.w
}

// SparklineWidget is a small line chart of recent samples.
type SparklineWidget struct {
	w *tesserav1.Widget
	s *tesserav1.Sparkline
}

// Sparkline returns a line chart of samples, oldest first and evenly
// spaced in time. A NaN or infinite sample is a gap in the line. The y
// axis spans the samples unless Range sets it. It keeps a copy of
// samples, so the caller may reuse the slice.
func Sparkline(label string, samples ...float64) *SparklineWidget {
	s := &tesserav1.Sparkline{Label: label, Samples: slices.Clone(samples)}
	return &SparklineWidget{&tesserav1.Widget{Kind: &tesserav1.Widget_Sparkline{Sparkline: s}}, s}
}

// Range sets the bottom and top of the y axis.
func (b *SparklineWidget) Range(lo, hi float64) *SparklineWidget {
	b.s.Min, b.s.Max = &lo, &hi
	return b
}

// Unit sets the samples' unit, for accessibility text.
func (b *SparklineWidget) Unit(u string) *SparklineWidget { b.s.Unit = u; return b }

// Labels sets the captions at the left and right ends of the x axis,
// such as "60s ago" and "now".
func (b *SparklineWidget) Labels(start, end string) *SparklineWidget {
	b.s.StartLabel, b.s.EndLabel = start, end
	return b
}

// Tone sets the color of the line and fill.
func (b *SparklineWidget) Tone(t Tone) *SparklineWidget { b.s.Tone = t; return b }

// ID sets the widget's ID, unique within the view.
func (b *SparklineWidget) ID(id string) *SparklineWidget { b.w.Id = id; return b }

// Loading draws a placeholder in the widget's shape instead.
func (b *SparklineWidget) Loading() *SparklineWidget { b.w.Loading = true; return b }

func (b *SparklineWidget) widget() *tesserav1.Widget {
	if b == nil {
		return nil
	}
	return b.w
}

// TableWidget shows rows and columns.
type TableWidget struct {
	w *tesserav1.Widget
	t *tesserav1.Table
}

// Table returns a table with the columns, at most 64, and no rows.
func Table(columns ...*TableColumnSpec) *TableWidget {
	t := &tesserav1.Table{}
	for _, c := range columns {
		if c != nil {
			t.Columns = append(t.Columns, c.c)
		}
	}
	return &TableWidget{&tesserav1.Widget{Kind: &tesserav1.Widget_Table{Table: t}}, t}
}

// Row adds a row with one cell per column. It keeps a copy of cells.
func (b *TableWidget) Row(cells ...string) *TableWidget {
	b.t.Rows = append(b.t.Rows, &tesserav1.TableRow{Cells: slices.Clone(cells)})
	return b
}

// ActionRow adds a row the user can activate, which calls
// [Plugin.Activate] with actionID. Action IDs are unique within a view.
func (b *TableWidget) ActionRow(actionID string, cells ...string) *TableWidget {
	b.t.Rows = append(b.t.Rows, &tesserav1.TableRow{Cells: slices.Clone(cells), ActionId: actionID})
	return b
}

// Sort shows the sort indicator on a column, counted from 0; the rows
// are sorted by the plugin. A column that does not exist, negative or
// past the last, shows none.
func (b *TableWidget) Sort(column int, order SortOrder) *TableWidget {
	if column < 0 {
		column = math.MaxInt
	}
	b.t.SortColumn, b.t.SortOrder = clampUint32(column), order
	return b
}

// Empty sets the text shown when there are no rows.
func (b *TableWidget) Empty(text string) *TableWidget { b.t.EmptyText = text; return b }

// ID sets the widget's ID, unique within the view. A table with an ID
// keeps its scroll position across views and calls [Plugin.Scroll].
func (b *TableWidget) ID(id string) *TableWidget { b.w.Id = id; return b }

// Loading draws a placeholder in the widget's shape instead.
func (b *TableWidget) Loading() *TableWidget { b.w.Loading = true; return b }

func (b *TableWidget) widget() *tesserav1.Widget {
	if b == nil {
		return nil
	}
	return b.w
}

// TableColumnSpec is one table column.
type TableColumnSpec struct {
	c *tesserav1.TableColumn
}

// TableColumn returns a left-aligned column of weight 1.
func TableColumn(title string) *TableColumnSpec {
	return &TableColumnSpec{&tesserav1.TableColumn{Title: title}}
}

// AlignEnd aligns the header and cells to the right, for numbers.
func (c *TableColumnSpec) AlignEnd() *TableColumnSpec {
	c.c.Align = tesserav1.Align_ALIGN_END
	return c
}

// Weight sets the column's share of the table width relative to the
// others.
func (c *TableColumnSpec) Weight(w int) *TableColumnSpec {
	c.c.Weight = clampUint32(w)
	return c
}

// ListWidget is a vertical list of items.
type ListWidget struct {
	w *tesserav1.Widget
	l *tesserav1.List
}

// List returns a list of the items.
func List(items ...*ListItemSpec) *ListWidget {
	l := &tesserav1.List{}
	b := &ListWidget{&tesserav1.Widget{Kind: &tesserav1.Widget_List{List: l}}, l}
	return b.Add(items...)
}

// Add appends items.
func (b *ListWidget) Add(items ...*ListItemSpec) *ListWidget {
	for _, it := range items {
		if it != nil {
			b.l.Items = append(b.l.Items, it.i)
		}
	}
	return b
}

// Empty sets the text shown when there are no items.
func (b *ListWidget) Empty(text string) *ListWidget { b.l.EmptyText = text; return b }

// ID sets the widget's ID, unique within the view. A list with an ID
// keeps its scroll position across views and calls [Plugin.Scroll].
func (b *ListWidget) ID(id string) *ListWidget { b.w.Id = id; return b }

// Loading draws a placeholder in the widget's shape instead.
func (b *ListWidget) Loading() *ListWidget { b.w.Loading = true; return b }

func (b *ListWidget) widget() *tesserav1.Widget {
	if b == nil {
		return nil
	}
	return b.w
}

// ListItemSpec is one list entry.
type ListItemSpec struct {
	i *tesserav1.ListItem
}

// Item returns a list entry with its main text.
func Item(title string) *ListItemSpec {
	return &ListItemSpec{&tesserav1.ListItem{Title: title}}
}

// Leading sets the short text before the title, such as a time.
func (it *ListItemSpec) Leading(s string) *ListItemSpec { it.i.Leading = s; return it }

// Detail sets the secondary text under the title.
func (it *ListItemSpec) Detail(s string) *ListItemSpec { it.i.Detail = s; return it }

// Action makes the item activatable: activating it calls
// [Plugin.Activate] with actionID. Action IDs are unique within a view.
func (it *ListItemSpec) Action(actionID string) *ListItemSpec { it.i.ActionId = actionID; return it }

// StackWidget lays out its children in a row or column.
type StackWidget struct {
	w *tesserav1.Widget
	s *tesserav1.Stack
}

// Row lays the children out left to right, and Column top to bottom.
// Each child takes its natural size unless wrapped in [Weighted]; a nil
// child, or [Space], is empty space.
func Row(children ...Widget) *StackWidget {
	return stack(tesserav1.Axis_AXIS_HORIZONTAL, children)
}

// Column lays the children out top to bottom; see [Row].
func Column(children ...Widget) *StackWidget {
	return stack(tesserav1.Axis_AXIS_UNSPECIFIED, children)
}

func stack(axis tesserav1.Axis, children []Widget) *StackWidget {
	s := &tesserav1.Stack{Axis: axis}
	b := &StackWidget{&tesserav1.Widget{Kind: &tesserav1.Widget_Stack{Stack: s}}, s}
	return b.Add(children...)
}

// Add appends children.
func (b *StackWidget) Add(children ...Widget) *StackWidget {
	for _, c := range children {
		item := &tesserav1.StackItem{Widget: protoOf(c)}
		if w, ok := c.(weighted); ok {
			item.Weight = w.weight
		}
		b.s.Items = append(b.s.Items, item)
	}
	return b
}

// Gap sets the space between children.
func (b *StackWidget) Gap(s Spacing) *StackWidget { b.s.Gap = s; return b }

// Dividers draws a line between children.
func (b *StackWidget) Dividers() *StackWidget { b.s.Dividers = true; return b }

// ID sets the widget's ID, unique within the view.
func (b *StackWidget) ID(id string) *StackWidget { b.w.Id = id; return b }

// Loading keeps the layout and draws each widget in it as a placeholder.
func (b *StackWidget) Loading() *StackWidget { b.w.Loading = true; return b }

func (b *StackWidget) widget() *tesserav1.Widget {
	if b == nil {
		return nil
	}
	return b.w
}

// Weighted gives a stack's child a share of the space left after the
// children at natural size, in proportion to weight, which is at least 1.
// Outside a stack the weight is ignored. Across a row, a sparkline,
// table, or list needs a weight to sit beside other children.
func Weighted(weight int, w Widget) Widget {
	return weighted{w, max(clampUint32(weight), 1)}
}

type weighted struct {
	w      Widget
	weight uint32
}

func (w weighted) widget() *tesserav1.Widget { return protoOf(w.w) }

// Space is empty space in a stack: nothing at natural size, or its
// share when [Weighted].
func Space() Widget { return nil }
