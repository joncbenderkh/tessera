// SPDX-License-Identifier: GPL-3.0-or-later

// Package plugintest tests a plugin end to end: the test binary, started
// by the host, serves the plugin, and the test launches a copy of itself
// through internal/pluginhost as the host does.
package plugintest

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// Main runs serve if the host started the test binary as a plugin, and
// the tests otherwise. Call it from TestMain with the plugin's main.
func Main(m *testing.M, serve func()) {
	if os.Getenv(tesserav1.MagicCookieKey) != "" {
		serve()
		return
	}
	os.Exit(m.Run())
}

// Plugin is a launched copy of the test binary.
type Plugin struct {
	*pluginhost.Instance
	t      testing.TB
	ctx    context.Context
	stream tesserav1.PanelService_WatchClient
}

// Launch installs the test binary as the plugin name in a temporary
// plugins directory and launches it. It is closed when the test ends.
func Launch(t testing.TB, name string) *Plugin {
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
	file := name
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, file), data, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	inst, err := pluginhost.Launch(ctx, dir, name, func(line string) { t.Log(line) }, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Close() })
	return &Plugin{Instance: inst, t: t, ctx: ctx}
}

// Context is the launch's context, which ends 20 seconds after it, for
// calls the helpers do not make.
func (p *Plugin) Context() context.Context { return p.ctx }

// Configure sends the panel's settings and returns the plugin's answer.
func (p *Plugin) Configure(settings map[string]any) error {
	p.t.Helper()
	s, err := structpb.NewStruct(settings)
	if err != nil {
		p.t.Fatal(err)
	}
	_, err = p.Client.Configure(p.ctx, &tesserav1.ConfigureRequest{PanelId: "p", Settings: s})
	return err
}

// Activate sends the plugin an activate event with actionID and
// returns its answer.
func (p *Plugin) Activate(actionID string) error {
	p.t.Helper()
	_, err := p.Client.HandleEvent(p.ctx, &tesserav1.HandleEventRequest{
		Event: &tesserav1.HandleEventRequest_Activate{Activate: &tesserav1.ActivateEvent{ActionId: actionID}},
	})
	return err
}

// Await reads views from the plugin's Watch stream, which the first
// call opens, until one satisfies ok. It fails the test, with the view
// last seen, if none does before the plugin's deadline.
func (p *Plugin) Await(ok func(v *tesserav1.View) bool) {
	p.t.Helper()
	if p.stream == nil {
		stream, err := p.Client.Watch(p.ctx, &tesserav1.WatchRequest{HeartbeatInterval: durationpb.New(5 * time.Second)})
		if err != nil {
			p.t.Fatal(err)
		}
		p.stream = stream
	}
	var last *tesserav1.View
	for {
		resp, err := p.stream.Recv()
		if err != nil {
			p.t.Fatalf("%v; last view: %v", err, last)
		}
		if v := resp.GetView(); v != nil {
			if last = v; ok(v) {
				return
			}
		}
	}
}

// AwaitTexts reads views until one whose text widgets are want, in
// order; see Await.
func (p *Plugin) AwaitTexts(want ...string) {
	p.t.Helper()
	p.Await(func(v *tesserav1.View) bool { return slices.Equal(Texts(v.GetBody()), want) })
}

// Texts returns the values of the text widgets in w, depth first.
func Texts(w *tesserav1.Widget) []string {
	if t := w.GetText(); t != nil {
		return []string{t.GetValue()}
	}
	var texts []string
	for _, item := range w.GetStack().GetItems() {
		texts = append(texts, Texts(item.GetWidget())...)
	}
	return texts
}
