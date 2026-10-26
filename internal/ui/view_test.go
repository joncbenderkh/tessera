// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"image"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/text"
	"golang.org/x/image/math/fixed"
	"google.golang.org/protobuf/proto"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// viewFrame lays view out over size after queueing events, and returns
// the input for the plugin.
func viewFrame(r *input.Router, v *viewState, view *tesserav1.View, size image.Point, events ...event.Event) []*tesserav1.HandleEventRequest {
	for _, e := range events {
		r.Queue(e)
	}
	gtx := context(r, size, time.Now())
	out := v.Layout(gtx, testTheme, view)
	r.Frame(gtx.Ops)
	return out
}

// loose is a context for size that lets widgets take their natural size.
func loose(r *input.Router, size image.Point) layout.Context {
	gtx := context(r, size, time.Now())
	gtx.Constraints.Min = image.Point{}
	return gtx
}

func textW(s string) *tesserav1.Widget {
	return &tesserav1.Widget{Kind: &tesserav1.Widget_Text{Text: &tesserav1.Text{Value: s}}}
}

func listOf(id string, items ...*tesserav1.ListItem) *tesserav1.Widget {
	return &tesserav1.Widget{Id: id, Kind: &tesserav1.Widget_List{List: &tesserav1.List{Items: items}}}
}

func f(x float64) *float64 { return &x }

// every is one of each widget, with the variants that draw differently.
func every() []*tesserav1.Widget {
	loading := func(w *tesserav1.Widget) *tesserav1.Widget { w.Loading = true; return w }
	gauge := func(g *tesserav1.Gauge) *tesserav1.Widget {
		return &tesserav1.Widget{Kind: &tesserav1.Widget_Gauge{Gauge: g}}
	}
	spark := func(s *tesserav1.Sparkline) *tesserav1.Widget {
		return &tesserav1.Widget{Kind: &tesserav1.Widget_Sparkline{Sparkline: s}}
	}
	stat := func(s *tesserav1.Stat) *tesserav1.Widget {
		return &tesserav1.Widget{Kind: &tesserav1.Widget_Stat{Stat: s}}
	}
	table := &tesserav1.Table{
		Columns:    []*tesserav1.TableColumn{{Title: "Process", Weight: 3}, {Title: "CPU", Align: tesserav1.Align_ALIGN_END}},
		Rows:       []*tesserav1.TableRow{{Cells: []string{"go build", "12.6%"}, ActionId: "pid:1"}, {Cells: []string{"tessera"}}},
		SortColumn: 1, SortOrder: tesserav1.SortOrder_SORT_ORDER_DESCENDING,
	}
	return []*tesserav1.Widget{
		textW("Primary text"),
		{Kind: &tesserav1.Widget_Text{Text: &tesserav1.Text{Value: "Display", Size: tesserav1.TextSize_TEXT_SIZE_DISPLAY, Tone: tesserav1.Tone_TONE_ACCENT}}},
		stat(&tesserav1.Stat{Label: "CPU utilization", Value: "24.8", Unit: "%", Caption: "−2.1 pp · last sample"}),
		stat(&tesserav1.Stat{Label: "Missing"}),
		gauge(&tesserav1.Gauge{Label: "Disk", Value: 68, Unit: "%", WarnAt: f(80), ErrorAt: f(95), Caption: "320 GB free"}),
		gauge(&tesserav1.Gauge{Value: math.NaN()}),
		gauge(&tesserav1.Gauge{Value: 5, Min: 10, Max: f(10)}),
		spark(&tesserav1.Sparkline{Label: "Load", Samples: []float64{1, 3, math.NaN(), 2, 5, 4}, StartLabel: "60s ago", EndLabel: "now", Tone: tesserav1.Tone_TONE_ACCENT}),
		spark(&tesserav1.Sparkline{Samples: []float64{7}}),
		spark(&tesserav1.Sparkline{Label: "Empty"}),
		{Kind: &tesserav1.Widget_Table{Table: table}},
		{Kind: &tesserav1.Widget_Table{Table: &tesserav1.Table{Columns: table.Columns, EmptyText: "No processes"}}},
		listOf("", &tesserav1.ListItem{Leading: "10:00", Title: "Review", Detail: "Room 4", ActionId: "ev:1"}, &tesserav1.ListItem{Title: "Lunch"}),
		{Kind: &tesserav1.Widget_List{List: &tesserav1.List{EmptyText: "No upcoming events"}}},
		loading(textW("x")),
		loading(stat(&tesserav1.Stat{})),
		loading(gauge(&tesserav1.Gauge{})),
		loading(spark(&tesserav1.Sparkline{})),
		loading(&tesserav1.Widget{Kind: &tesserav1.Widget_Table{Table: table}}),
		loading(&tesserav1.Widget{}),
		{},
		{Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{
			Axis: tesserav1.Axis_AXIS_HORIZONTAL, Dividers: true,
			Items: []*tesserav1.StackItem{{Widget: textW("a"), Weight: 1}, {}, {Widget: textW("b"), Weight: 2}},
		}}},
	}
}

// Each widget stays within the width it gets, at narrow and wide sizes
// and large text, whatever its variant.
func TestViewWidgetsFit(t *testing.T) {
	for _, size := range []image.Point{{120, 200}, {360, 300}, {1200, 400}} {
		for _, scale := range []float32{1, 2} {
			for i, w := range every() {
				var r input.Router
				var v viewState
				gtx := loose(&r, size)
				gtx.Metric.PxPerSp = scale
				v.Layout(gtx, testTheme, &tesserav1.View{}) // sets the maps up
				v.index(w, new(counts))
				dims := v.widget(gtx, testTheme, w)
				if dims.Size.X > size.X {
					t.Errorf("%v at ×%v: widget %d is %d wide", size, scale, i, dims.Size.X)
				}
			}
		}
	}
}

func TestTextSizesAndTones(t *testing.T) {
	sizes := map[tesserav1.TextSize]float32{
		tesserav1.TextSize_TEXT_SIZE_UNSPECIFIED: 12, tesserav1.TextSize_TEXT_SIZE_SMALL: 11,
		tesserav1.TextSize_TEXT_SIZE_LARGE: 14, tesserav1.TextSize_TEXT_SIZE_DISPLAY: 36,
	}
	for s, want := range sizes {
		if got := float32(textSize(s)); got != want {
			t.Errorf("textSize(%v) = %v, want %v", s, got, want)
		}
	}
	th := testTheme
	tones := map[tesserav1.Tone]any{
		tesserav1.Tone_TONE_UNSPECIFIED: th.Fg, tesserav1.Tone_TONE_MUTED: th.Muted, tesserav1.Tone_TONE_ACCENT: th.Accent,
		tesserav1.Tone_TONE_OK: th.Ok, tesserav1.Tone_TONE_WARN: th.Warn, tesserav1.Tone_TONE_ERROR: th.Error,
	}
	for tone, want := range tones {
		if got := viewTone(th, tone, th.Fg); got != want {
			t.Errorf("viewTone(%v) = %v, want %v", tone, got, want)
		}
	}
}

func TestReadGauge(t *testing.T) {
	for _, tt := range []struct {
		name  string
		g     *tesserav1.Gauge
		tone  gaugeTone
		fill  float64
		value string
		ticks int
	}{
		{"normal", &tesserav1.Gauge{Value: 68, WarnAt: f(80), ErrorAt: f(95)}, gaugeNormal, 0.68, "68", 2},
		{"warn", &tesserav1.Gauge{Value: 88, WarnAt: f(80), ErrorAt: f(95)}, gaugeWarn, 0.88, "88", 2},
		{"error over warn", &tesserav1.Gauge{Value: 97, WarnAt: f(80), ErrorAt: f(95)}, gaugeError, 0.97, "97", 2},
		{"warn below min always", &tesserav1.Gauge{Value: 5, Min: 10, Max: f(20), WarnAt: f(0)}, gaugeWarn, 0, "10", 0},
		{"error above max never", &tesserav1.Gauge{Value: 30, ErrorAt: f(150)}, gaugeNormal, 0.3, "30", 0},
		{"NaN threshold unset", &tesserav1.Gauge{Value: 99, WarnAt: f(math.NaN())}, gaugeNormal, 0.99, "99", 0},
		{"clamped", &tesserav1.Gauge{Value: 150}, gaugeNormal, 1, "100", 0},
		{"display", &tesserav1.Gauge{Value: 0.5, Max: f(1), Display: "50"}, gaugeNormal, 0.5, "50", 0},
		{"formatted", &tesserav1.Gauge{Value: 12.345}, gaugeNormal, 0.12345, "12.3", 0},
		{"NaN value", &tesserav1.Gauge{Value: math.NaN()}, gaugeMissing, 0, "—", 0},
		{"infinite max", &tesserav1.Gauge{Value: 1, Max: f(math.Inf(1))}, gaugeMissing, 0, "—", 0},
		{"empty range", &tesserav1.Gauge{Value: 1, Min: 5, Max: f(5)}, gaugeMissing, 0, "—", 0},
	} {
		r := readGauge(tt.g)
		if r.tone != tt.tone || math.Abs(r.fraction-tt.fill) > 1e-9 || r.value[0] != tt.value || len(r.ticks) != tt.ticks {
			t.Errorf("%s: tone %v, fill %v, value %q, %d ticks; want %v, %v, %q, %d",
				tt.name, r.tone, r.fraction, r.value[0], len(r.ticks), tt.tone, tt.fill, tt.value, tt.ticks)
		}
	}
}

// Many samples a pixel column keep each column's highest and lowest
// point, in order, and still break at a gap.
func TestSparkPointsThin(t *testing.T) {
	samples := make([]float64, 100001)
	for i := range samples {
		samples[i] = float64(i % 2) // 0, 1, 0, 1, ...
	}
	samples[50000] = math.NaN()
	runs := sparkPoints(samples, 0, 1, 100, 40)
	if len(runs) != 2 {
		t.Fatalf("%d runs, want 2", len(runs))
	}
	for _, run := range runs {
		if len(run) > 2*102 {
			t.Errorf("%d points for 100 px", len(run))
		}
		for i := 0; i+1 < len(run); i += 2 {
			if run[i].Y == run[i+1].Y {
				t.Fatalf("column at x %v kept %v and %v, want its lowest and highest", run[i].X, run[i].Y, run[i+1].Y)
			}
		}
	}
}

func TestSparkPoints(t *testing.T) {
	runs := sparkPoints([]float64{0, 10, math.NaN(), 5, math.Inf(1), 20}, 0, 10, 100, 40)
	if len(runs) != 3 || len(runs[0]) != 2 || len(runs[1]) != 1 || len(runs[2]) != 1 {
		t.Fatalf("runs %v, want 2, 1 and 1 points", runs)
	}
	if runs[0][0] != f32.Pt(0, 40) || runs[0][1] != f32.Pt(20, 0) || runs[1][0] != f32.Pt(60, 20) {
		t.Errorf("points %v", runs)
	}
	if runs[2][0] != f32.Pt(100, 0) { // 20 is clamped to the top
		t.Errorf("clamped point %v", runs[2][0])
	}
	if flat := sparkPoints([]float64{3, 3}, 3, 3, 100, 40); flat[0][0].Y != 20 {
		t.Errorf("an empty range is drawn at %v, want mid-height", flat[0][0].Y)
	}
	if one := sparkPoints([]float64{3}, 3, 3, 100, 40); one[0][0] != f32.Pt(50, 20) {
		t.Errorf("a single sample at %v", one[0][0])
	}
	if _, _, ok := sparkRange(&tesserav1.Sparkline{Samples: []float64{math.NaN()}}); ok {
		t.Error("no finite sample has a range")
	}
	lo, hi, _ := sparkRange(&tesserav1.Sparkline{Samples: []float64{2, 8}, Min: f(0), Max: f(math.NaN())})
	if lo != 0 || hi != 8 {
		t.Errorf("range %v to %v, want 0 to 8", lo, hi)
	}
}

// Values with the same number of digits are the same width.
func TestTabularDigits(t *testing.T) {
	var r input.Router
	var v viewState
	gtx := loose(&r, image.Pt(400, 100))
	v.Layout(gtx, testTheme, &tesserav1.View{})
	width := func(s string) int {
		return v.tabular(gtx, testTheme, s, theme.TextValue, testTheme.Fg, theme.Body).Size.X
	}
	if a, b := width("11.1"), width("88.8"); a != b {
		t.Errorf("11.1 is %d wide, 88.8 is %d", a, b)
	}
	// The test fonts' digits are equal already, so widen the cell to
	// see each digit take one.
	v.digits[digitKey{theme.Body, theme.TextValue, gtx.Metric.PxPerSp, gtx.Locale}] = 50
	dot := recordLabel(gtx, testTheme, ".", theme.TextValue, testTheme.Fg, theme.Body).dims.Size.X
	if got := width("11.1"); got != 3*50+dot {
		t.Errorf("11.1 is %d wide, want three 50 px cells and a %d px point", got, dot)
	}
}

// A compact stat, in a narrow space, has a smaller value.
func TestStatCompact(t *testing.T) {
	s := &tesserav1.Widget{Kind: &tesserav1.Widget_Stat{Stat: &tesserav1.Stat{Value: "24.8", Unit: "%"}}}
	height := func(w int) int {
		var r input.Router
		var v viewState
		gtx := loose(&r, image.Pt(w, 200))
		v.Layout(gtx, testTheme, &tesserav1.View{})
		return v.widget(gtx, testTheme, s).Size.Y
	}
	if wide, narrow := height(300), height(150); narrow >= wide {
		t.Errorf("compact stat %d tall, full %d", narrow, wide)
	}
}

// A weighted item takes the space left; natural items keep their size.
func TestStackWeights(t *testing.T) {
	var r input.Router
	var v viewState
	gtx := loose(&r, image.Pt(300, 200))
	v.Layout(gtx, testTheme, &tesserav1.View{})
	row := &tesserav1.Stack{Axis: tesserav1.Axis_AXIS_HORIZONTAL, Items: []*tesserav1.StackItem{{Widget: textW("a")}, {Weight: 1}}}
	if got := v.stack(gtx, testTheme, row).Size.X; got != 300 {
		t.Errorf("weighted row is %d wide, want 300", got)
	}
	row.Items[1].Weight = 0
	if got := v.stack(gtx, testTheme, row).Size.X; got >= 100 {
		t.Errorf("natural row is %d wide", got)
	}
	col := &tesserav1.Stack{Gap: tesserav1.Spacing_SPACING_LARGE, Items: []*tesserav1.StackItem{{Widget: textW("a")}, {Widget: textW("b")}}}
	one := v.widget(gtx, testTheme, textW("a")).Size.Y
	if got := v.stack(gtx, testTheme, col).Size.Y; got != 2*one+16 {
		t.Errorf("column is %d tall, want %d", got, 2*one+16)
	}
}

func activated(events []*tesserav1.HandleEventRequest) []string {
	var ids []string
	for _, e := range events {
		if a := e.GetActivate(); a != nil {
			ids = append(ids, a.GetActionId())
		}
	}
	return ids
}

// Clicking an item with an action_id sends it; the item stays selected
// in the next view; an item repeating an action_id is inert.
func TestListActivate(t *testing.T) {
	view := &tesserav1.View{Body: listOf("",
		&tesserav1.ListItem{Title: "Review", ActionId: "ev:1"},
		&tesserav1.ListItem{Title: "Again", ActionId: "ev:1"},
		&tesserav1.ListItem{Title: "Plain"},
	)}
	var r input.Router
	var v viewState
	size := image.Pt(300, 400)
	viewFrame(&r, &v, view, size)
	itemH := 2*12 + v.widget(loose(&r, size), testTheme, textW("Review")).Size.Y
	at := func(i int) []event.Event { return click(image.Pt(100, i*itemH+itemH/2)) }
	if got := activated(viewFrame(&r, &v, view, size, at(0)...)); !slices.Equal(got, []string{"ev:1"}) {
		t.Fatalf("clicking the first item sent %v", got)
	}
	for _, i := range []int{1, 2} {
		if got := activated(viewFrame(&r, &v, view, size, at(i)...)); got != nil {
			t.Errorf("clicking item %d sent %v", i, got)
		}
	}
	next := proto.Clone(view).(*tesserav1.View)
	viewFrame(&r, &v, next, size)
	if v.selected != "ev:1" {
		t.Errorf("selected %q after a new view, want ev:1", v.selected)
	}
	// Enter on the focused item activates it too.
	gtx := context(&r, size, time.Now())
	gtx.Execute(key.FocusCmd{Tag: v.rows["ev:1"]})
	v.Layout(gtx, testTheme, next)
	r.Frame(gtx.Ops)
	enter := []event.Event{key.Event{Name: key.NameReturn, State: key.Press}, key.Event{Name: key.NameReturn, State: key.Release}}
	if got := activated(viewFrame(&r, &v, next, size, enter...)); !slices.Equal(got, []string{"ev:1"}) {
		t.Errorf("Enter sent %v", got)
	}
}

func scrolls(events []*tesserav1.HandleEventRequest) []*tesserav1.ScrollEvent {
	var out []*tesserav1.ScrollEvent
	for _, e := range events {
		if s := e.GetScroll(); s != nil {
			out = append(out, s)
		}
	}
	return out
}

// A table with an id that does not fit scrolls; each new visible range
// is sent once, and the position survives a new view.
func TestTableScrolls(t *testing.T) {
	table := &tesserav1.Table{Columns: []*tesserav1.TableColumn{{Title: "Name"}}}
	for i := range 50 {
		table.Rows = append(table.Rows, &tesserav1.TableRow{Cells: []string{fmt.Sprint("row ", i)}})
	}
	view := &tesserav1.View{Body: &tesserav1.Widget{Id: "procs", Kind: &tesserav1.Widget_Table{Table: table}}}
	var r input.Router
	var v viewState
	size := image.Pt(300, 300)
	first := scrolls(viewFrame(&r, &v, view, size))
	if len(first) != 1 || first[0].GetWidgetId() != "procs" || first[0].GetFirstVisible() != 0 || first[0].GetVisibleCount() == 0 {
		t.Fatalf("first frame sent %v", first)
	}
	if again := scrolls(viewFrame(&r, &v, view, size)); again != nil {
		t.Errorf("an unchanged range was sent again: %v", again)
	}
	wheel := pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(100, 150), Scroll: f32.Pt(0, 400)}
	moved := scrolls(viewFrame(&r, &v, view, size, wheel))
	if len(moved) != 1 || moved[0].GetFirstVisible() == 0 {
		t.Fatalf("scrolling sent %v", moved)
	}
	viewFrame(&r, &v, proto.Clone(view).(*tesserav1.View), size)
	if got := v.lists["id:procs"].Position.First; got != int(moved[0].GetFirstVisible()) {
		t.Errorf("after a new view the first row is %d, want %d", got, moved[0].GetFirstVisible())
	}
}

// A live tile draws its view, and input from it reaches the host as an
// event action.
func TestTileSendsViewEvents(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("cal", 0, 0, 1, 2)})
	states := live("cal")
	states[0].View = &tesserav1.View{Title: "Calendar", Body: listOf("", &tesserav1.ListItem{Title: "Review", ActionId: "ev:1"})}
	var r input.Router
	var g Grid
	now := time.Now()
	frame(&r, &g, cfg, states, now)
	rect := g.placed["cal"]
	head := g.tiles["cal"].headSize.Y
	got := frame(&r, &g, cfg, states, now, click(image.Pt(rect.Min.X+60, rect.Min.Y+head+20))...)
	want := []Action{{Kind: ActionEvent, Panel: "cal", Event: &tesserav1.HandleEventRequest{
		Event: &tesserav1.HandleEventRequest_Activate{Activate: &tesserav1.ActivateEvent{ActionId: "ev:1"}},
	}}}
	if len(got) != 1 || got[0].Kind != want[0].Kind || got[0].Panel != "cal" || !proto.Equal(got[0].Event, want[0].Event) {
		t.Errorf("actions %v, want %v", got, want)
	}
	// A view over a limit is gone; the tile says why rather than
	// drawing nothing.
	states[0].View, states[0].ViewErr = nil, fmt.Errorf("view over the size limit")
	frame(&r, &g, cfg, states, now)
	if !hasLabel(&r, "view over the size limit") {
		t.Error("the tile does not say why it has no view")
	}
}

// hasLabel reports whether the last frame drew a label with text.
func hasLabel(r *input.Router, text string) bool {
	for _, n := range r.AppendSemantics(nil) {
		if n.Desc.Label == text {
			return true
		}
	}
	return false
}

// A loading stack draws its children as placeholders, as view.proto
// says; a loading table or list keeps its scroll position for the view
// after it.
func TestViewLoading(t *testing.T) {
	var r input.Router
	var v viewState
	size := image.Pt(300, 300)
	stack := &tesserav1.View{Body: &tesserav1.Widget{Loading: true, Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{
		Items: []*tesserav1.StackItem{{Widget: textW("still here")}},
	}}}}
	viewFrame(&r, &v, stack, size)
	if hasLabel(&r, "still here") {
		t.Error("a loading stack drew its child's content")
	}
	gtx := loose(&r, size)
	if d := v.widget(gtx, testTheme, stack.Body); d.Size.Y == 0 {
		t.Error("a loading stack drew no placeholder")
	}

	table := &tesserav1.Table{Columns: []*tesserav1.TableColumn{{Title: "Name"}}}
	for i := range 50 {
		table.Rows = append(table.Rows, &tesserav1.TableRow{Cells: []string{fmt.Sprint("row ", i)}})
	}
	view := &tesserav1.View{Body: &tesserav1.Widget{Id: "procs", Kind: &tesserav1.Widget_Table{Table: table}}}
	viewFrame(&r, &v, view, size)
	viewFrame(&r, &v, view, size, pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(100, 150), Scroll: f32.Pt(0, 400)})
	first := v.lists["id:procs"].Position.First
	loading := proto.Clone(view).(*tesserav1.View)
	loading.Body.Loading = true
	viewFrame(&r, &v, loading, size)
	if got := scrolls(viewFrame(&r, &v, view, size)); got != nil || v.lists["id:procs"].Position.First != first || first == 0 {
		t.Errorf("after a loading view: first row %d, want %d; sent %v", v.lists["id:procs"].Position.First, first, got)
	}
}

// A plugin's id never shares state with an unnamed list's place.
func TestViewKeysApart(t *testing.T) {
	var r input.Router
	var v viewState
	viewFrame(&r, &v, &tesserav1.View{Body: column2(listOf("#1", &tesserav1.ListItem{Title: "a"}), listOf("", &tesserav1.ListItem{Title: "b"}))}, image.Pt(300, 300))
	if len(v.lists) != 2 {
		t.Errorf("%d list states for two lists: %v", len(v.lists), v.keys)
	}
}

func column2(a, b *tesserav1.Widget) *tesserav1.Widget {
	return &tesserav1.Widget{Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{Items: []*tesserav1.StackItem{{Widget: a}, {Widget: b}}}}}
}

// An empty list with an id reports that nothing is visible, once, and
// again when it empties after having items.
func TestEmptyListReportsRange(t *testing.T) {
	var r input.Router
	var v viewState
	size := image.Pt(300, 300)
	empty := &tesserav1.View{Body: listOf("events")}
	got := scrolls(viewFrame(&r, &v, empty, size))
	if len(got) != 1 || got[0].GetVisibleCount() != 0 {
		t.Fatalf("an empty list sent %v", got)
	}
	if got := scrolls(viewFrame(&r, &v, empty, size)); got != nil {
		t.Errorf("sent again: %v", got)
	}
	viewFrame(&r, &v, &tesserav1.View{Body: listOf("events", &tesserav1.ListItem{Title: "a"})}, size)
	if got := scrolls(viewFrame(&r, &v, empty, size)); len(got) != 1 || got[0].GetVisibleCount() != 0 {
		t.Errorf("emptying sent %v", got)
	}
}

// A table or list with no room for rows reports that none is visible.
func TestNoRoomReportsNothingVisible(t *testing.T) {
	table := &tesserav1.Widget{Id: "procs", Kind: &tesserav1.Widget_Table{Table: &tesserav1.Table{
		Columns: []*tesserav1.TableColumn{{Title: "Name"}},
		Rows:    []*tesserav1.TableRow{{Cells: []string{"a"}}},
	}}}
	for name, body := range map[string]*tesserav1.Widget{
		"list":  listOf("events", &tesserav1.ListItem{Title: "a"}),
		"table": table,
	} {
		// A line of text takes the little height there is; the weighted
		// widget gets none.
		stack := &tesserav1.Widget{Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{Items: []*tesserav1.StackItem{
			{Widget: &tesserav1.Widget{Kind: &tesserav1.Widget_Text{Text: &tesserav1.Text{Value: "head"}}}},
			{Widget: body, Weight: 1},
		}}}}
		var r input.Router
		var v viewState
		got := scrolls(viewFrame(&r, &v, &tesserav1.View{Body: stack}, image.Pt(300, 10)))
		if len(got) != 1 || got[0].GetVisibleCount() != 0 {
			t.Errorf("%s with no room sent %v", name, got)
		}
	}
}

// A value too wide for its space is cut off as a label is, not drawn
// with digits missing.
func TestTabularOverflow(t *testing.T) {
	var r input.Router
	var v viewState
	gtx := loose(&r, image.Pt(51, 100))
	v.Layout(gtx, testTheme, &tesserav1.View{})
	got := v.tabular(gtx, testTheme, "12345678", theme.TextStat, testTheme.Fg, theme.Body)
	want := label(testTheme, "12345678", theme.TextStat, testTheme.Fg, theme.Body).Layout(gtx)
	if got.Size != want.Size {
		t.Errorf("overflowing value is %v, want the cut label's %v", got.Size, want.Size)
	}
}

// Many columns in a narrow table each start inside it.
func TestColumnBoundsNarrow(t *testing.T) {
	var r input.Router
	gtx := loose(&r, image.Pt(120, 100))
	cols := make([]*tesserav1.TableColumn, 11)
	for i, b := range columnBounds(gtx, cols, 120) {
		if b[0] >= b[1] || b[1] > 120 {
			t.Errorf("column %d at %v in 120 px", i, b)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	for x, want := range map[float64]string{
		-0.04: "-0.04", 0.05: "0.05", 0.004: "0.004", 0.123: "0.12", math.Copysign(0, -1): "0",
		3.2e-7: "3.2e-07", 1e-300: "1e-300",
		12.345: "12.3", 1e308: "1e+308", -2.5e20: "-2.5e+20", 100: "100",
	} {
		if got := formatNumber(x); got != want {
			t.Errorf("formatNumber(%v) = %q, want %q", x, got, want)
		}
	}
}

func TestHugeRanges(t *testing.T) {
	if r := readGauge(&tesserav1.Gauge{Value: 1e308, Min: -1e308, Max: f(1e308), WarnAt: f(1e308)}); r.tone != gaugeMissing || len(r.ticks) != 0 {
		t.Errorf("a range too wide for a float64: %+v", r)
	}
	for _, p := range sparkPoints([]float64{-1e308, 1e308, 0}, -1e308, 1e308, 100, 40)[0] {
		if p.Y != 20 {
			t.Errorf("point %v, want mid-height", p)
		}
	}
}

func TestDescribeSpark(t *testing.T) {
	got := describeSpark(&tesserav1.Sparkline{Label: "Load", Samples: []float64{1, math.NaN(), 3}, StartLabel: "60s ago", Unit: "%"})
	if want := "Load: 2 samples, latest 3 %"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A restarted plugin's first view sends its ranges again.
func TestRestartResendsRanges(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("p", 0, 0, 1, 2)})
	states := live("p")
	view := &tesserav1.View{Body: listOf("events", &tesserav1.ListItem{Title: "a"})}
	var r input.Router
	var g Grid
	now := time.Now()
	sent := func() int {
		states[0].View = view
		n := 0
		for _, a := range frame(&r, &g, cfg, states, now) {
			if a.Event.GetScroll() != nil {
				n++
			}
		}
		return n
	}
	if n := sent(); n != 1 {
		t.Fatalf("first view sent %d ranges", n)
	}
	states[0].State, states[0].View = pluginhost.Starting, nil
	frame(&r, &g, cfg, states, now)
	states[0].State = pluginhost.Live
	if n := sent(); n != 1 {
		t.Errorf("after a restart, %d ranges sent, want 1", n)
	}
	// A launch that came and went between frames is told apart by its
	// Describe answer.
	states[0].Info = &tesserav1.DescribeResponse{Name: "p"}
	if n := sent(); n != 1 {
		t.Errorf("after an unseen restart, %d ranges sent, want 1", n)
	}
}

// An activation the plugin did not get gives the highlight back to the
// one before it.
func TestUnselect(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("cal", 0, 0, 1, 2)})
	states := live("cal")
	states[0].View = &tesserav1.View{Body: listOf("",
		&tesserav1.ListItem{Title: "Review", ActionId: "ev:1"},
		&tesserav1.ListItem{Title: "Focus", ActionId: "ev:2"},
	)}
	var r input.Router
	var g Grid
	now := time.Now()
	frame(&r, &g, cfg, states, now)
	rect := g.placed["cal"]
	top := rect.Min.Y + g.tiles["cal"].headSize.Y
	itemH := 2*12 + v0(&r).widget(loose(&r, rect.Size()), testTheme, textW("Review")).Size.Y
	frame(&r, &g, cfg, states, now, click(image.Pt(rect.Min.X+60, top+itemH/2))...)
	frame(&r, &g, cfg, states, now, click(image.Pt(rect.Min.X+60, top+itemH+itemH/2))...)
	if g.tiles["cal"].content.selected != "ev:2" {
		t.Fatalf("selected %q, want ev:2", g.tiles["cal"].content.selected)
	}
	g.Unselect("cal", "ev:1") // not the selected one: no change
	g.Unselect("cal", "ev:2")
	if got := g.tiles["cal"].content.selected; got != "ev:1" {
		t.Errorf("selected %q after the failed activation, want ev:1", got)
	}
}

// v0 is a view state ready for measuring.
func v0(r *input.Router) *viewState {
	var v viewState
	v.Layout(loose(r, image.Pt(10, 10)), testTheme, &tesserav1.View{})
	return &v
}

// A huge value costs no more than one that fits: measuring stops once
// it is too wide.
func TestTabularHugeValue(t *testing.T) {
	var r input.Router
	var v viewState
	gtx := loose(&r, image.Pt(300, 100))
	v.Layout(gtx, testTheme, &tesserav1.View{})
	start := time.Now()
	v.tabular(gtx, testTheme, strings.Repeat("7", 1_000_000), theme.TextStat, testTheme.Fg, theme.Body)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("a million digits took %v", d)
	}
}

// In a narrow row, long leading text leaves the title room.
func TestListItemLeadingCapped(t *testing.T) {
	var r input.Router
	var v viewState
	viewFrame(&r, &v, &tesserav1.View{Body: listOf("", &tesserav1.ListItem{
		Leading: "a very long leading text", Title: "Title", ActionId: "x",
	})}, image.Pt(120, 200))
	b, ok := labelBounds(&r, "Title")
	if !ok || b.Dx() <= 0 || b.Max.X > 120 {
		t.Errorf("the title is at %v (drawn %t), want inside the 120 px row", b, ok)
	}
}

// labelBounds is where the last frame drew the label with text.
func labelBounds(r *input.Router, text string) (image.Rectangle, bool) {
	var walk func(ns []input.SemanticNode) (image.Rectangle, bool)
	walk = func(ns []input.SemanticNode) (image.Rectangle, bool) {
		for _, n := range ns {
			if n.Desc.Label == text {
				return n.Desc.Bounds, true
			}
			if b, ok := walk(n.Children); ok {
				return b, ok
			}
		}
		return image.Rectangle{}, false
	}
	return walk(r.AppendSemantics(nil))
}

// A range the plugin's queue refused is sent again on the next frame.
func TestUnsentRangeResent(t *testing.T) {
	cfg := gridConfig([]config.Panel{panel("p", 0, 0, 1, 2)})
	states := live("p")
	states[0].View = &tesserav1.View{Body: listOf("events", &tesserav1.ListItem{Title: "a"})}
	var r input.Router
	var g Grid
	now := time.Now()
	count := func() int {
		n := 0
		for _, a := range frame(&r, &g, cfg, states, now) {
			if a.Event.GetScroll() != nil {
				n++
			}
		}
		return n
	}
	count()
	if n := count(); n != 0 {
		t.Fatalf("an unchanged range was sent again")
	}
	g.Unsent("p", "events")
	if n := count(); n != 1 {
		t.Errorf("after Unsent, %d ranges sent, want 1", n)
	}
}

// A narrow stat cuts its value, not its unit.
func TestStatKeepsUnit(t *testing.T) {
	var r input.Router
	stat := &tesserav1.Widget{Kind: &tesserav1.Widget_Stat{Stat: &tesserav1.Stat{Value: "1234", Unit: "MB/s"}}}
	viewFrame(&r, new(viewState), &tesserav1.View{Body: stat}, image.Pt(60, 100))
	// Semantic bounds span a label's constraints, so check where the
	// unit starts against its own width.
	unitW := recordLabel(loose(&r, image.Pt(60, 100)), testTheme, "MB/s", theme.TextBody, testTheme.Muted, theme.Body).dims.Size.X
	if b, ok := labelBounds(&r, "MB/s"); !ok || b.Min.X+unitW > 60 {
		t.Errorf("unit %d px wide starts at %v (drawn %t), want whole inside 60 px", unitW, b.Min, ok)
	}
}

// A loading item in a row leaves its siblings their room.
func TestLoadingSiblingVisible(t *testing.T) {
	var r input.Router
	loading := textW("CPU")
	loading.Loading = true
	row := &tesserav1.Widget{Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{
		Axis:  tesserav1.Axis_AXIS_HORIZONTAL,
		Items: []*tesserav1.StackItem{{Widget: loading}, {Widget: textW("visible")}},
	}}}
	viewFrame(&r, new(viewState), &tesserav1.View{Body: row}, image.Pt(300, 100))
	if b, ok := labelBounds(&r, "visible"); !ok || b.Dx() <= 0 || b.Max.X > 300 {
		t.Errorf("sibling at %v (drawn %t)", b, ok)
	}
}

// A list that lost its id to an earlier one does not take the place of
// the unnamed list after it.
func TestDuplicateIDKeepsUnnamedKeys(t *testing.T) {
	var r input.Router
	var v viewState
	a := listOf("a", &tesserav1.ListItem{Title: "x"})
	two := column2(a, listOf("", &tesserav1.ListItem{Title: "y"}))
	viewFrame(&r, &v, &tesserav1.View{Body: two}, image.Pt(300, 300))
	unnamed := v.keys[two.GetStack().GetItems()[1].GetWidget()]
	three := &tesserav1.Widget{Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{Items: []*tesserav1.StackItem{
		{Widget: listOf("a", &tesserav1.ListItem{Title: "x"})},
		{Widget: listOf("a", &tesserav1.ListItem{Title: "z"})},
		{Widget: listOf("", &tesserav1.ListItem{Title: "y"})},
	}}}}
	viewFrame(&r, &v, &tesserav1.View{Body: three}, image.Pt(300, 300))
	if got := v.keys[three.GetStack().GetItems()[2].GetWidget()]; got != unnamed {
		t.Errorf("the unnamed list is %q, was %q", got, unnamed)
	}
}

// Long captions under a sparkline share its width; the end one stays in
// view.
func TestSparkCaptionsShare(t *testing.T) {
	var r input.Router
	long := strings.Repeat("start ", 10)
	spark := &tesserav1.Widget{Kind: &tesserav1.Widget_Sparkline{Sparkline: &tesserav1.Sparkline{
		Samples: []float64{1, 2}, StartLabel: long, EndLabel: "now",
	}}}
	viewFrame(&r, new(viewState), &tesserav1.View{Body: spark}, image.Pt(120, 200))
	nowW := recordLabel(loose(&r, image.Pt(120, 200)), testTheme, "now", theme.TextMin, testTheme.Muted, theme.Mono).dims.Size.X
	if b, ok := labelBounds(&r, "now"); !ok || b.Min.X+nowW > 120 {
		t.Errorf("end caption %d px wide starts at %v (drawn %t)", nowW, b.Min, ok)
	}
}

// A view is indexed once, not every frame it is drawn.
func TestViewIndexedOnce(t *testing.T) {
	var r input.Router
	var v viewState
	gtx := loose(&r, image.Pt(300, 200))
	view := &tesserav1.View{}
	v.Layout(gtx, testTheme, view)
	marker := &tesserav1.Widget{}
	v.keys[marker] = "marker"
	v.Layout(gtx, testTheme, view)
	if _, ok := v.keys[marker]; !ok {
		t.Error("the same view was indexed again")
	}
	v.Layout(gtx, testTheme, &tesserav1.View{})
	if _, ok := v.keys[marker]; ok {
		t.Error("a new view kept the old index")
	}
}

// Cells widen digits where the shaper put them: a right-to-left run
// keeps its glyphs right to left, and what follows moves over.
func TestTabularCellsKeepOrder(t *testing.T) {
	const letter, digit = 1, 2
	// "אב12" as shaped: the Hebrew run, its first letter rightmost, then
	// the digits.
	glyphs := []text.Glyph{
		{ID: letter, X: fixed.I(15), Advance: fixed.I(15)},
		{ID: letter, X: fixed.I(0), Advance: fixed.I(15)},
		{ID: digit, X: fixed.I(30), Advance: fixed.I(10)},
		{ID: digit, X: fixed.I(40), Advance: fixed.I(10)},
	}
	left, width := inCells(glyphs, map[text.GlyphID]bool{digit: true}, fixed.I(20))
	var got []int
	for _, g := range glyphs {
		got = append(got, g.X.Round())
	}
	if want := []int{15, 0, 35, 55}; !slices.Equal(got, want) || left != 0 || width != fixed.I(70) {
		t.Errorf("glyphs at %v, left %v, width %v; want %v, 0, 70", got, left.Round(), width.Round(), want)
	}
}

// A value too wide in cells falls back to a label of the whole value,
// which is cut off with an ellipsis only if it does not fit as plain
// text either, and which a screen reader reads whole.
func TestTabularFallback(t *testing.T) {
	var r input.Router
	var v viewState
	gtx := loose(&r, image.Pt(60, 100))
	v.Layout(gtx, testTheme, &tesserav1.View{})
	v.digits[digitKey{theme.Body, theme.TextStat, gtx.Metric.PxPerSp, gtx.Locale}] = 18 // wider cells than the digits
	d := v.tabular(gtx, testTheme, "11111111", theme.TextStat, testTheme.Fg, theme.Body)
	r.Frame(gtx.Ops)
	if !hasLabel(&r, "11111111") || d.Size.X > 60 {
		t.Errorf("fallback %v wide, labelled whole %t", d.Size, hasLabel(&r, "11111111"))
	}
}

// A mark on a digit moves with the digit as it is centred in its cell.
func TestTabularCellsCarryMarks(t *testing.T) {
	const digit, mark = 2, 3
	// "1́2" as shaped: the mark sits at the end of the 1, with no advance.
	glyphs := []text.Glyph{
		{ID: digit, X: fixed.I(0), Advance: fixed.I(10)},
		{ID: mark, X: fixed.I(10)},
		{ID: digit, X: fixed.I(10), Advance: fixed.I(10)},
	}
	inCells(glyphs, map[text.GlyphID]bool{digit: true}, fixed.I(20))
	var got []int
	for _, g := range glyphs {
		got = append(got, g.X.Round())
	}
	if want := []int{5, 15, 25}; !slices.Equal(got, want) {
		t.Errorf("glyphs at %v, want %v", got, want)
	}
}

// A stack draws only the items it has room for, however many it has.
func TestStackSkipsItemsWithNoRoom(t *testing.T) {
	var items []*tesserav1.StackItem
	for i := range 50000 {
		items = append(items, &tesserav1.StackItem{Widget: &tesserav1.Widget{Kind: &tesserav1.Widget_Text{Text: &tesserav1.Text{Value: fmt.Sprint("line ", i)}}}})
	}
	items = append(items, &tesserav1.StackItem{Widget: listOf("events", &tesserav1.ListItem{Title: "a"})})
	view := &tesserav1.View{Body: &tesserav1.Widget{Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{Items: items, Gap: tesserav1.Spacing_SPACING_NONE}}}}
	var r input.Router
	var v viewState
	got := scrolls(viewFrame(&r, &v, view, image.Pt(300, 100)))
	if !hasLabel(&r, "line 0") || hasLabel(&r, "line 49999") {
		t.Errorf("first line drawn %t, last line drawn %t", hasLabel(&r, "line 0"), hasLabel(&r, "line 49999"))
	}
	if len(got) != 1 || got[0].GetVisibleCount() != 0 || v.lists["id:events"] == nil {
		t.Errorf("the list out of room sent %v, kept state %t", got, v.lists["id:events"] != nil)
	}
	// The gaps of items out of room do not take the room of the first.
	gapped := &tesserav1.View{Body: &tesserav1.Widget{Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{Items: items}}}}
	viewFrame(&r, &v, gapped, image.Pt(300, 100))
	if !hasLabel(&r, "line 0") {
		t.Error("a stack whose gaps outgrow it drew nothing")
	}
	// Items out of room add no gaps, and the stack stays within its
	// space.
	gtx := context(&r, image.Pt(300, 100), time.Now())
	if d := v.widget(gtx, testTheme, gapped.GetBody()); d.Size.Y > 100 {
		t.Errorf("the stack is %v tall in 100 px", d.Size.Y)
	}
}

// A value cut at the shaping limit ends in an ellipsis, even when what
// is left of it draws nothing.
func TestTabularCutShows(t *testing.T) {
	var r input.Router
	var v viewState
	gtx := loose(&r, image.Pt(300, 100))
	v.Layout(gtx, testTheme, &tesserav1.View{})
	// Zero-width spaces, three bytes each, over the limit before the 42.
	blank := strings.Repeat("\u200b", maxTabular/3+1)
	v.tabular(gtx, testTheme, blank+"42", theme.TextStat, testTheme.Fg, theme.Body)
	r.Frame(gtx.Ops)
	if !hasLabel(&r, blank[:maxTabular/3*3]+"…") {
		t.Error("a value cut at the limit does not end in an ellipsis")
	}
}

// A value is one label, not one per digit.
func TestStatValueIsOneLabel(t *testing.T) {
	var r input.Router
	stat := &tesserav1.Widget{Kind: &tesserav1.Widget_Stat{Stat: &tesserav1.Stat{Label: "CPU", Value: "12.5", Unit: "%"}}}
	viewFrame(&r, new(viewState), &tesserav1.View{Body: stat}, image.Pt(300, 200))
	if !hasLabel(&r, "12.5") || hasLabel(&r, "1") {
		t.Error(`want the value labelled "12.5", not digit by digit`)
	}
}

// A long unit leaves the value room.
func TestStatLongUnit(t *testing.T) {
	var r input.Router
	stat := &tesserav1.Widget{Kind: &tesserav1.Widget_Stat{Stat: &tesserav1.Stat{Value: "1234", Unit: "requests per second"}}}
	viewFrame(&r, new(viewState), &tesserav1.View{Body: stat}, image.Pt(120, 100))
	unit, ok := labelBounds(&r, "requests per second")
	if !ok || unit.Min.X < 40 {
		t.Errorf("the unit starts at %v (drawn %t), leaving the value no room", unit.Min, ok)
	}
}

// A loading gauge shows its limits, as the mockup's does.
func TestLoadingGaugeLimits(t *testing.T) {
	var r input.Router
	g := &tesserav1.Widget{Loading: true, Kind: &tesserav1.Widget_Gauge{Gauge: &tesserav1.Gauge{}}}
	viewFrame(&r, new(viewState), &tesserav1.View{Body: g}, image.Pt(300, 200))
	if !hasLabel(&r, "0") || !hasLabel(&r, "100") {
		t.Error("the loading gauge has no limits")
	}
}

// A row's spoken label has the cells it shows, not extra ones.
func TestRowLabelIgnoresExtraCells(t *testing.T) {
	var r input.Router
	table := &tesserav1.Table{
		Columns: []*tesserav1.TableColumn{{Title: "Name"}, {Title: "CPU"}},
		Rows:    []*tesserav1.TableRow{{Cells: []string{"A", "B", "C"}, ActionId: "x"}},
	}
	viewFrame(&r, new(viewState), &tesserav1.View{Body: &tesserav1.Widget{Kind: &tesserav1.Widget_Table{Table: table}}}, image.Pt(300, 200))
	if !hasLabel(&r, "A, B") || hasLabel(&r, "A, B, C") {
		t.Error(`want the row labelled "A, B"`)
	}
}
