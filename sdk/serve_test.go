// SPDX-License-Identifier: GPL-3.0-or-later

package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
	"github.com/joncbenderkh/tessera/sdk"
)

// The test binary doubles as a plugin built with the SDK: started by the
// host, it serves greeter, unless its file name is one of these:
//
//   - quiet: sends no view.
//   - closer: sends one view, then closes Plugin.Views.
//   - oversize: sends a view over each limit, then one within them.
func TestMain(m *testing.M) {
	if os.Getenv(tesserav1.MagicCookieKey) != "" {
		name := strings.TrimSuffix(filepath.Base(os.Args[0]), exeSuffix())
		views := make(chan sdk.View)
		switch name {
		case "quiet":
		case "closer":
			go func() {
				views <- sdk.View{Body: sdk.Text("last")}
				close(views)
			}()
		case "oversize":
			go sendOversize(views)
		default:
			serveGreeter(name)
			return
		}
		sdk.Serve(sdk.Plugin{Name: name, Version: "0.0.1", Views: views})
		return
	}
	os.Exit(m.Run())
}

func sendOversize(views chan<- sdk.View) {
	var columns []*sdk.TableColumnSpec
	for range tesserav1.MaxTableColumns + 1 {
		columns = append(columns, sdk.TableColumn("c"))
	}
	var deep sdk.Widget = sdk.Text("bottom")
	for range tesserav1.MaxViewDepth {
		deep = sdk.Column(deep)
	}
	many := sdk.Column()
	for range tesserav1.MaxViewWidgets {
		many.Add(sdk.Text("w"))
	}
	var shared sdk.Widget = sdk.Text("leaf")
	for range 4 {
		level := sdk.Column()
		for range tesserav1.MaxViewWidgets {
			level.Add(shared)
		}
		shared = level
	}
	cycle := sdk.Column()
	cycle.Add(cycle)
	invalidUTF8 := sdk.Text("bad\xffname")
	for _, body := range []sdk.Widget{
		sdk.Table(columns...),
		deep,
		many,
		shared,
		cycle,
		invalidUTF8,
		sdk.Text(strings.Repeat("x", tesserav1.MaxViewBytes)),
		sdk.Text("within"),
	} {
		views <- sdk.View{Body: body}
	}
}

const greeterSchema = `{"type":"object","properties":{"greeting":{"type":"string"}}}`

// serveGreeter shows its greeting setting, "pong" once activated, and
// the visible range once scrolled. It rejects the greeting "bad".
func serveGreeter(name string) {
	views := make(chan sdk.View)
	var mu sync.Mutex
	greeting := "hello"
	show := func(s string) {
		views <- sdk.View{Title: name, Body: sdk.Text(s)}
	}
	sdk.Serve(sdk.Plugin{
		Name:         name,
		Version:      "1.2.3",
		Description:  "Says hello.",
		ConfigSchema: greeterSchema,
		DefaultSize:  sdk.Size{Columns: 3, Rows: 2},
		Views:        views,
		Configure: func(_ context.Context, c sdk.Config) error {
			var s struct {
				Greeting string `json:"greeting"`
			}
			if err := c.Decode(&s); err != nil {
				return err
			}
			switch s.Greeting {
			case "bad":
				return sdk.InvalidSettings("greeting %q is not allowed", s.Greeting)
			case "unimplemented":
				return status.Error(codes.Unimplemented, "a data source's own status")
			}
			mu.Lock()
			defer mu.Unlock()
			if s.Greeting != "" {
				greeting = s.Greeting
			}
			show(greeting + " " + c.PanelID)
			return nil
		},
		Activate: func(_ context.Context, id string) error {
			show("activated " + id)
			return nil
		},
		Scroll: func(_ context.Context, e sdk.ScrollEvent) error {
			show(strings.Join([]string{"scrolled", e.WidgetID, strconv.Itoa(e.First), strconv.Itoa(e.Count)}, " "))
			return nil
		},
	})
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// launch copies the test binary into a plugins directory as name and
// launches it.
func launch(t *testing.T, name string) *pluginhost.Instance {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name+exeSuffix()), data, 0o755); err != nil {
		t.Fatal(err)
	}
	inst, err := pluginhost.Launch(context.Background(), dir, name, func(line string) { t.Log(line) }, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(inst.Close)
	return inst
}

func configure(t *testing.T, inst *pluginhost.Instance, settings map[string]any) error {
	t.Helper()
	s, err := structpb.NewStruct(settings)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = inst.Client.Configure(ctx, &tesserav1.ConfigureRequest{
		PanelId:  "p1",
		Settings: s,
		Size:     &tesserav1.GridSize{Columns: 2, Rows: 2},
	})
	return err
}

func watch(t *testing.T, inst *pluginhost.Instance, heartbeat time.Duration) tesserav1.PanelService_WatchClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stream, err := inst.Client.Watch(ctx, &tesserav1.WatchRequest{HeartbeatInterval: durationpb.New(heartbeat)})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

// next returns the text of the next view on stream, skipping heartbeats.
func next(t *testing.T, stream tesserav1.PanelService_WatchClient) string {
	t.Helper()
	for {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if v := resp.GetView(); v != nil {
			return v.GetBody().GetText().GetValue()
		}
	}
}

func TestDescribe(t *testing.T) {
	inst := launch(t, "greeter")
	want := &tesserav1.DescribeResponse{
		Name:            "greeter",
		Version:         "1.2.3",
		Description:     "Says hello.",
		ProtocolVersion: tesserav1.ProtocolVersion,
		ViewVersion:     tesserav1.ViewVersion,
		ConfigSchema:    greeterSchema,
		DefaultSize:     &tesserav1.GridSize{Columns: 3, Rows: 2},
	}
	if !proto.Equal(inst.Info, want) {
		t.Errorf("Describe = %v, want %v", inst.Info, want)
	}
}

func TestViewsAndEvents(t *testing.T) {
	inst := launch(t, "greeter")
	if err := configure(t, inst, map[string]any{"greeting": "hi"}); err != nil {
		t.Fatal(err)
	}
	stream := watch(t, inst, time.Minute)
	if got := next(t, stream); got != "hi p1" {
		t.Fatalf("first view %q, want %q", got, "hi p1")
	}

	ctx := context.Background()
	if _, err := inst.Client.HandleEvent(ctx, &tesserav1.HandleEventRequest{
		Event: &tesserav1.HandleEventRequest_Activate{Activate: &tesserav1.ActivateEvent{ActionId: "go"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := next(t, stream); got != "activated go" {
		t.Fatalf("after activation %q, want %q", got, "activated go")
	}
	if _, err := inst.Client.HandleEvent(ctx, &tesserav1.HandleEventRequest{
		Event: &tesserav1.HandleEventRequest_Scroll{Scroll: &tesserav1.ScrollEvent{WidgetId: "t", FirstVisible: 4, VisibleCount: 10}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := next(t, stream); got != "scrolled t 4 10" {
		t.Fatalf("after scroll %q, want %q", got, "scrolled t 4 10")
	}

	resp, err := inst.Client.Render(ctx, &tesserav1.RenderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetView().GetBody().GetText().GetValue(); got != "scrolled t 4 10" {
		t.Errorf("Render %q, want the latest view", got)
	}

	// A new stream starts with the latest view.
	if got := next(t, watch(t, inst, time.Minute)); got != "scrolled t 4 10" {
		t.Errorf("second stream starts with %q, want the latest view", got)
	}
}

func TestHeartbeat(t *testing.T) {
	inst := launch(t, "greeter")
	if err := configure(t, inst, nil); err != nil {
		t.Fatal(err)
	}
	stream := watch(t, inst, time.Second)
	if got := next(t, stream); got != "hello p1" {
		t.Fatalf("first view %q, want %q", got, "hello p1")
	}
	start := time.Now()
	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetContent() != nil {
		t.Errorf("got a view, want a heartbeat")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("heartbeat after %v, want about half the interval", d)
	}
}

func TestEmptyViewBeforeTheFirst(t *testing.T) {
	inst := launch(t, "quiet")
	if err := configure(t, inst, map[string]any{"ignored": true}); err != nil {
		t.Fatalf("Configure without a callback: %v", err)
	}
	stream := watch(t, inst, 400*time.Millisecond)
	for i := range 3 {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		v := resp.GetView()
		if i == 0 && (v == nil || v.GetBody() != nil) {
			t.Errorf("first response %v, want an empty view", resp)
		}
		if i > 0 && resp.GetContent() != nil {
			t.Errorf("response %d %v, want a heartbeat", i, resp)
		}
	}
}

func TestEmptyViewWithoutHeartbeats(t *testing.T) {
	inst := launch(t, "quiet")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := inst.Client.Watch(ctx, &tesserav1.WatchRequest{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if v := resp.GetView(); v == nil || v.GetBody() != nil {
		t.Errorf("first response %v, want an empty view", resp)
	}
}

func TestTinyHeartbeatInterval(t *testing.T) {
	inst := launch(t, "greeter")
	stream := watch(t, inst, time.Nanosecond)
	start := time.Now()
	n := 0
	for time.Since(start) < 500*time.Millisecond {
		if _, err := stream.Recv(); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n > 100 {
		t.Errorf("%d responses in half a second, want a heartbeat at most every 10ms", n)
	}
}

func TestDecodeNamesExactly(t *testing.T) {
	type inner struct {
		Port int `json:"port"`
	}
	type Embedded struct {
		Shared string `json:"shared"`
	}
	type settings struct {
		Embedded
		Name    string `json:"name,omitempty"`
		Plain   int
		Skipped int `json:"-"`
		Server  *inner
		Servers []inner          `json:"servers"`
		ByName  map[string]inner `json:"by_name"`
		When    time.Time        `json:"when"`
	}
	for _, tc := range []struct {
		settings map[string]any
		unknown  string
	}{
		{map[string]any{"name": "x", "Plain": 1.0, "shared": "s", "when": "2026-10-27T00:00:00Z"}, ""},
		{map[string]any{"Server": map[string]any{"port": 1.0}, "servers": []any{map[string]any{"port": 2.0}}, "by_name": map[string]any{"a": map[string]any{"port": 3.0}}}, ""},
		{map[string]any{"Name": "x"}, "Name"},
		{map[string]any{"plain": 1.0}, "plain"},
		{map[string]any{"Skipped": 1.0}, "Skipped"},
		{map[string]any{"Shared": "s"}, "Shared"},
		{map[string]any{"Server": map[string]any{"Port": 1.0}}, "Server.Port"},
		{map[string]any{"servers": []any{map[string]any{"PORT": 2.0}}}, "servers.PORT"},
		{map[string]any{"by_name": map[string]any{"a": map[string]any{"Port": 3.0}}}, "by_name.a.Port"},
	} {
		var s settings
		err := sdk.Config{Settings: tc.settings}.Decode(&s)
		if tc.unknown == "" && err != nil {
			t.Errorf("Decode(%v) = %v, want nil", tc.settings, err)
		}
		if want := strconv.Quote(tc.unknown); tc.unknown != "" && (err == nil || !strings.Contains(err.Error(), "unknown setting "+want)) {
			t.Errorf("Decode(%v) = %v, want unknown setting %s", tc.settings, err, want)
		}
	}
}

type recursive struct {
	*recursive
	Step int `json:"step"`
}

type portConfig struct{ Port int }
type hostConfig struct{ Host string }
type deep struct{ Server portConfig }
type viaDeep struct{ deep }
type shallow struct{ Server hostConfig }
type stepConfig struct{ Step int }
type taggedEmbed struct {
	stepConfig `json:"nested"`
}

// TestDecodeEmbedding pins encoding/json's rules for embedded structs:
// the shallowest field of a name wins, and a type embedded in itself
// is looked into once.
func TestDecodeEmbedding(t *testing.T) {
	var r recursive
	if err := (sdk.Config{Settings: map[string]any{"step": 2.0}}).Decode(&r); err != nil || r.Step != 2 || r.recursive != nil {
		t.Errorf("recursive: Decode = %v, step %d", err, r.Step)
	}
	var s struct {
		viaDeep
		shallow
	}
	if err := (sdk.Config{Settings: map[string]any{"Server": map[string]any{"Host": "h"}}}).Decode(&s); err != nil || s.shallow.Server.Host != "h" {
		t.Errorf("shallowest: Decode = %v, host %q", err, s.shallow.Server.Host)
	}
}

func TestDecodeMisuseIsNotASettingsError(t *testing.T) {
	var s struct{}
	for _, v := range []any{s, nil, (*struct{})(nil)} {
		err := sdk.Config{Settings: map[string]any{}}.Decode(v)
		if err == nil || strings.Contains(err.Error(), "json:") {
			t.Errorf("Decode(%T) = %v, want the SDK's own error", v, err)
		}
	}
}

// TestDecodeTaggedEmbedding decodes into an unexported embedded struct
// that a tag names, as encoding/json does.
func TestDecodeTaggedEmbedding(t *testing.T) {
	var s taggedEmbed
	err := sdk.Config{Settings: map[string]any{"nested": map[string]any{"Step": 2.0}}}.Decode(&s)
	if err != nil || s.Step != 2 {
		t.Errorf("Decode = %v, step %d, want step 2", err, s.Step)
	}
}

func TestDecodeKeepsTheCause(t *testing.T) {
	var s struct {
		Step int `json:"step"`
	}
	err := sdk.Config{Settings: map[string]any{"step": "x"}}.Decode(&s)
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		t.Errorf("Decode = %v, want it to wrap a *json.UnmarshalTypeError", err)
	}
}

// TestDecodeFollowsInterfaces checks the exact names inside a value an
// interface field already points to, which encoding/json decodes into.
func TestDecodeFollowsInterfaces(t *testing.T) {
	inner := &struct {
		Step int `json:"step"`
	}{}
	s := struct{ Nested any }{Nested: inner}
	err := sdk.Config{Settings: map[string]any{"Nested": map[string]any{"Step": 2.0}}}.Decode(&s)
	if err == nil || !strings.Contains(err.Error(), `unknown setting "Nested.Step"`) {
		t.Errorf("Decode = %v, want unknown setting Nested.Step", err)
	}
	if err := (sdk.Config{Settings: map[string]any{"Nested": map[string]any{"step": 2.0}}}).Decode(&s); err != nil || inner.Step != 2 {
		t.Errorf("Decode = %v, step %d, want step 2", err, inner.Step)
	}
}

func TestDecodeTypeErrors(t *testing.T) {
	var s struct {
		Step  int             `json:"step"`
		Name  string          `json:"name"`
		On    bool            `json:"on"`
		Hosts []string        `json:"hosts"`
		Inner struct{ N int } `json:"inner"`
	}
	for _, tc := range []struct {
		settings map[string]any
		want     string
	}{
		{map[string]any{"step": "x"}, `setting "step": want a whole number, not a string`},
		{map[string]any{"step": 1.5}, `setting "step": want a whole number, not 1.5`},
		{map[string]any{"name": true}, `setting "name": want a string, not true or false`},
		{map[string]any{"on": []any{}}, `setting "on": want true or false, not a list`},
		{map[string]any{"hosts": map[string]any{}}, `setting "hosts": want a list, not a map`},
		{map[string]any{"inner": map[string]any{"N": "x"}}, `setting "inner.N": want a whole number, not a string`},
	} {
		err := sdk.Config{Settings: tc.settings}.Decode(&s)
		if err == nil || err.Error() != tc.want {
			t.Errorf("Decode(%v) = %v, want %s", tc.settings, err, tc.want)
		}
	}
}

func TestClosingViewsFailsWatch(t *testing.T) {
	inst := launch(t, "closer")
	stream := watch(t, inst, time.Minute)
	for {
		resp, err := stream.Recv()
		if err != nil {
			if status.Code(err) != codes.Unavailable {
				t.Errorf("Watch ended with %v, want UNAVAILABLE", err)
			}
			break
		}
		if body := resp.GetView().GetBody(); body != nil && body.GetText().GetValue() != "last" {
			t.Errorf("view %v, want the one sent", body)
		}
	}
	if _, err := inst.Client.Render(context.Background(), &tesserav1.RenderRequest{}); status.Code(err) != codes.Unavailable {
		t.Errorf("Render after close = %v, want UNAVAILABLE", err)
	}
}

func TestViewsOverLimitsAreDropped(t *testing.T) {
	inst := launch(t, "oversize")
	stream := watch(t, inst, time.Minute)
	for {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		body := resp.GetView().GetBody()
		if body == nil {
			continue // the empty first view, or a heartbeat
		}
		if got := body.GetText().GetValue(); got != "within" {
			t.Fatalf("got a view over a limit: %.40v", body)
		}
		return
	}
}

// TestCallbackStatusIsHidden keeps a status error that a callback passes
// on, from a gRPC client of its own, from steering the host: UNIMPLEMENTED
// would mark the plugin as not a tessera plugin for good.
func TestCallbackStatusIsHidden(t *testing.T) {
	inst := launch(t, "greeter")
	err := configure(t, inst, map[string]any{"greeting": "unimplemented"})
	if status.Code(err) != codes.Unknown {
		t.Errorf("Configure = %v, want UNKNOWN", err)
	}
}

func TestConfigureRejects(t *testing.T) {
	inst := launch(t, "greeter")
	for _, tc := range []struct {
		settings map[string]any
		want     string
	}{
		{map[string]any{"greeting": "bad"}, `greeting "bad" is not allowed`},
		{map[string]any{"colour": "red"}, `unknown setting "colour"`},
		{map[string]any{"Greeting": "hi"}, `unknown setting "Greeting"`},
		{map[string]any{"greeting": 7.0}, `setting "greeting": want a string, not a number`},
	} {
		err := configure(t, inst, tc.settings)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Configure(%v) = %v, want INVALID_ARGUMENT with %q", tc.settings, err, tc.want)
		}
	}
}
