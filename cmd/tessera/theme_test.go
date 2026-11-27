// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/ui"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// toastText is the message toast shows at the next frame.
func toastText(toast *ui.Toaster) string {
	gtx := layout.Context{
		Ops:         new(op.Ops),
		Now:         time.Now(),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(800, 600)),
	}
	toast.Layout(gtx, theme.New(true, theme.NewShaper(false)))
	return toast.Text()
}

func TestWindowConfigThemeReload(t *testing.T) {
	w, _, _ := configureWindow(t)
	w.theme, w.fileTheme = config.ThemeDark, config.ThemeDark
	reloads := make(chan reload, 1)
	w.reloads = reloads
	take := func(c config.Config) {
		t.Helper()
		reloads <- reload{cfg: c, valid: true}
		w.takeReload()
	}

	c := layoutConfig()
	c.Theme = "dusk"
	c.Themes = map[config.Theme]config.ThemeColours{"dusk": {Muted: "#333"}}
	look := w.look
	take(c)
	if w.theme != "dusk" || w.look == look {
		t.Fatalf("theme %q, look %d after %d; want dusk, a new look", w.theme, w.look, look)
	}
	if got, want := toastText(w.toast), "Hard to read: theme dusk: muted on bg has contrast"; !strings.HasPrefix(got, want) || !strings.HasSuffix(got, "(and 1 more; see the log)") {
		t.Fatalf("toast %q, want %q… and one more", got, want)
	}

	// New colours for the theme showing are a new look too.
	c.Themes = map[config.Theme]config.ThemeColours{"dusk": {Muted: "#bbb"}}
	look = w.look
	take(c)
	if w.theme != "dusk" || w.look == look {
		t.Fatalf("theme %q, look %d after %d; want dusk, a new look", w.theme, w.look, look)
	}
	if got := toastText(w.toast); got != "" {
		t.Fatalf("toast %q once the colours are readable, want none", got)
	}

	// Dropping the theme showing falls back to the file's, even when
	// the file's theme stays as it was: here a toggle to dusk was not
	// saved.
	c.Theme = config.ThemeLight
	take(c)
	w.setTheme("dusk")
	c.Themes = nil
	take(c)
	if w.theme != config.ThemeLight {
		t.Fatalf("theme %q after dusk was removed, want light", w.theme)
	}
}

func TestThemeLook(t *testing.T) {
	w, _, _ := configureWindow(t)
	w.cfg.Themes = map[config.Theme]config.ThemeColours{
		"dusk":  {Bg: "#1e1e2e"},
		"paper": {Base: config.ThemeLight},
	}
	for _, tt := range []struct {
		shown, next    config.Theme
		dark, nextDark bool
		bg             color.NRGBA
	}{
		{config.ThemeDark, config.ThemeLight, true, false, theme.Dark.Bg},
		{config.ThemeLight, "dusk", false, true, theme.Light.Bg},
		{"dusk", "paper", true, false, color.NRGBA{0x1e, 0x1e, 0x2e, 255}},
		{"paper", config.ThemeDark, false, true, theme.Light.Bg},
	} {
		w.setTheme(tt.shown)
		th, next, nextDark := w.themeLook(nil)
		if th.Dark != tt.dark || th.Bg != tt.bg || next != string(tt.next) || nextDark != tt.nextDark {
			t.Errorf("%s: dark %t, bg %v, next %q (dark %t); want %t, %v, %q (%t)",
				tt.shown, th.Dark, th.Bg, next, nextDark, tt.dark, tt.bg, tt.next, tt.nextDark)
		}
	}
}

func TestWarnContrastOnce(t *testing.T) {
	w, _, _ := configureWindow(t)
	var logged []string
	w.logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	w.cfg.Themes = map[config.Theme]config.ThemeColours{config.ThemeLight: {Fg: "#eee"}}
	w.warnContrast()
	if got := toastText(w.toast); !strings.HasPrefix(got, "Hard to read: theme light: fg on bg") {
		t.Fatalf("toast %q, want the light theme's fg", got)
	}
	w.toast.Drop(contrastToast)
	w.warnContrast()
	if got := toastText(w.toast); got != "" {
		t.Fatalf("the same warnings were shown again: %q", got)
	}
	if len(logged) != 2 {
		t.Fatalf("logged %q, want the two warnings once", logged)
	}

	// Only what changes is logged: a new warning, then the end of them.
	logged = nil
	w.cfg.Themes = map[config.Theme]config.ThemeColours{config.ThemeLight: {Fg: "#eee", Muted: "#eee"}}
	w.warnContrast()
	if len(logged) != 2 || !strings.Contains(logged[0], "muted on bg") || !strings.Contains(logged[1], "muted on surface") {
		t.Fatalf("logged %q, want only muted's two new warnings", logged)
	}
	// Fewer warnings log how many are left.
	logged = nil
	w.cfg.Themes = map[config.Theme]config.ThemeColours{config.ThemeLight: {Fg: "#eee"}}
	w.warnContrast()
	if len(logged) != 1 || !strings.Contains(logged[0], "2 theme colour warnings remain") {
		t.Fatalf("logged %q, want how many are left", logged)
	}
	logged = nil
	w.cfg.Themes = nil
	w.warnContrast()
	if len(logged) != 1 || !strings.Contains(logged[0], "readable again") {
		t.Fatalf("logged %q, want one line saying the colours are readable again", logged)
	}
}
