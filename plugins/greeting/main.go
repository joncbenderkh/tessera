// SPDX-License-Identifier: GPL-3.0-or-later

// Command greeting is the greeting panel: a heading, a line of secondary
// text, and the host's name, all settings.
package main

import (
	"context"
	"os"
	"strings"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/sdk"
)

const schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "text": {
      "type": "string",
      "description": "The heading; \"\" hides it.",
      "default": "Your workspace, at a glance."
    },
    "secondary": {
      "type": "string",
      "description": "The line below the heading; \"\" hides it.",
      "default": "A quiet place for the signals that matter."
    },
    "host_info": {
      "type": "boolean",
      "description": "Show the name of this computer.",
      "default": true
    }
  },
  "additionalProperties": false
}`

type settings struct {
	Text      string `json:"text"`
	Secondary string `json:"secondary"`
	HostInfo  bool   `json:"host_info"`
}

var defaults = settings{
	Text:      "Your workspace, at a glance.",
	Secondary: "A quiet place for the signals that matter.",
	HostInfo:  true,
}

func main() {
	host, _ := os.Hostname()
	views := make(chan sdk.View)
	sdk.Serve(sdk.Plugin{
		Name:         "greeting",
		Version:      tessera.Version,
		Description:  "A greeting, with the name of this computer.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 4, Rows: 1},
		MinBody:      minBody,
		Views:        views,
		Configure: func(_ context.Context, cfg sdk.Config) error {
			s := defaults
			if err := cfg.Decode(&s); err != nil {
				return err
			}
			views <- view(s, host)
			return nil
		},
	})
}

// view shows the greeting, and host if it is set and s asks for it.
func view(s settings, host string) sdk.View {
	greeting := sdk.Column().Gap(sdk.SpacingSmall)
	if text := strings.TrimSpace(s.Text); text != "" {
		greeting.Add(sdk.Text(text).Size(sdk.SizeLarge))
	}
	if text := strings.TrimSpace(s.Secondary); text != "" {
		greeting.Add(sdk.Text(text).Tone(sdk.ToneMuted))
	}
	body := sdk.Row(sdk.Weighted(1, greeting)).Gap(sdk.SpacingLarge)
	if s.HostInfo && host != "" {
		body.Add(sdk.Column(
			sdk.Text("Local host").Size(sdk.SizeSmall).Tone(sdk.ToneMuted).Monospace(),
			sdk.Text(host).Monospace(),
		).Gap(sdk.SpacingExtraSmall))
	}
	return sdk.View{Title: "Greeting", Body: body}
}

// minBody is the least room the panel's view needs, in dp: room for
// the default greeting and its second line, each on one line.
var minBody = sdk.BodySize{Width: 300, Height: 40}
