// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// Started by the host, the test binary is the counter plugin.
func TestMain(m *testing.M) {
	if os.Getenv(tesserav1.MagicCookieKey) != "" {
		main()
		return
	}
	os.Exit(m.Run())
}

func TestCounter(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "counter"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	inst, err := pluginhost.Launch(ctx, dir, "counter", func(line string) { t.Log(line) }, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()

	configure := func(settings map[string]any) error {
		s, err := structpb.NewStruct(settings)
		if err != nil {
			t.Fatal(err)
		}
		_, err = inst.Client.Configure(ctx, &tesserav1.ConfigureRequest{PanelId: "c", Settings: s})
		return err
	}
	if err := configure(map[string]any{"step": 0.0}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("step 0: %v, want INVALID_ARGUMENT", err)
	}
	if err := configure(map[string]any{"step": 5.0}); err != nil {
		t.Fatal(err)
	}

	stream, err := inst.Client.Watch(ctx, &tesserav1.WatchRequest{HeartbeatInterval: durationpb.New(5 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	// count returns the count shown once the add item says "Add 5".
	count := func() string {
		for {
			resp, err := stream.Recv()
			if err != nil {
				t.Fatal(err)
			}
			items := resp.GetView().GetBody().GetStack().GetItems()
			if len(items) == 2 && items[1].GetWidget().GetList().GetItems()[0].GetTitle() == "Add 5" {
				return items[0].GetWidget().GetStat().GetValue()
			}
		}
	}
	if got := count(); got != "0" {
		t.Fatalf("count %s, want 0", got)
	}
	activate := &tesserav1.HandleEventRequest{Event: &tesserav1.HandleEventRequest_Activate{Activate: &tesserav1.ActivateEvent{ActionId: "add"}}}
	if _, err := inst.Client.HandleEvent(ctx, activate); err != nil {
		t.Fatal(err)
	}
	for got := count(); got != "5"; got = count() {
		if got != "0" {
			t.Fatalf("count %s after adding 5, want 5", got)
		}
	}
}
