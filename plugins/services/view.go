// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/joncbenderkh/tessera/sdk"
)

// maxItems is how many units the list holds.
const maxItems = 100

// panelState is what the view shows.
type panelState struct {
	rows     []unit
	errs     [2]error // why each manager could not be read
	named    []ref    // the units the settings name
	polled   bool
	selected string
	busy     map[string]bool
	results  map[string]string
}

// view is the panel's view at now.
func view(now time.Time, s panelState) sdk.View {
	failed := 0
	for _, u := range s.rows {
		if u.active == "failed" {
			failed++
		}
	}
	summary := listed(len(s.rows))
	if failed > 0 {
		summary += fmt.Sprintf(" · %d failed", failed)
	}
	body := sdk.Column(sdk.Text(summary).Size(sdk.SizeSmall).Tone(sdk.ToneMuted).Monospace()).Gap(sdk.SpacingSmall)
	for sc, err := range s.errs {
		if err != nil && (needs(s.named, scope(sc)) || s.errs[1-sc] != nil) {
			body.Add(sdk.Text(fmt.Sprintf("Cannot read the %s's services: %v", managerName(scope(sc)), err)).Size(sdk.SizeSmall).Tone(sdk.ToneWarn))
		}
	}
	// The units named always have rows once read, so with none, only
	// failed ones were looked for.
	empty := "No failed services"
	if len(s.named) > 0 {
		empty = "No units read"
	}
	list := sdk.List().ID("units").Empty(empty)
	if !s.polled {
		list.Loading()
	}
	var details sdk.Widget
	for i, u := range s.rows {
		if i < maxItems {
			list.Add(item(u, now))
		}
		if key := u.ref.String(); key == s.selected {
			details = unitDetails(u, now, s.busy[key], s.results[key])
		}
	}
	body.Add(sdk.Weighted(1, list))
	if details != nil {
		body.Add(details)
	}
	return sdk.View{Title: "Services", Body: body}
}

// needs reports whether the units named include one of sc's.
func needs(named []ref, sc scope) bool {
	for _, r := range named {
		if r.scope == sc {
			return true
		}
	}
	return false
}

func managerName(sc scope) string {
	if sc == user {
		return "user manager"
	}
	return "system manager"
}

// item is u's entry in the list at now.
func item(u unit, now time.Time) *sdk.ListItemSpec {
	detail := []string{u.sub}
	if u.ref.scope == user {
		detail = append([]string{"user"}, detail...)
	}
	if !u.since.IsZero() {
		detail = append(detail, "since "+since(u.since, now))
	}
	return sdk.Item(u.ref.name).Leading(state(u)).Detail(strings.Join(nonEmpty(detail), " · ")).Action(unitPrefix + u.ref.String())
}

// state names u's state in a word.
func state(u unit) string {
	switch {
	case u.load == "not-found":
		return "missing"
	case u.load == "masked":
		return "masked"
	}
	switch u.active {
	case "activating":
		return "starting"
	case "deactivating":
		return "stopping"
	case "":
		return "unknown"
	}
	return u.active
}

// unitDetails shows all the panel knows of u, and a user's own
// service's Restart, unless it is being restarted.
func unitDetails(u unit, now time.Time, busy bool, result string) sdk.Widget {
	col := sdk.Column(sdk.Text(u.ref.String())).Gap(sdk.SpacingSmall)
	if u.id != "" && u.id != u.ref.name {
		col.Add(sdk.Text("An alias of " + u.id).Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
	}
	if u.description != "" && u.description != u.ref.name {
		col.Add(sdk.Text(u.description).Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
	}
	line := fmt.Sprintf("%s (%s)", u.active, u.sub)
	if !u.since.IsZero() {
		line += " since " + since(u.since, now)
	}
	if u.load != "loaded" && u.load != "" {
		line += " · " + u.load
	}
	col.Add(sdk.Text(line).Size(sdk.SizeSmall).Monospace())
	switch {
	case busy:
		col.Add(sdk.Text("Restarting…").Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
	case result != "":
		col.Add(sdk.Text("Not restarted: " + result).Size(sdk.SizeSmall).Tone(sdk.ToneError))
	}
	items := sdk.List()
	if restartable(u) && !busy {
		items.Add(sdk.Item("Restart").Action(restartPrefix + u.ref.String()))
	}
	// An item rather than activating the unit again: the host keeps the
	// item last activated highlighted, which this one takes away with it.
	items.Add(sdk.Item("Hide details").Action(hideAction))
	return col.Add(items)
}

// since is when t was: its time today, its weekday and time within a
// week, else its date.
func since(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	}
	return t.Format("Jan 2")
}

// listed says how many units there are, and how many the list holds
// if not all.
func listed(n int) string {
	if n > maxItems {
		return fmt.Sprintf("%d of %s", maxItems, count(n, "unit"))
	}
	return count(n, "unit")
}

// count is n of noun, in the plural unless n is 1.
func count(n int, noun string) string {
	if n != 1 {
		noun += "s"
	}
	return fmt.Sprintf("%d %s", n, noun)
}

func nonEmpty(parts []string) []string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// minBody is the least room the panel's view needs, in dp: a unit's
// details with a long description and a failed restart, at the least
// width; a tile two rows tall at the default row height has it.
var minBody = sdk.BodySize{Width: 200, Height: 264}
