// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"image"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/sdk"
)

func TestMain(m *testing.M) { plugintest.Main(m, main) }

func TestView(t *testing.T) {
	for _, tc := range []struct {
		s    settings
		want []string
	}{
		{settings{Title: "Plan", Body: "Ship it.\n"}, []string{"Plan", "Ship it."}},
		{settings{Body: "Body only"}, []string{"Body only"}},
		{settings{Title: "Title only", Body: "\n"}, []string{"Title only"}},
		{settings{Body: "\n  - one\n  - two\n\n"}, []string{"  - one\n  - two"}},
		{settings{Body: " \r\n\n  indented\n\nafter a gap"}, []string{"  indented\n\nafter a gap"}},
		{settings{Title: "T", Body: " \n"}, []string{"T"}},
		{settings{Title: " "}, []string{"No note yet. Set its title and body in the panel's settings."}},
	} {
		if got := plugintest.Texts(view(tc.s).Proto().GetBody()); !slices.Equal(got, tc.want) {
			t.Errorf("view(%+v) = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestNotes(t *testing.T) {
	p := plugintest.Launch(t, "notes")
	if err := p.Configure(map[string]any{"title": 1.0}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("numeric title: %v, want INVALID_ARGUMENT", err)
	}
	if err := p.Configure(map[string]any{"title": "Plan", "body": "Ship it."}); err != nil {
		t.Fatal(err)
	}
	p.AwaitTexts("Plan", "Ship it.")
}

// The views fit the least body the plugin asks for, minBody, at the
// default text size.
func TestMinBody(t *testing.T) {
	// From the narrowest body a tile has, or the least width it asks for.
	for _, width := range []int{124, 168, 300, 432, 600, 900} {
		testMinBodyAt(t, max(width, minBody.Width))
	}
}

func testMinBodyAt(t *testing.T, width int) {
	t.Helper()
	for i, v := range minBodyViews() {
		if !ui.ViewFits(v.Proto(), image.Pt(width, minBody.Height), 1) {
			t.Errorf("view %d does not fit %d by %d dp", i, width, minBody.Height)
		}
	}
}

// minBodyViews are the views minBody is measured with.
func minBodyViews() []sdk.View {
	return []sdk.View{view(settings{Title: "Plan", Body: "Ship it."}), view(settings{})}
}
