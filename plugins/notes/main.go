// SPDX-License-Identifier: GPL-3.0-or-later

// Command notes is the notes panel: a note with a title and a body,
// both settings.
package main

import (
	"context"
	"strings"
	"unicode"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/sdk"
)

const schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "title": {"type": "string", "description": "The note's title."},
    "body": {"type": "string", "description": "The note's text."}
  },
  "additionalProperties": false
}`

type settings struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func main() {
	views := make(chan sdk.View)
	sdk.Serve(sdk.Plugin{
		Name:         "notes",
		Version:      tessera.Version,
		Description:  "A note you write in the panel's settings.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 2, Rows: 1},
		Views:        views,
		Configure: func(_ context.Context, cfg sdk.Config) error {
			var s settings
			if err := cfg.Decode(&s); err != nil {
				return err
			}
			views <- view(s)
			return nil
		},
	})
}

// view shows the note, or how to write one if it is empty.
func view(s settings) sdk.View {
	title, body := strings.TrimSpace(s.Title), trimBlankLines(s.Body)
	if title == "" && strings.TrimSpace(body) == "" {
		return sdk.View{
			Title: "Notes",
			Body:  sdk.Text("No note yet. Set its title and body in the panel's settings.").Tone(sdk.ToneMuted),
		}
	}
	note := sdk.Column().Gap(sdk.SpacingSmall)
	if title != "" {
		note.Add(sdk.Text(title))
	}
	if strings.TrimSpace(body) != "" {
		note.Add(sdk.Text(body).Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
	}
	return sdk.View{Title: "Notes", Body: note}
}

// trimBlankLines drops the blank lines that start and end s, and the
// spaces that end it, but keeps the first line's indentation.
func trimBlankLines(s string) string {
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	for {
		line, rest, found := strings.Cut(s, "\n")
		if !found || strings.TrimSpace(line) != "" {
			return s
		}
		s = rest
	}
}
