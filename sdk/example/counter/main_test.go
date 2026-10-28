// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// Started by the host, the test binary is the counter plugin.
func TestMain(m *testing.M) { plugintest.Main(m, main) }

func TestCounter(t *testing.T) {
	p := plugintest.Launch(t, "counter")
	if err := p.Configure(map[string]any{"step": 0.0}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("step 0: %v, want INVALID_ARGUMENT", err)
	}
	if err := p.Configure(map[string]any{"step": 5.0}); err != nil {
		t.Fatal(err)
	}

	// count returns the count shown, if the add item says "Add 5".
	count := func(v *tesserav1.View) (string, bool) {
		items := v.GetBody().GetStack().GetItems()
		if len(items) == 2 && items[1].GetWidget().GetList().GetItems()[0].GetTitle() == "Add 5" {
			return items[0].GetWidget().GetStat().GetValue(), true
		}
		return "", false
	}
	p.Await(func(v *tesserav1.View) bool {
		got, ok := count(v)
		if ok && got != "0" {
			t.Fatalf("count %s, want 0", got)
		}
		return ok
	})
	activate := &tesserav1.HandleEventRequest{Event: &tesserav1.HandleEventRequest_Activate{Activate: &tesserav1.ActivateEvent{ActionId: "add"}}}
	if _, err := p.Client.HandleEvent(p.Context(), activate); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		got, ok := count(v)
		if ok && got != "0" && got != "5" {
			t.Fatalf("count %s after adding 5, want 5", got)
		}
		return got == "5"
	})
}
