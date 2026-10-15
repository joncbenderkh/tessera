// SPDX-License-Identifier: GPL-3.0-or-later

package tesserav1_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// counter is a minimal panel: a stat that counts activations.
type counter struct {
	tesserav1.UnimplementedPanelServiceServer
	clicks chan struct{}
	label  string
}

func (c *counter) Describe(_ context.Context, req *tesserav1.DescribeRequest) (*tesserav1.DescribeResponse, error) {
	return &tesserav1.DescribeResponse{
		Name:            "counter",
		Version:         "0.1.0",
		ProtocolVersion: tesserav1.ProtocolVersion,
		ViewVersion:     req.GetViewVersions()[0],
		DefaultSize:     &tesserav1.GridSize{Columns: 1, Rows: 1},
	}, nil
}

func (c *counter) Configure(_ context.Context, req *tesserav1.ConfigureRequest) (*tesserav1.ConfigureResponse, error) {
	label, ok := req.GetSettings().GetFields()["label"]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "settings.label is required")
	}
	c.label = label.GetStringValue()
	return &tesserav1.ConfigureResponse{}, nil
}

func (c *counter) view(n int) *tesserav1.View {
	return &tesserav1.View{
		Title: "Counter",
		Body: &tesserav1.Widget{Kind: &tesserav1.Widget_Stat{Stat: &tesserav1.Stat{
			Label: c.label,
			Value: string(rune('0' + n)),
		}}},
	}
}

func (c *counter) Watch(_ *tesserav1.WatchRequest, stream grpc.ServerStreamingServer[tesserav1.WatchResponse]) error {
	for n := 0; ; n++ {
		if err := stream.Send(&tesserav1.WatchResponse{View: c.view(n)}); err != nil {
			return err
		}
		select {
		case <-stream.Context().Done():
			return nil
		case <-c.clicks:
		}
	}
}

func (c *counter) HandleEvent(_ context.Context, req *tesserav1.HandleEventRequest) (*tesserav1.HandleEventResponse, error) {
	if req.GetActivate().GetActionId() == "increment" {
		c.clicks <- struct{}{}
	}
	return &tesserav1.HandleEventResponse{}, nil
}

func dial(t *testing.T, srv tesserav1.PanelServiceServer) tesserav1.PanelServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 16)
	s := grpc.NewServer()
	tesserav1.RegisterPanelServiceServer(s, srv)
	go s.Serve(lis)
	t.Cleanup(s.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return tesserav1.NewPanelServiceClient(conn)
}

func TestPanelLifecycle(t *testing.T) {
	ctx := t.Context()
	client := dial(t, &counter{clicks: make(chan struct{})})

	desc, err := client.Describe(ctx, &tesserav1.DescribeRequest{HostVersion: "0.1.0", ViewVersions: []uint32{tesserav1.ViewVersion}})
	if err != nil {
		t.Fatal(err)
	}
	if desc.GetProtocolVersion() != tesserav1.ProtocolVersion || desc.GetViewVersion() != tesserav1.ViewVersion {
		t.Fatalf("handshake mismatch: %v", desc)
	}

	_, err = client.Configure(ctx, &tesserav1.ConfigureRequest{PanelId: "c"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing settings: got %v, want InvalidArgument", err)
	}
	settings, err := structpb.NewStruct(map[string]any{"label": "Clicks"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Configure(ctx, &tesserav1.ConfigureRequest{PanelId: "c", Settings: settings}); err != nil {
		t.Fatal(err)
	}

	stream, err := client.Watch(ctx, &tesserav1.WatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for want := range 3 {
		update, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		stat := update.GetView().GetBody().GetStat()
		if stat.GetLabel() != "Clicks" || stat.GetValue() != string(rune('0'+want)) {
			t.Fatalf("update %d: got %v", want, stat)
		}
		event := &tesserav1.HandleEventRequest{Event: &tesserav1.HandleEventRequest_Activate{
			Activate: &tesserav1.ActivateEvent{ActionId: "increment"},
		}}
		if _, err := client.HandleEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := client.Render(ctx, &tesserav1.RenderRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Render on a streaming-only plugin: got %v, want Unimplemented", err)
	}
}

func TestViewRoundTrip(t *testing.T) {
	lo, hi := 0.0, 100.0
	view := &tesserav1.View{
		Title: "System",
		Body: &tesserav1.Widget{Kind: &tesserav1.Widget_Stack{Stack: &tesserav1.Stack{
			Axis:     tesserav1.Axis_AXIS_HORIZONTAL,
			Dividers: true,
			Items: []*tesserav1.StackItem{
				{Weight: 1, Widget: &tesserav1.Widget{Kind: &tesserav1.Widget_Gauge{Gauge: &tesserav1.Gauge{
					Label: "Disk", Value: 68, Max: proto.Float64(100), Unit: "%", WarnAt: proto.Float64(80), ErrorAt: proto.Float64(95),
				}}}},
				{Weight: 1, Widget: &tesserav1.Widget{Kind: &tesserav1.Widget_Sparkline{Sparkline: &tesserav1.Sparkline{
					Samples: []float64{1, 2, 3}, Min: &lo, Max: &hi, StartLabel: "60s ago", EndLabel: "now",
				}}}},
				{Widget: &tesserav1.Widget{Id: "procs", Kind: &tesserav1.Widget_Table{Table: &tesserav1.Table{
					Columns:   []*tesserav1.TableColumn{{Title: "Process"}, {Title: "CPU", Align: tesserav1.Align_ALIGN_END}},
					Rows:      []*tesserav1.TableRow{{Cells: []string{"go build", "12.6%"}, ActionId: "pid:4821"}},
					SortOrder: tesserav1.SortOrder_SORT_ORDER_DESCENDING, SortColumn: 1,
				}}}},
				{Widget: &tesserav1.Widget{Loading: true, Kind: &tesserav1.Widget_List{List: &tesserav1.List{EmptyText: "No upcoming events"}}}},
			},
		}}},
	}
	data, err := proto.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var got tesserav1.View
	if err := proto.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(view, &got) {
		t.Fatalf("round trip changed the view:\n got %v\nwant %v", &got, view)
	}
}
