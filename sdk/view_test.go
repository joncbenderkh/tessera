// SPDX-License-Identifier: GPL-3.0-or-later

package sdk_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
	"github.com/joncbenderkh/tessera/sdk"
)

func ptr[T any](v T) *T { return &v }

func TestBuilders(t *testing.T) {
	got := sdk.View{
		Title: "All",
		Body: sdk.Column(
			sdk.Text("hi").Size(sdk.SizeDisplay).Tone(sdk.ToneAccent).Monospace().ID("t"),
			sdk.Row(
				sdk.Stat("CPU", "24.8").Unit("%").Caption("4 cores").CaptionTone(sdk.ToneOK),
				sdk.Gauge("Disk", 71).Range(0, 200).Unit("%").Display("71").WarnAt(150).ErrorAt(190).Caption("free"),
				sdk.Weighted(2, sdk.Sparkline("Net", 1, 2, 3).Range(0, 10).Unit("MB/s").Labels("60s ago", "now").Tone(sdk.ToneWarn).Loading()),
				sdk.Weighted(1, sdk.Space()),
			).Gap(sdk.SpacingSmall).Dividers(),
			sdk.Table(sdk.TableColumn("Name").Weight(3), sdk.TableColumn("PID").AlignEnd()).
				Row("init", "1").
				ActionRow("pid:42", "sh", "42").
				Sort(1, sdk.SortDescending).
				Empty("No processes").
				ID("procs"),
			sdk.List(sdk.Item("Standup").Leading("10:00").Detail("Room 4").Action("ev:1")).
				Add(sdk.Item("Lunch")).
				Empty("Nothing today"),
			nil,
		).Loading(),
	}.Proto()

	widget := func(w *tesserav1.Widget) *tesserav1.StackItem { return &tesserav1.StackItem{Widget: w} }
	want := &tesserav1.View{
		Title: "All",
		Body: &tesserav1.Widget{Loading: true, Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{
			Items: []*tesserav1.StackItem{
				widget(&tesserav1.Widget{Id: "t", Kind: &tesserav1.Widget_Text{Text: &tesserav1.Text{
					Value: "hi", Size: tesserav1.TextSize_TEXT_SIZE_DISPLAY, Tone: tesserav1.Tone_TONE_ACCENT, Monospace: true,
				}}}),
				widget(&tesserav1.Widget{Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{
					Axis:     tesserav1.Axis_AXIS_HORIZONTAL,
					Gap:      tesserav1.Spacing_SPACING_SMALL,
					Dividers: true,
					Items: []*tesserav1.StackItem{
						widget(&tesserav1.Widget{Kind: &tesserav1.Widget_Stat{Stat: &tesserav1.Stat{
							Label: "CPU", Value: "24.8", Unit: "%", Caption: "4 cores", CaptionTone: tesserav1.Tone_TONE_OK,
						}}}),
						widget(&tesserav1.Widget{Kind: &tesserav1.Widget_Gauge{Gauge: &tesserav1.Gauge{
							Label: "Disk", Value: 71, Min: 0, Max: ptr(200.0), Unit: "%", Display: "71",
							WarnAt: ptr(150.0), ErrorAt: ptr(190.0), Caption: "free",
						}}}),
						{Weight: 2, Widget: &tesserav1.Widget{Loading: true, Kind: &tesserav1.Widget_Sparkline{Sparkline: &tesserav1.Sparkline{
							Label: "Net", Samples: []float64{1, 2, 3}, Min: ptr(0.0), Max: ptr(10.0), Unit: "MB/s",
							StartLabel: "60s ago", EndLabel: "now", Tone: tesserav1.Tone_TONE_WARN,
						}}}},
						{Weight: 1},
					},
				}}}),
				widget(&tesserav1.Widget{Id: "procs", Kind: &tesserav1.Widget_Table{Table: &tesserav1.Table{
					Columns: []*tesserav1.TableColumn{
						{Title: "Name", Weight: 3},
						{Title: "PID", Align: tesserav1.Align_ALIGN_END},
					},
					Rows: []*tesserav1.TableRow{
						{Cells: []string{"init", "1"}},
						{Cells: []string{"sh", "42"}, ActionId: "pid:42"},
					},
					SortColumn: 1,
					SortOrder:  tesserav1.SortOrder_SORT_ORDER_DESCENDING,
					EmptyText:  "No processes",
				}}}),
				widget(&tesserav1.Widget{Kind: &tesserav1.Widget_List{List: &tesserav1.List{
					Items: []*tesserav1.ListItem{
						{Leading: "10:00", Title: "Standup", Detail: "Room 4", ActionId: "ev:1"},
						{Title: "Lunch"},
					},
					EmptyText: "Nothing today",
				}}}),
				{},
			},
		}}},
	}
	if !proto.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestRawAndEmptyBody(t *testing.T) {
	w := &tesserav1.Widget{Id: "x"}
	if got := (sdk.View{Body: sdk.Raw(w)}).Proto().GetBody(); got != w {
		t.Errorf("Raw body = %v, want the widget given", got)
	}
	if got := (sdk.View{Title: "t"}).Proto(); got.GetBody() != nil || got.GetTitle() != "t" {
		t.Errorf("empty view = %v", got)
	}
}

func TestNilBuildersAndSort(t *testing.T) {
	var text *sdk.TextWidget
	got := sdk.View{Body: sdk.Column(
		text,
		sdk.Table(nil, sdk.TableColumn("A")).Sort(-1, sdk.SortAscending),
		sdk.List(nil, sdk.Item("x")),
	)}.Proto()
	items := got.GetBody().GetStack().GetItems()
	if len(items) != 3 || items[0].GetWidget() != nil {
		t.Fatalf("stack items %v, want a nil builder as empty space", items)
	}
	table := items[1].GetWidget().GetTable()
	if len(table.GetColumns()) != 1 {
		t.Errorf("columns %v, want the nil one skipped", table.GetColumns())
	}
	if table.GetSortColumn() == 0 {
		t.Errorf("Sort(-1) put the indicator on column 0")
	}
	if n := len(items[2].GetWidget().GetList().GetItems()); n != 1 {
		t.Errorf("%d list items, want the nil one skipped", n)
	}
}

// TestBuildersCopySlices lets a plugin reuse its slices, such as a ring
// buffer of samples, once a view is built from them.
func TestBuildersCopySlices(t *testing.T) {
	samples := []float64{1, 2}
	cells := []string{"a", "b"}
	view := sdk.View{Body: sdk.Column(
		sdk.Sparkline("cpu", samples...),
		sdk.Table(sdk.TableColumn("x"), sdk.TableColumn("y")).Row(cells...).ActionRow("id", cells...),
	)}.Proto()
	samples[0], cells[0] = 9, "z"
	items := view.GetBody().GetStack().GetItems()
	if got := items[0].GetWidget().GetSparkline().GetSamples()[0]; got != 1 {
		t.Errorf("sample = %v, want 1", got)
	}
	for _, row := range items[1].GetWidget().GetTable().GetRows() {
		if got := row.GetCells()[0]; got != "a" {
			t.Errorf("cell = %q, want a", got)
		}
	}
}
