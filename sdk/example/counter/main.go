// SPDX-License-Identifier: GPL-3.0-or-later

// Command counter is a complete tessera plugin built with the SDK: a
// counter the user steps up or resets from a list, with how long the
// panel has been up. Its step is a setting. docs/plugins.md walks
// through it.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/joncbenderkh/tessera/sdk"
)

const schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "step": {"type": "integer", "minimum": 1, "default": 1}
  },
  "additionalProperties": false
}`

type settings struct {
	Step int `json:"step"`
}

// counter is the panel's state. Configure, the event callbacks, and the
// ticker run concurrently, so mu guards it, and every change sends a new
// view.
type counter struct {
	views   chan<- sdk.View
	started time.Time

	mu    sync.Mutex
	step  int
	count int
}

func main() {
	views := make(chan sdk.View)
	c := &counter{views: views, started: time.Now(), step: 1}
	go c.tick()
	sdk.Serve(sdk.Plugin{
		Name:         "counter",
		Version:      "0.1.0",
		Description:  "A counter: the tessera SDK's example plugin.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 2, Rows: 2},
		Views:        views,
		Configure:    c.configure,
		Activate:     c.activate,
	})
}

func (c *counter) configure(_ context.Context, cfg sdk.Config) error {
	s := settings{Step: 1}
	if err := cfg.Decode(&s); err != nil {
		return err
	}
	if s.Step < 1 {
		return sdk.InvalidSettings("step must be at least 1, not %d", s.Step)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.step = s.Step
	c.show()
	return nil
}

func (c *counter) activate(_ context.Context, actionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch actionID {
	case "add":
		c.count += c.step
	case "reset":
		c.count = 0
	default:
		return fmt.Errorf("unknown action %q", actionID)
	}
	c.show()
	return nil
}

// tick shows the view right away, then again every second for the
// uptime.
func (c *counter) tick() {
	for {
		c.mu.Lock()
		c.show()
		c.mu.Unlock()
		time.Sleep(time.Second)
	}
}

// show sends the current view. c.mu is held.
func (c *counter) show() {
	up := time.Since(c.started).Truncate(time.Second)
	c.views <- sdk.View{
		Title: "Counter",
		Body: sdk.Column(
			sdk.Stat("Count", fmt.Sprint(c.count)).Caption(fmt.Sprintf("up %v", up)),
			sdk.List(
				sdk.Item(fmt.Sprintf("Add %d", c.step)).Action("add"),
				sdk.Item("Reset").Detail("Back to 0").Action("reset"),
			),
		),
	}
}
