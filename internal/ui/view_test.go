// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"image"
	"math"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"google.golang.org/protobuf/proto"

	"github.com/joncbenderkh/tessera/internal/config"
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
				v.index(w, new(int))
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
	v.digits[digitKey{theme.Body, theme.TextValue, gtx.Metric.PxPerSp}] = 50
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
	if got := v.lists["procs"].Position.First; got != int(moved[0].GetFirstVisible()) {
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
}
