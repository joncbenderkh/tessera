// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml/ast"

	"github.com/joncbenderkh/tessera/internal/palette"
)

// ThemeColours is a theme under the config's themes: the built-in theme
// it starts from and the colour tokens it replaces, each written as
// palette.Parse reads it. An empty token keeps the base's colour, as
// the config's themes change it.
type ThemeColours struct {
	// Base is ThemeDark or ThemeLight, for a custom theme only; it
	// defaults to ThemeDark. The built-in themes are their own base.
	Base    Theme  `yaml:"base,omitempty"`
	Bg      string `yaml:"bg,omitempty"`
	Surface string `yaml:"surface,omitempty"`
	Fg      string `yaml:"fg,omitempty"`
	Muted   string `yaml:"muted,omitempty"`
	Border  string `yaml:"border,omitempty"`
	Accent  string `yaml:"accent,omitempty"`
	Ok      string `yaml:"ok,omitempty"`
	Warn    string `yaml:"warn,omitempty"`
	Error   string `yaml:"error,omitempty"`
	Hover   string `yaml:"hover,omitempty"`
	Inset   string `yaml:"inset,omitempty"`
}

// colour is the token named name, one of palette.Tokens.
func (t *ThemeColours) colour(name string) *string {
	switch name {
	case "bg":
		return &t.Bg
	case "surface":
		return &t.Surface
	case "fg":
		return &t.Fg
	case "muted":
		return &t.Muted
	case "border":
		return &t.Border
	case "accent":
		return &t.Accent
	case "ok":
		return &t.Ok
	case "warn":
		return &t.Warn
	case "error":
		return &t.Error
	case "hover":
		return &t.Hover
	case "inset":
		return &t.Inset
	}
	panic("config: unknown colour token " + name)
}

// themeName is the grammar of a custom theme's name.
var themeName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func builtin(t Theme) bool { return t == ThemeDark || t == ThemeLight }

// ThemeNames lists the themes the config offers, in the order the
// header's toggle steps through them: dark, light, then the custom
// themes by name.
func (c Config) ThemeNames() []Theme {
	names := []Theme{ThemeDark, ThemeLight}
	for _, t := range slices.Sorted(maps.Keys(c.Themes)) {
		if !builtin(t) {
			names = append(names, t)
		}
	}
	return names
}

// NextTheme is the theme after t in ThemeNames, wrapping around; for a
// theme the config does not offer, it is the first.
func (c Config) NextTheme(t Theme) Theme {
	names := c.ThemeNames()
	i := slices.Index(names, t.OrDefault())
	return names[(i+1)%len(names)]
}

// OrDefault is t, or ThemeDark if t is empty.
func (t Theme) OrDefault() Theme {
	if t == "" {
		return ThemeDark
	}
	return t
}

// Offers reports whether t is one of ThemeNames, or empty.
func (c Config) Offers(t Theme) bool {
	return slices.Contains(c.ThemeNames(), t.OrDefault())
}

// Palette is theme t's colours and whether it is dark: its base, with
// the config's changes to that base, and t's tokens over it. A token
// that does not parse, which Validate reports, keeps the base's colour;
// a theme the config does not offer gives the dark palette.
func (c Config) Palette(t Theme) (palette.Palette, bool) {
	p, dark, _ := c.resolve(t.OrDefault())
	return p, dark
}

// resolve is Palette, with the problems in the theme's definition.
func (c Config) resolve(t Theme) (palette.Palette, bool, []error) {
	tc := c.Themes[t]
	base := t
	if !builtin(t) {
		if !c.Offers(t) {
			return palette.Dark, true, nil
		}
		base = tc.Base.OrDefault()
	}
	var errs []error
	if !builtin(t) && !builtin(base) {
		errs = append(errs, fmt.Errorf("themes.%s: base must be %q or %q, got %q", t, ThemeDark, ThemeLight, tc.Base))
		base = ThemeDark
	}
	if builtin(t) && tc.Base != "" {
		errs = append(errs, fmt.Errorf("themes.%s: base applies only to custom themes", t))
	}
	var p palette.Palette
	switch {
	case !builtin(t):
		// A custom theme starts from its base as the config changes it;
		// the base's own problems are reported for the base.
		p, _, _ = c.resolve(base)
	case t == ThemeDark:
		p = palette.Dark
	default:
		p = palette.Light
	}
	for _, name := range palette.Tokens() {
		s := *tc.colour(name)
		if s == "" {
			continue
		}
		v, err := palette.Parse(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("themes.%s.%s: %w", t, name, err))
			continue
		}
		*p.Token(name) = v
	}
	return p, base == ThemeDark, errs
}

// validateThemes reports problems with the theme and themes fields.
func (c Config) validateThemes() []error {
	var errs []error
	for _, t := range slices.Sorted(maps.Keys(c.Themes)) {
		if !builtin(t) && !themeName.MatchString(string(t)) {
			errs = append(errs, errThemeName(string(t)))
			continue
		}
		_, _, problems := c.resolve(t)
		errs = append(errs, problems...)
	}
	if !c.Offers(c.Theme) {
		errs = append(errs, fmt.Errorf("theme must be %q, %q, or a theme under themes, got %q", ThemeDark, ThemeLight, c.Theme))
	}
	return errs
}

// ContrastWarnings lists, one per line, the text colours of the
// config's themes that are hard to read against their backgrounds.
// Such a theme is still valid; the built-in ones have none. A custom
// theme's shortfall in both colours as its base has them is reported
// for the base alone.
func (c Config) ContrastWarnings() []string {
	var out []string
	for _, t := range c.ThemeNames() {
		tc, ok := c.Themes[t]
		if !ok {
			continue
		}
		p, _ := c.Palette(t)
		var base palette.Palette
		if !builtin(t) {
			base, _ = c.Palette(tc.Base.OrDefault())
		}
		for _, s := range p.Shortfalls() {
			if !builtin(t) && *base.Token(s.Text) == *p.Token(s.Text) && *base.Token(s.On) == *p.Token(s.On) {
				continue
			}
			out = append(out, fmt.Sprintf("theme %s: %s", t, s))
		}
	}
	return out
}

// checkThemes reports, as the file writes them, a theme whose name is
// null or not a theme name, and a base or token with no value or an
// empty one. A hex colour left unquoted has none, since # starts a YAML
// comment, and would otherwise keep the base's colour without a word.
// Unknown keys are left for the decoder to report.
func checkThemes(file *ast.File) error {
	if len(file.Docs) == 0 || file.Docs[0].Body == nil {
		return nil
	}
	themes, ok := unwrap(value(file.Docs[0].Body, "themes")).(*ast.MappingNode)
	if !ok {
		return nil
	}
	var errs []error
	for _, theme := range themes.Values {
		if _, null := unwrap(theme.Key).(*ast.NullNode); null {
			errs = append(errs, fmt.Errorf("line %d: themes: a theme's name is null; quote it to use the word", line(theme)))
			continue
		}
		if tag := keyTag(theme.Key); tag != "" && tag != "!!str" {
			errs = append(errs, fmt.Errorf("line %d: themes: a theme's name may not use the %s tag", line(theme), tag))
			continue
		}
		name := keyText(theme.Key)
		if !builtin(Theme(name)) && !themeName.MatchString(name) {
			errs = append(errs, fmt.Errorf("line %d: %w", line(theme), errThemeName(name)))
			continue
		}
		m, ok := unwrap(theme.Value).(*ast.MappingNode)
		if !ok {
			continue
		}
		for _, pair := range m.Values {
			key := keyText(pair.Key)
			if key == "<<" {
				errs = append(errs, fmt.Errorf("line %d: themes.%s: merge keys are not supported in themes", line(pair), name))
				continue
			}
			if key != "base" && !slices.Contains(palette.Tokens(), key) {
				continue
			}
			v := unwrap(pair.Value)
			_, null := v.(*ast.NullNode)
			if lit, ok := v.(*ast.LiteralNode); ok {
				v = lit.Value // a block scalar, as |- or >-
			}
			str, isString := v.(*ast.StringNode)
			switch {
			case v == nil || null:
				hint := ""
				if key != "base" {
					hint = `; quote a hex colour, as in "#1e1e2e", since # starts a comment`
				}
				errs = append(errs, fmt.Errorf("line %d: themes.%s.%s has no value%s", line(pair), name, key, hint))
			case !isString:
				// The decoder would turn 0x1e1e2e into 1973806, which the
				// file does not say.
				if text := asWritten(pair.Value); text != nil {
					want := "is not a colour; quote it"
					if key == "base" {
						want = `must be "dark" or "light"`
					}
					errs = append(errs, fmt.Errorf("line %d: themes.%s.%s: %s %s", line(pair), name, key, *text, want))
				}
			case isString && str.Value == "":
				hint := "leave it out to keep the base's colour"
				if key == "base" {
					hint = "leave it out to use dark"
				}
				errs = append(errs, fmt.Errorf("line %d: themes.%s.%s is empty; %s", line(pair), name, key, hint))
			}
		}
	}
	return errors.Join(errs...)
}

// keyTag is the tag on a mapping key, as "!!str", or "".
func keyTag(key ast.Node) string {
	for n := key; n != nil; {
		switch m := n.(type) {
		case *ast.MappingKeyNode:
			n = m.Value
		case *ast.AnchorNode:
			n = m.Value
		case *ast.TagNode:
			return m.Start.Value
		default:
			return ""
		}
	}
	return ""
}

// errThemeName says why name cannot be a custom theme's name.
func errThemeName(name string) error {
	return fmt.Errorf("themes: name %q must be lowercase letters, digits, and hyphens, starting with a letter, at most 32 characters", name)
}

// writeThemes writes the themes field, if the config has themes.
func writeThemes(b *strings.Builder, themes map[Theme]ThemeColours) {
	if len(themes) == 0 {
		return
	}
	b.WriteString("themes:\n")
	for _, t := range slices.Sorted(maps.Keys(themes)) {
		tc := themes[t]
		var fields []string
		if tc.Base != "" {
			fields = append(fields, "base: "+scalarString(string(tc.Base)))
		}
		for _, name := range palette.Tokens() {
			if s := *tc.colour(name); s != "" {
				fields = append(fields, name+": "+scalarString(s))
			}
		}
		if len(fields) == 0 {
			fmt.Fprintf(b, "  %s: {}\n", scalarString(string(t)))
			continue
		}
		fmt.Fprintf(b, "  %s:\n", scalarString(string(t)))
		for _, f := range fields {
			fmt.Fprintf(b, "    %s\n", f)
		}
	}
}
