// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/joncbenderkh/tessera/sdk"
)

// panelState is what the view shows.
type panelState struct {
	label   string
	place   *place // where the location was found, or nil
	query   query
	cur     *current // the latest conditions, or nil before any
	err     error    // why the latest fetch failed, or nil
	refresh time.Duration
}

// staleAfter is how old conditions may be before they show as stale:
// Open-Meteo updates them every 15 minutes, and the panel fetches them
// every refresh.
func staleAfter(refresh time.Duration) time.Duration {
	return 2*refresh + 15*time.Minute
}

// view is the panel's view at now.
func view(now time.Time, s panelState) sdk.View {
	v := sdk.View{Title: title(s)}
	switch {
	case s.cur == nil && s.err == nil:
		v.Body = sdk.Column(
			sdk.Stat("", "").Loading(),
			sdk.Text("Fetching the weather").Size(sdk.SizeSmall).Tone(sdk.ToneMuted),
		).Gap(sdk.SpacingSmall)
	case s.cur == nil:
		text := s.err.Error()
		if errors.Is(s.err, errNoPlace) {
			text += "; check the location setting"
		}
		v.Body = sdk.Column(
			sdk.Text(text).Tone(sdk.ToneError),
			sdk.Text("Retrying; the details are in the panel's logs.").Size(sdk.SizeSmall).Tone(sdk.ToneMuted),
		).Gap(sdk.SpacingSmall)
	default:
		c, unit := s.cur, "°C"
		if s.query.imperial {
			unit = "°F"
		}
		body := sdk.Column(
			sdk.Stat("", degrees(c.temperature)).Unit(unit).Caption(condition(c.code, c.day)).CaptionTone(sdk.ToneDefault),
			sdk.Text(fmt.Sprintf("Feels like %s%s · Humidity %s%%", degrees(c.feelsLike), unit, strconv.FormatFloat(math.Round(c.humidity), 'f', 0, 64))).Size(sdk.SizeSmall),
		).Gap(sdk.SpacingSmall)
		age := "As of " + clock(now, c.at)
		switch {
		case s.err != nil:
			body.Add(sdk.Text(age + "; " + s.err.Error()).Size(sdk.SizeSmall).Tone(sdk.ToneWarn))
		case now.Sub(c.at) > staleAfter(s.refresh):
			body.Add(sdk.Text(age + "; not updated since").Size(sdk.SizeSmall).Tone(sdk.ToneWarn))
		default:
			body.Add(sdk.Text(age + " · Open-Meteo").Size(sdk.SizeSmall).Tone(sdk.ToneMuted))
		}
		v.Body = body
	}
	return v
}

// title is the label, or the place found with its region, so that the
// place a name found shows, or the location, or the coordinates.
func title(s panelState) string {
	switch {
	case s.label != "":
		return s.label
	case s.place != nil && s.place.region != "":
		return s.place.name + ", " + s.place.region
	case s.place != nil:
		return s.place.name
	case s.query.location != "":
		return s.query.location
	}
	return coordinates(s.query.lat, s.query.lon)
}

// coordinates writes a latitude and longitude, as 52.52° N, 13.41° E.
func coordinates(lat, lon float64) string {
	ns, ew := "N", "E"
	if lat < 0 {
		ns, lat = "S", -lat
	}
	if lon < 0 {
		ew, lon = "W", -lon
	}
	return fmt.Sprintf("%.2f° %s, %.2f° %s", lat, ns, lon, ew)
}

// degrees writes a temperature to the degree, never as -0.
func degrees(t float64) string {
	r := math.Round(t)
	if r == 0 {
		r = 0 // not -0
	}
	return strconv.FormatFloat(r, 'f', 0, 64)
}

// clock writes when t was, in local time: the time of day, and the date
// unless it is today's.
func clock(now, t time.Time) string {
	now, t = now.Local(), t.Local()
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

// condition names WMO weather code code, as Open-Meteo reports it.
func condition(code int, day bool) string {
	switch code {
	case 0:
		if !day {
			return "Clear"
		}
		return "Sunny"
	case 1:
		if !day {
			return "Mainly clear"
		}
		return "Mainly sunny"
	case 2:
		return "Partly cloudy"
	case 3:
		return "Overcast"
	case 45:
		return "Fog"
	case 48:
		return "Freezing fog"
	case 51:
		return "Light drizzle"
	case 53:
		return "Drizzle"
	case 55:
		return "Heavy drizzle"
	case 56:
		return "Light freezing drizzle"
	case 57:
		return "Freezing drizzle"
	case 61:
		return "Light rain"
	case 63:
		return "Rain"
	case 65:
		return "Heavy rain"
	case 66:
		return "Light freezing rain"
	case 67:
		return "Freezing rain"
	case 71:
		return "Light snow"
	case 73:
		return "Snow"
	case 75:
		return "Heavy snow"
	case 77:
		return "Snow grains"
	case 80:
		return "Light showers"
	case 81:
		return "Showers"
	case 82:
		return "Heavy showers"
	case 85:
		return "Light snow showers"
	case 86:
		return "Snow showers"
	case 95:
		return "Thunderstorm"
	case 96, 99:
		return "Thunderstorm with hail"
	}
	return fmt.Sprintf("Weather code %d", code)
}

// minBody is the least room the panel's view needs, in dp: the
// temperature with its condition, and the two lines below, which fits
// a tile one row tall at the default row height.
var minBody = sdk.BodySize{Width: 168, Height: 108}
