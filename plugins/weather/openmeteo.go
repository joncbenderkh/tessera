// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joncbenderkh/tessera"
)

// Open-Meteo's endpoints: the forecast, with current conditions, and
// the place-name search. Free and keyless for non-commercial use; the
// data is CC BY 4.0, credited in the view.
var (
	forecastURL  = "https://api.open-meteo.com/v1/forecast"
	geocodingURL = "https://geocoding-api.open-meteo.com/v1/search"
)

const (
	// fetchTimeout bounds one request.
	fetchTimeout = 30 * time.Second
	// maxBody bounds an answer; one is a few hundred bytes.
	maxBody = 1 << 20
)

var client = &http.Client{Timeout: fetchTimeout}

// place is where a location was found.
type place struct {
	name     string // as Open-Meteo names it, such as Berlin
	region   string // the region, or the country, that tells it apart, such as Illinois, or ""
	lat, lon float64
}

// current is the conditions Open-Meteo reports.
type current struct {
	at          time.Time // the time they are for
	temperature float64
	feelsLike   float64
	humidity    float64 // in percent
	code        int     // the WMO weather code
	day         bool
}

// errNoPlace is the error when a location is not found.
var errNoPlace = errors.New("no such place")

// fetch fetches the current conditions for q, looking up its location
// first unless known, where it was found before, is given. It returns
// the place it used.
func fetch(ctx context.Context, q query, known *place) (*place, *current, error) {
	p := known
	if p == nil && q.location != "" {
		var err error
		if p, err = lookup(ctx, q.location); err != nil {
			return nil, nil, err
		}
	}
	lat, lon := q.lat, q.lon
	if p != nil {
		lat, lon = p.lat, p.lon
	}
	v := url.Values{
		"latitude":   {strconv.FormatFloat(lat, 'f', -1, 64)},
		"longitude":  {strconv.FormatFloat(lon, 'f', -1, 64)},
		"current":    {"temperature_2m,apparent_temperature,relative_humidity_2m,weather_code,is_day"},
		"timeformat": {"unixtime"},
	}
	if q.imperial {
		v.Set("temperature_unit", "fahrenheit")
	}
	var answer struct {
		Current *struct {
			Time        *int64   `json:"time"`
			Temperature *float64 `json:"temperature_2m"`
			FeelsLike   *float64 `json:"apparent_temperature"`
			Humidity    *float64 `json:"relative_humidity_2m"`
			Code        *int     `json:"weather_code"`
			IsDay       *int     `json:"is_day"`
		} `json:"current"`
	}
	if err := getJSON(ctx, forecastURL, v, &answer); err != nil {
		return p, nil, err
	}
	c := answer.Current
	if c == nil || c.Time == nil || c.Temperature == nil || c.FeelsLike == nil || c.Humidity == nil || c.Code == nil {
		return p, nil, errors.New("Open-Meteo's answer lacks the current conditions")
	}
	return p, &current{
		at:          time.Unix(*c.Time, 0),
		temperature: *c.Temperature,
		feelsLike:   *c.FeelsLike,
		humidity:    *c.Humidity,
		code:        *c.Code,
		day:         c.IsDay == nil || *c.IsDay != 0,
	}, nil
}

// lookup finds location, a place's name, perhaps followed by the region
// or country it is in after commas, as in Springfield, Illinois: the
// first place Open-Meteo finds by the name that is in all of them.
func lookup(ctx context.Context, location string) (*place, error) {
	parts := strings.Split(location, ",")
	name := strings.TrimSpace(parts[0])
	var within []string
	for _, p := range parts[1:] {
		if p = strings.TrimSpace(p); p != "" {
			within = append(within, p)
		}
	}
	count := "1"
	if len(within) > 0 {
		count = "100"
	}
	var answer struct {
		Results []struct {
			Name        string  `json:"name"`
			Latitude    float64 `json:"latitude"`
			Longitude   float64 `json:"longitude"`
			Country     string  `json:"country"`
			CountryCode string  `json:"country_code"`
			Admin1      string  `json:"admin1"`
			Admin2      string  `json:"admin2"`
		} `json:"results"`
	}
	v := url.Values{"name": {name}, "count": {count}, "language": {"en"}, "format": {"json"}}
	if err := getJSON(ctx, geocodingURL, v, &answer); err != nil {
		return nil, err
	}
next:
	for _, r := range answer.Results {
		for _, w := range within {
			if !strings.EqualFold(w, r.Country) && !strings.EqualFold(w, r.CountryCode) &&
				!strings.EqualFold(w, r.Admin1) && !strings.EqualFold(w, r.Admin2) {
				continue next
			}
		}
		return &place{name: r.Name, region: region(r.Name, r.Admin1, r.Country), lat: r.Latitude, lon: r.Longitude}, nil
	}
	return nil, fmt.Errorf("%w named %q", errNoPlace, location)
}

// region is what names a place called name in its region admin1 and
// country: the region, unless that is named after the place, as the
// State of Berlin is, then the country.
func region(name, admin1, country string) string {
	if admin1 != "" && !strings.Contains(admin1, name) {
		return admin1
	}
	if country != name {
		return country
	}
	return ""
}

// getJSON gets u with the query v and decodes the JSON answer into out.
func getJSON(ctx context.Context, u string, v url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+v.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "tessera-weather/"+tessera.Version)
	resp, err := client.Do(req)
	if err != nil {
		return describe(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return describe(err)
	}
	if len(data) > maxBody {
		return errors.New("Open-Meteo's answer is too large")
	}
	if resp.StatusCode != http.StatusOK {
		// Open-Meteo says why in the body.
		var why struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(data, &why)
		msg := fmt.Sprintf("Open-Meteo answered HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
		if why.Reason != "" {
			msg += ": " + why.Reason
		}
		return errors.New(msg)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("Open-Meteo's answer does not read: %w", err)
	}
	return nil
}

// describe says what went wrong reaching Open-Meteo in a few words.
func describe(err error) error {
	var dns *net.DNSError
	switch {
	case errors.Is(err, context.Canceled):
		return err
	case os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("Open-Meteo did not answer within %v", fetchTimeout)
	case errors.As(err, &dns):
		return fmt.Errorf("cannot find %s", dns.Name)
	}
	return fmt.Errorf("cannot reach Open-Meteo: %w", err)
}
