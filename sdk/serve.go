// SPDX-License-Identifier: GPL-3.0-or-later

package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// Plugin describes a panel plugin and how it answers the host.
type Plugin struct {
	// Name is the plugin's stable name, such as "clock": 1 to 64
	// lowercase letters, digits, and hyphens, starting with a letter or
	// digit. It must equal the executable's file name (plus ".exe" on
	// Windows).
	Name string
	// Version is the plugin's version in semver form, such as "0.1.0".
	Version string
	// Description is one line for the panel picker.
	Description string
	// ConfigSchema is a JSON Schema (draft 2020-12) for the panel's
	// settings. Empty means the panel takes no settings.
	ConfigSchema string
	// DefaultSize is the size a new panel gets. A zero dimension means 1.
	DefaultSize Size

	// Views carries the panel's views; each one replaces the last. The
	// host is sent the newest, so a plugin need not wait for one to go out
	// before sending the next. A view sent belongs to the SDK: do not
	// change its widgets, table columns, or list items afterwards. Until
	// the first one arrives, the panel shows as starting; if none arrives
	// within half the heartbeat interval the host asks for (2.5 seconds),
	// the SDK sends an empty view, since the protocol requires one. A view
	// the host would refuse, over one of the limits in docs/protocol.md or
	// not encodable (a string of invalid UTF-8), is dropped, and the
	// reason logged to stderr, so the panel keeps the last view it
	// accepted.
	// Closing Views fails the plugin, and the host restarts it. Required.
	Views <-chan View

	// Configure, if set, is called with the panel's settings and size
	// right after the plugin starts, and again whenever they change. It
	// runs alongside the reading of Views and the event callbacks, so
	// guard the state they share. An error made by [InvalidSettings], or
	// returned by [Config.Decode], rejects the settings: the host shows
	// the panel as failed, with the message, until the user changes them.
	// Any other error fails the plugin, which the host restarts.
	Configure func(ctx context.Context, c Config) error
	// Activate, if set, is called when the user activates a table row or
	// list item with an action ID. Show its effect by sending a new view.
	Activate func(ctx context.Context, actionID string) error
	// Scroll, if set, is called when the visible rows of a table or list
	// with an ID change.
	Scroll func(ctx context.Context, e ScrollEvent) error
}

// Size is a panel's size in grid cells.
type Size struct {
	Columns, Rows int
}

// Config is a panel's settings and size, as the host passes them to
// [Plugin.Configure].
type Config struct {
	// PanelID is the panel's id from the config file.
	PanelID string
	// Settings is the panel's settings block, with the JSON data model:
	// objects are map[string]any, arrays []any, and every number a
	// float64. The host does not validate them against the schema yet.
	Settings map[string]any
	// Size is the panel's size in grid cells.
	Size Size
}

// Decode stores the settings in the struct v points to, as
// encoding/json would decode them from a JSON object, except that a
// setting's name must equal the field's JSON name exactly: its json tag,
// or else the field's own name. A setting v has no field for, or one of
// the wrong type, is an error that rejects the settings (see
// [InvalidSettings]). On an error, v may be partly filled, so decode
// into a fresh value rather than the plugin's live settings.
//
// v must be a non-nil pointer to a struct, a map, or an interface;
// anything else is a plain error, since the fault is the plugin's, not
// the settings'.
func (c Config) Decode(v any) error {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("sdk: Decode needs a non-nil pointer, not %T", v)
	}
	t := rv.Type()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if k := t.Kind(); k != reflect.Struct && k != reflect.Map && k != reflect.Interface {
		return fmt.Errorf("sdk: Decode needs a pointer to a struct, a map, or an interface, not %T", v)
	}
	if problem := checkKeys(c.Settings, rv, rv.Type(), ""); problem != "" {
		return InvalidSettings("%s", problem)
	}
	data, err := json.Marshal(c.Settings)
	if err != nil {
		return InvalidSettings("%w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		// encoding/json's own path leaves out map keys, and list indices
		// before Go 1.27, and names no setting for a type that decodes
		// itself.
		prefix := ""
		if path := failingKey(c.Settings, rv.Type(), ""); path != "" {
			prefix = fmt.Sprintf("setting %q: ", path)
		}
		if typeErr := (*json.UnmarshalTypeError)(nil); errors.As(err, &typeErr) {
			want := describeType(typeErr.Type)
			if r := numberRange(typeErr.Type, typeErr.Value); r != "" {
				want = r
			}
			return InvalidSettings("%swant %s, not %s%.0w", prefix, want, describeValue(typeErr.Value), err)
		}
		return InvalidSettings("%s%w", prefix, err)
	}
	return nil
}

// describeType names what a setting of type t must be, for the user.
func describeType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.String:
		return "a string"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map, reflect.Struct:
		return "a map"
	}
	return t.String()
}

// numberRange names the numbers a numeric type t holds, when v, as an
// UnmarshalTypeError names it, is a number of the right kind that lies
// outside them, or returns "".
func numberRange(t reflect.Type, v string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if wholeNumber(v) {
			return fmt.Sprintf("a whole number from %d to %d", int64(-1)<<(t.Bits()-1), int64(1)<<(t.Bits()-1)-1)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if wholeNumber(v) {
			return fmt.Sprintf("a whole number from 0 to %d", uint64(math.MaxUint64)>>(64-t.Bits()))
		}
	case reflect.Float32:
		if strings.HasPrefix(v, "number ") {
			return fmt.Sprintf("a number from %.2g to %.2g", -math.MaxFloat32, math.MaxFloat32)
		}
	}
	return ""
}

// wholeNumber reports whether v, as an UnmarshalTypeError names it, is
// a whole number.
func wholeNumber(v string) bool {
	n, ok := strings.CutPrefix(v, "number ")
	if !ok {
		return false
	}
	f, err := strconv.ParseFloat(n, 64)
	return err == nil && f == math.Trunc(f)
}

// describeValue names the JSON value encoding/json reports in an
// UnmarshalTypeError: "string", "number 7", and so on.
func describeValue(v string) string {
	if n, ok := strings.CutPrefix(v, "number "); ok {
		return n
	}
	switch v {
	case "bool":
		return "a boolean"
	case "array":
		return "a list"
	case "object":
		return "a map"
	}
	return "a " + v
}

// ScrollEvent reports which rows of a table or list are visible.
type ScrollEvent struct {
	// WidgetID is the ID of the scrolled table or list.
	WidgetID string
	// First is the index of the first visible row or item.
	First int
	// Count is the number of rows or items visible.
	Count int
}

// InvalidSettings returns an error that rejects a panel's settings,
// formatted as [fmt.Errorf] does. The host shows its message to the user.
func InvalidSettings(format string, args ...any) error {
	return settingsError{fmt.Errorf(format, args...)}
}

type settingsError struct{ error }

func (e settingsError) Unwrap() error { return e.error }

// Serve serves p to the host until the host shuts the plugin down. Until
// it is called, stdout carries the handshake, so the plugin must write
// nothing there before; it may log to stderr. A plugin started by hand,
// not by the host, prints a short message and exits with status 1.
func Serve(p Plugin) {
	if p.Views == nil {
		panic("sdk: Plugin.Views is nil")
	}
	s := newServer(p)
	go s.receive(p.Views)
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: plugin.HandshakeConfig{
			ProtocolVersion:  tesserav1.ProtocolVersion,
			MagicCookieKey:   tesserav1.MagicCookieKey,
			MagicCookieValue: tesserav1.MagicCookieValue,
		},
		Plugins:    plugin.PluginSet{"panel": panelPlugin{server: s}},
		GRPCServer: plugin.DefaultGRPCServer,
		Logger:     hclog.New(&hclog.LoggerOptions{Output: os.Stderr, Level: hclog.Warn}),
	})
}

// panelPlugin is the plugin side of PanelService for go-plugin.
type panelPlugin struct {
	plugin.NetRPCUnsupportedPlugin
	server *server
}

func (p panelPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	tesserav1.RegisterPanelServiceServer(s, p.server)
	return nil
}

func (panelPlugin) GRPCClient(context.Context, *plugin.GRPCBroker, *grpc.ClientConn) (any, error) {
	return nil, errors.New("sdk: a plugin does not call PanelService")
}

// server answers PanelService for a Plugin. It keeps only the latest
// view, which every Watch stream and Render reads.
type server struct {
	tesserav1.UnimplementedPanelServiceServer
	plugin Plugin

	mu      sync.Mutex
	view    *tesserav1.View
	seq     uint64        // counts views received
	changed chan struct{} // closed, and replaced, when a view arrives
	closed  bool          // Plugin.Views is closed; changed stays closed
}

func newServer(p Plugin) *server {
	return &server{plugin: p, changed: make(chan struct{})}
}

// receive keeps the latest view from views, dropping any the host would
// refuse, and records when views is closed.
func (s *server) receive(views <-chan View) {
	for v := range views {
		pv := v.proto()
		if err := checkLimits(pv); err != nil {
			log.Printf("sdk: view dropped: %v", err)
			continue
		}
		s.mu.Lock()
		s.view = pv
		s.seq++
		close(s.changed)
		s.changed = make(chan struct{})
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.closed = true
	close(s.changed)
	s.mu.Unlock()
}

// errClosed ends Watch once Plugin.Views is closed, so the host
// restarts the plugin; Render reports it too.
var errClosed = status.Error(codes.Unavailable, "sdk: the plugin closed its views channel")

// checkLimits returns why the host would refuse v, if it would.
func checkLimits(v *tesserav1.View) error {
	if err := checkWidgets(v.GetBody()); err != nil {
		return err
	}
	b, err := proto.Marshal(&tesserav1.WatchResponse{Content: &tesserav1.WatchResponse_View{View: v}})
	if err != nil {
		return err
	}
	if len(b) > tesserav1.MaxViewBytes {
		return fmt.Errorf("%d bytes encoded, over the limit of %d", len(b), tesserav1.MaxViewBytes)
	}
	return nil
}

// checkWidgets checks body's widgets against the limits on depth, count,
// and columns. Unlike [tesserav1.Measure], it stops at the first limit
// exceeded, so a widget shared many times over, or a cycle, ends it.
func checkWidgets(body *tesserav1.Widget) error {
	count := 0
	var walk func(w *tesserav1.Widget, depth int) error
	walk = func(w *tesserav1.Widget, depth int) error {
		if w == nil {
			return nil
		}
		count++
		switch n := len(w.GetTable().GetColumns()); {
		case depth > tesserav1.MaxViewDepth:
			return fmt.Errorf("widgets nested over the limit of %d deep", tesserav1.MaxViewDepth)
		case count > tesserav1.MaxViewWidgets:
			return fmt.Errorf("over the limit of %d widgets", tesserav1.MaxViewWidgets)
		case n > tesserav1.MaxTableColumns:
			return fmt.Errorf("a table of %d columns, over the limit of %d", n, tesserav1.MaxTableColumns)
		}
		for _, item := range w.GetStack().GetItems() {
			if err := walk(item.GetWidget(), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(body, 1)
}

// latest returns the latest view, its sequence number (0 before the
// first), and a channel closed when a newer one arrives, or errClosed
// once Plugin.Views is closed.
func (s *server) latest() (*tesserav1.View, uint64, <-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0, nil, errClosed
	}
	return s.view, s.seq, s.changed, nil
}

// Describe always picks view schema 1, the only one the SDK builds: a
// host that does not offer it then refuses the plugin for good, where an
// error here would have it retried.
func (s *server) Describe(context.Context, *tesserav1.DescribeRequest) (*tesserav1.DescribeResponse, error) {
	return &tesserav1.DescribeResponse{
		Name:            s.plugin.Name,
		Version:         s.plugin.Version,
		Description:     s.plugin.Description,
		ProtocolVersion: tesserav1.ProtocolVersion,
		ViewVersion:     tesserav1.ViewVersion,
		ConfigSchema:    s.plugin.ConfigSchema,
		DefaultSize:     s.plugin.DefaultSize.proto(),
	}, nil
}

func (s *server) Configure(ctx context.Context, req *tesserav1.ConfigureRequest) (*tesserav1.ConfigureResponse, error) {
	if s.plugin.Configure == nil {
		return &tesserav1.ConfigureResponse{}, nil
	}
	c := Config{
		PanelID:  req.GetPanelId(),
		Settings: req.GetSettings().AsMap(),
		Size:     Size{Columns: int(req.GetSize().GetColumns()), Rows: int(req.GetSize().GetRows())},
	}
	err := s.plugin.Configure(ctx, c)
	if se := (settingsError{}); errors.As(err, &se) {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err != nil {
		return nil, unknown(err)
	}
	return &tesserav1.ConfigureResponse{}, nil
}

// Watch sends the latest view whenever it changes, and a heartbeat when
// half the heartbeat interval passes without one. A plugin that has sent
// no view by then, or at once without heartbeats, gets an empty one,
// since the first response must carry a view.
func (s *server) Watch(req *tesserav1.WatchRequest, stream tesserav1.PanelService_WatchServer) error {
	ctx := stream.Context()
	var beat <-chan time.Time
	var timer *time.Timer
	interval := req.GetHeartbeatInterval().AsDuration()
	if interval > 0 {
		interval = max(interval/2, minBeat)
		timer = time.NewTimer(interval)
		defer timer.Stop()
		beat = timer.C
	}
	var viewSent bool
	send := func(v *tesserav1.View) error {
		if timer != nil {
			timer.Reset(interval)
		}
		resp := &tesserav1.WatchResponse{}
		if v != nil {
			resp.Content = &tesserav1.WatchResponse_View{View: v}
			viewSent = true
		}
		return stream.Send(resp)
	}
	var sent uint64
	for {
		view, seq, changed, err := s.latest()
		if err != nil {
			return err
		}
		if seq != sent {
			if err := send(view); err != nil {
				return err
			}
			sent = seq
			continue
		}
		if !viewSent && timer == nil {
			if err := send(&tesserav1.View{}); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		case <-beat:
			var v *tesserav1.View
			if !viewSent {
				v = &tesserav1.View{}
			}
			if err := send(v); err != nil {
				return err
			}
		}
	}
}

// minBeat bounds how often Watch sends heartbeats, however short the
// interval the host asks for.
const minBeat = 10 * time.Millisecond

func (s *server) Render(context.Context, *tesserav1.RenderRequest) (*tesserav1.RenderResponse, error) {
	view, _, _, err := s.latest()
	if err != nil {
		return nil, err
	}
	if view == nil {
		view = &tesserav1.View{}
	}
	return &tesserav1.RenderResponse{Content: &tesserav1.RenderResponse_View{View: view}}, nil
}

func (s *server) HandleEvent(ctx context.Context, req *tesserav1.HandleEventRequest) (*tesserav1.HandleEventResponse, error) {
	var err error
	switch e := req.GetEvent().(type) {
	case *tesserav1.HandleEventRequest_Activate:
		if s.plugin.Activate != nil {
			err = s.plugin.Activate(ctx, e.Activate.GetActionId())
		}
	case *tesserav1.HandleEventRequest_Scroll:
		if s.plugin.Scroll != nil {
			err = s.plugin.Scroll(ctx, ScrollEvent{
				WidgetID: e.Scroll.GetWidgetId(),
				First:    int(e.Scroll.GetFirstVisible()),
				Count:    int(e.Scroll.GetVisibleCount()),
			})
		}
	}
	if err != nil {
		return nil, unknown(err)
	}
	return &tesserav1.HandleEventResponse{}, nil
}

// unknown reports a callback's error as UNKNOWN, so that a gRPC status it
// passes on from a client of its own does not tell the host how to treat
// the plugin.
func unknown(err error) error {
	return status.Error(codes.Unknown, err.Error())
}

func (s Size) proto() *tesserav1.GridSize {
	if s == (Size{}) {
		return nil
	}
	return &tesserav1.GridSize{Columns: clampUint32(s.Columns), Rows: clampUint32(s.Rows)}
}

func clampUint32(n int) uint32 {
	return uint32(min(uint64(max(n, 0)), math.MaxUint32))
}
