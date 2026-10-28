// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
)

func TestMain(m *testing.M) { plugintest.Main(m, main) }

func TestView(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    settings
		host string
		want []string
	}{
		{"defaults", defaults, "tessera-dev", []string{defaults.Text, defaults.Secondary, "Local host", "tessera-dev"}},
		{"no host info", settings{Text: "Hi", Secondary: "there"}, "tessera-dev", []string{"Hi", "there"}},
		{"unknown host", defaults, "", []string{defaults.Text, defaults.Secondary}},
		{"blank text", settings{Text: " ", Secondary: "only this", HostInfo: true}, "h", []string{"only this", "Local host", "h"}},
	} {
		if got := plugintest.Texts(view(tc.s, tc.host).Proto().GetBody()); !slices.Equal(got, tc.want) {
			t.Errorf("%s: view = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestGreeting(t *testing.T) {
	p := plugintest.Launch(t, "greeting")
	if err := p.Configure(map[string]any{"heading": "Hi"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("unknown setting: %v, want INVALID_ARGUMENT", err)
	}
	if err := p.Configure(map[string]any{"text": "Hello", "host_info": false}); err != nil {
		t.Fatal(err)
	}
	p.AwaitTexts("Hello", defaults.Secondary)
}
