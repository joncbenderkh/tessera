// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"image/color"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/joncbenderkh/tessera/internal/palette"
)

const themesHead = "version: 1\ngrid:\n  columns: 1\n  row_height: 180\npanels: []\n"

func TestParseThemes(t *testing.T) {
	c, err := Parse([]byte(themesHead + `theme: dusk
themes:
  dark:
    accent: oklch(0.7 0.15 300)
  dusk:
    base: light
    bg: "#1E1e2e"
    fg: "#fff"
  plain: {}
  night:
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Theme != "dusk" {
		t.Fatalf("theme %q, want dusk", c.Theme)
	}
	want := map[Theme]ThemeColours{
		ThemeDark: {Accent: "oklch(0.7 0.15 300)"},
		"dusk":    {Base: ThemeLight, Bg: "#1E1e2e", Fg: "#fff"},
		"plain":   {},
		"night":   {},
	}
	if !reflect.DeepEqual(c.Themes, want) {
		t.Fatalf("themes %#v, want %#v", c.Themes, want)
	}
	data, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"  dark:\n    accent: \"oklch(0.7 0.15 300)\"\n", `    bg: "#1E1e2e"`, "  night: {}\n"} {
		if !strings.Contains(string(data), line) {
			t.Errorf("Marshal wrote %q, which lacks %q", data, line)
		}
	}
	again, err := Parse(data)
	if err != nil {
		t.Fatalf("reparsing %q: %v", data, err)
	}
	if !reflect.DeepEqual(again, c) {
		t.Fatalf("round trip gave %#v, want %#v", again, c)
	}

	p, dark := c.Palette("dusk")
	wantP := palette.Light
	wantP.Bg = color.NRGBA{0x1e, 0x1e, 0x2e, 255}
	wantP.Fg = color.NRGBA{255, 255, 255, 255}
	if dark || p != wantP {
		t.Errorf("dusk: %v, dark %t; want light with bg and fg replaced", p, dark)
	}
	wantNight := palette.Dark
	wantNight.Accent = palette.OKLCH(.7, .15, 300)
	if p, dark := c.Palette("night"); !dark || p != wantNight {
		t.Errorf("night: %v, dark %t; want the dark palette with the config's accent for dark", p, dark)
	}
	if p, dark := c.Palette(""); !dark || p.Accent != palette.OKLCH(.7, .15, 300) || p.Bg != palette.Dark.Bg {
		t.Errorf("default theme: %v, dark %t; want dark with its accent replaced", p, dark)
	}
	if p, dark := c.Palette(ThemeLight); dark || p != palette.Light {
		t.Errorf("light: %v, dark %t; want the light palette", p, dark)
	}
}

func TestParseThemesErrors(t *testing.T) {
	for _, tt := range []struct {
		name, yaml, err string
	}{
		{"undefined", "theme: dusk\n", `theme must be "dark", "light", or a theme under themes, got "dusk"`},
		{"unknown token", "themes:\n  dark:\n    background: '#000'\n", "background"},
		{"bad colour", "themes:\n  dark:\n    bg: red\n", `themes.dark.bg: "red" is not a colour`},
		{"bad lightness", "themes:\n  dusk:\n    fg: oklch(2 0 0)\n", "themes.dusk.fg: \"oklch(2 0 0)\": lightness must be"},
		{"unquoted hex", "themes:\n  dark:\n    bg: #000\n", `line 8: themes.dark.bg has no value; quote a hex colour`},
		{"null base", "themes:\n  dusk:\n    base:\n", "themes.dusk.base has no value"},
		{"base on built-in", "themes:\n  light:\n    base: dark\n", "themes.light: base applies only to custom themes"},
		{"bad base", "themes:\n  dusk:\n    base: dusk\n", `themes.dusk: base must be "dark" or "light", got "dusk"`},
		{"capital name", "themes:\n  Dusk: {}\n", `name "Dusk" must be lowercase`},
		{"digit first", "themes:\n  1dusk: {}\n", `name "1dusk" must be lowercase`},
		{"long name", "themes:\n  " + strings.Repeat("a", 33) + ": {}\n", "at most 32 characters"},
		{"not a mapping", "themes:\n  dusk: [bg]\n", "dusk"},
		{"empty token", "themes:\n  dark:\n    bg: ''\n", "line 8: themes.dark.bg is empty; leave it out to keep the base's colour"},
		{"empty block scalar", "themes:\n  dusk:\n    fg: |-\n", "line 8: themes.dusk.fg is empty"},
		{"empty folded base", "themes:\n  dusk:\n    base: >-\n", "line 8: themes.dusk.base is empty"},
		{"merge key in a theme named settings", "themes:\n  settings:\n    <<:\n      bg:\n", "line 8: themes.settings: merge keys are not supported in themes"},
		{"merge key", "themes:\n  dusk:\n    <<: {fg: '#111'}\n", "merge keys are not supported"},
		{"tagged name", "themes:\n  !!null x: {}\n", "line 7: themes: a theme's name may not use the !!null tag"},
		{"hex as a number", "themes:\n  dark:\n    bg: 0x1e1e2e\n", "line 8: themes.dark.bg: 0x1e1e2e is not a colour; quote it"},
		{"number base", "themes:\n  dusk:\n    base: 1\n", `line 8: themes.dusk.base: 1 must be "dark" or "light"`},
		{"yes base", "themes:\n  dusk:\n    base: yes\n", `base must be "dark" or "light", got "yes"`},
		{"empty base", "themes:\n  dusk:\n    base: \"\"\n", "line 8: themes.dusk.base is empty; leave it out to use dark"},
		{"empty base on built-in", "themes:\n  dark:\n    base: ''\n", "themes.dark.base is empty"},
		{"unknown token without a value", "themes:\n  dark:\n    background:\n", `unknown field "background"`},
		{"name as written", "themes:\n  0o17: {}\n", `line 7: themes: name "0o17" must be lowercase`},
		{"null name", "themes:\n  null: {}\n", "line 7: themes: a theme's name is null"},
		{"tilde name", "themes:\n  ~:\n    bg: '#000'\n", "line 7: themes: a theme's name is null"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(themesHead + tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("error %v does not contain %q", err, tt.err)
			}
		})
	}
}

// TestThemeNamesAsWritten checks names a YAML 1.1 reader takes for
// booleans or null keep their text, as the theme field names them.
func TestThemeNamesAsWritten(t *testing.T) {
	for _, name := range []string{"on", "off", "yes", "no", "y", "n", "true", `"null"`} {
		t.Run(name, func(t *testing.T) {
			c, err := Parse([]byte(themesHead + "theme: " + name + "\nthemes:\n  " + name + ":\n    bg: '#000'\n"))
			if err != nil {
				t.Fatal(err)
			}
			name := strings.Trim(name, `"`)
			if _, ok := c.Themes[Theme(name)]; !ok || c.Theme != Theme(name) {
				t.Fatalf("theme %q, themes %v; want %q in both", c.Theme, c.Themes, name)
			}
			data, err := Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if again, err := Parse(data); err != nil || !reflect.DeepEqual(again, c) {
				t.Fatalf("round trip of %q gave %v, %v", data, again, err)
			}
		})
	}
}

func TestParseEmptyThemes(t *testing.T) {
	c, err := Parse([]byte(themesHead + "themes: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Themes != nil {
		t.Fatalf("themes %#v, want nil, as a round trip gives", c.Themes)
	}
}

func TestNextTheme(t *testing.T) {
	c := Default()
	c.Themes = map[Theme]ThemeColours{"zest": {}, ThemeLight: {Bg: "#fff"}, "amber": {}}
	if got, want := c.ThemeNames(), []Theme{ThemeDark, ThemeLight, "amber", "zest"}; !slices.Equal(got, want) {
		t.Fatalf("ThemeNames() = %v, want %v", got, want)
	}
	for _, tt := range []struct{ from, want Theme }{
		{"", ThemeLight}, {ThemeDark, ThemeLight}, {ThemeLight, "amber"}, {"amber", "zest"}, {"zest", ThemeDark}, {"gone", ThemeDark},
	} {
		if got := c.NextTheme(tt.from); got != tt.want {
			t.Errorf("NextTheme(%q) = %q, want %q", tt.from, got, tt.want)
		}
	}
	if got := Default().NextTheme(ThemeLight); got != ThemeDark {
		t.Errorf("without custom themes, NextTheme(light) = %q, want dark", got)
	}
}

func TestContrastWarnings(t *testing.T) {
	c := Default()
	if w := c.ContrastWarnings(); len(w) != 0 {
		t.Fatalf("built-in themes warn: %v", w)
	}
	c.Themes = map[Theme]ThemeColours{
		"dusk":     {Base: ThemeLight, Muted: "#eee"},
		ThemeDark:  {Accent: "#000"},
		ThemeLight: {Bg: "#fff"},
		"night":    {},                // dark's accent, reported for dark alone
		"pitch":    {Surface: "#000"}, // dark's accent on its own surface
	}
	got := c.ContrastWarnings()
	want := []string{
		"theme dark: accent on bg has contrast",
		"theme dark: accent on surface has contrast",
		"theme dusk: muted on bg has contrast",
		"theme dusk: muted on surface has contrast",
		"theme pitch: accent on surface has contrast",
	}
	if len(got) != len(want) {
		t.Fatalf("warnings %q, want %d", got, len(want))
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("warning %d is %q, want it to start %q", i, got[i], want[i])
		}
	}
}
