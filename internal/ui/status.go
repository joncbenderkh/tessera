// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"strings"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
)

// Tone is the colour of a status dot.
type Tone int

const (
	ToneOk Tone = iota
	ToneWarn
	ToneError
)

// Status is the header's plugin status line.
type Status struct {
	Text string
	Tone Tone
}

// StatusOf summarises the enabled panels: how many there are and how many
// are starting, stale (or live with a view over a limit), or failing. Any failing panel makes the tone an
// error, else any starting or stale one a warning.
func StatusOf(states []pluginhost.PanelState) Status {
	var starting, stale, failing int
	for _, s := range states {
		switch s.State {
		case pluginhost.Starting:
			starting++
		case pluginhost.Stale:
			stale++
		case pluginhost.Live:
			// A view over a limit: no more are read until a restart.
			if s.ViewErr != nil {
				stale++
			}
		case pluginhost.Stopping:
			// Without an error, a restart the user asked for.
			if s.Err != nil {
				failing++
			} else {
				starting++
			}
		case pluginhost.Restarting, pluginhost.Crashed:
			failing++
		}
	}
	parts := []string{count(len(states), "panel", "panels")}
	if starting > 0 {
		parts = append(parts, fmt.Sprintf("%d starting", starting))
	}
	if stale > 0 {
		parts = append(parts, fmt.Sprintf("%d stale", stale))
	}
	if failing > 0 {
		parts = append(parts, count(failing, "error", "errors"))
	}
	s := Status{Text: strings.Join(parts, " · ")}
	switch {
	case failing > 0:
		s.Tone = ToneError
	case starting > 0 || stale > 0:
		s.Tone = ToneWarn
	}
	return s
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
