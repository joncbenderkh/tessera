// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
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
		return p, nil, &fetchError{"unreadable answer", "Open-Meteo's answer lacks the current conditions"}
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
// or country it is in after commas, as in Springfield, Illinois. Open-Meteo
// reads the first state or country so, and the place is the first it
// finds of that name, or else of a name like it, that lies in the rest;
// a region it does not read, as a county, is looked for among the first
// hundred places of the name.
func lookup(ctx context.Context, location string) (*place, error) {
	parts := strings.Split(location, ",")
	name := strings.TrimSpace(parts[0])
	var within []string
	for _, p := range parts[1:] {
		if p = strings.TrimSpace(p); p != "" {
			within = append(within, p)
		}
	}
	if len(within) == 0 {
		found, err := search(ctx, name, 1)
		if err != nil || len(found) == 0 {
			return nil, cmp.Or(err, fmt.Errorf("%w named %q", errNoPlace, location))
		}
		return found[0].place(), nil
	}
	found, err := search(ctx, strings.Join(append([]string{name}, within...), ", "), 10)
	if err != nil {
		return nil, err
	}
	// Open-Meteo reads the first region or country only: the place must
	// lie in the others too.
	found = slices.DeleteFunc(found, func(r result) bool { return !r.inAll(within[1:]) })
	// Best the place of the name in the first region too, as Open-Meteo
	// read it, then of the name, then the first like it.
	exact := func(r result) bool { return strings.EqualFold(r.Name, name) }
	if i := slices.IndexFunc(found, func(r result) bool { return exact(r) && r.in(within[0]) }); i >= 0 {
		return found[i].place(), nil
	}
	if i := slices.IndexFunc(found, exact); i >= 0 {
		return found[i].place(), nil
	}
	if len(found) > 0 {
		return found[0].place(), nil
	}
	if found, err = search(ctx, name, 100); err != nil {
		return nil, err
	}
	for _, r := range found {
		if r.inAll(within) {
			return r.place(), nil
		}
	}
	return nil, fmt.Errorf("%w named %q", errNoPlace, location)
}

// result is a place Open-Meteo's search finds.
type result struct {
	Name        string  `json:"name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Admin1      string  `json:"admin1"`
	Admin2      string  `json:"admin2"`
	Admin3      string  `json:"admin3"`
	Admin4      string  `json:"admin4"`
}

func (r result) place() *place {
	return &place{name: r.Name, region: region(r.Name, r.Admin1, r.Country), lat: r.Latitude, lon: r.Longitude}
}

// in reports whether the place lies in the region or country named w,
// with or without such words as County: Open-Meteo has Sangamon but
// DuPage County.
func (r result) in(w string) bool {
	if r.CountryCode == "US" && strings.EqualFold(usStates[strings.ToUpper(w)], r.Admin1) {
		return true
	}
	return slices.ContainsFunc([]string{r.Country, r.CountryCode, r.Admin1, r.Admin2, r.Admin3, r.Admin4},
		func(a string) bool {
			return a != "" && (strings.EqualFold(a, w) || strings.EqualFold(bare(a), bare(w)))
		})
}

// usStates names the US states by their postal codes, which Open-Meteo
// reads after a place's name, so that they work in any place there.
var usStates = map[string]string{
	"AL": "Alabama", "AK": "Alaska", "AZ": "Arizona", "AR": "Arkansas", "CA": "California",
	"CO": "Colorado", "CT": "Connecticut", "DE": "Delaware", "DC": "District of Columbia",
	"FL": "Florida", "GA": "Georgia", "HI": "Hawaii", "ID": "Idaho", "IL": "Illinois",
	"IN": "Indiana", "IA": "Iowa", "KS": "Kansas", "KY": "Kentucky", "LA": "Louisiana",
	"ME": "Maine", "MD": "Maryland", "MA": "Massachusetts", "MI": "Michigan", "MN": "Minnesota",
	"MS": "Mississippi", "MO": "Missouri", "MT": "Montana", "NE": "Nebraska", "NV": "Nevada",
	"NH": "New Hampshire", "NJ": "New Jersey", "NM": "New Mexico", "NY": "New York",
	"NC": "North Carolina", "ND": "North Dakota", "OH": "Ohio", "OK": "Oklahoma", "OR": "Oregon",
	"PA": "Pennsylvania", "RI": "Rhode Island", "SC": "South Carolina", "SD": "South Dakota",
	"TN": "Tennessee", "TX": "Texas", "UT": "Utah", "VT": "Vermont", "VA": "Virginia",
	"WA": "Washington", "WV": "West Virginia", "WI": "Wisconsin", "WY": "Wyoming",
}

// inAll reports whether the place lies in every region or country in
// within.
func (r result) inAll(within []string) bool {
	return !slices.ContainsFunc(within, func(w string) bool { return !r.in(w) })
}

// search asks Open-Meteo for up to count places by name.
func search(ctx context.Context, name string, count int) ([]result, error) {
	var answer struct {
		Results []result `json:"results"`
	}
	v := url.Values{"name": {name}, "count": {strconv.Itoa(count)}, "language": {"en"}, "format": {"json"}}
	if err := getJSON(ctx, geocodingURL, v, &answer); err != nil {
		return nil, err
	}
	return answer.Results, nil
}

// region is what names a place called name in its region admin1 and
// country: the region, unless that is named after the place, as the
// State of Berlin and Land Berlin are, then the country.
func region(name, admin1, country string) string {
	if admin1 != "" && !namedAfter(admin1, name) {
		return admin1
	}
	if country != name {
		return country
	}
	return ""
}

// namedAfter reports whether region is place's name, perhaps with words
// such as State of or Land that name a kind of region: New York is not
// named after York.
func namedAfter(region, place string) bool {
	return strings.EqualFold(region, place) || strings.EqualFold(bare(region), place)
}

// bare is a region's name without the words that name a kind of region,
// such as State of, Land, or County.
func bare(region string) string {
	var words []string
	for _, w := range strings.Fields(region) {
		switch strings.ToLower(w) {
		case "state", "of", "land", "province", "region", "city", "county", "the":
			continue
		}
		words = append(words, w)
	}
	return strings.Join(words, " ")
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
		return &fetchError{"unreadable answer", "Open-Meteo's answer is too large"}
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
		return &fetchError{fmt.Sprintf("HTTP %d", resp.StatusCode), msg}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &fetchError{"unreadable answer", "Open-Meteo's answer does not read: " + err.Error()}
	}
	return nil
}

// fetchError is why a fetch failed: in a few words for the panel, and
// in full, but never with the URL, which holds the place, for its logs.
type fetchError struct {
	brief, detail string
}

func (e *fetchError) Error() string { return e.detail }

// brief is why err happened in a few words, for the panel.
func brief(err error) string {
	var f *fetchError
	if errors.As(err, &f) {
		return f.brief
	}
	return err.Error()
}

// describe says what went wrong reaching Open-Meteo, in words that
// never quote the URL: Go's errors for a request do.
func describe(err error) error {
	var dns *net.DNSError
	var op *net.OpError
	var cert *tls.CertificateVerificationError
	switch {
	case errors.Is(err, context.Canceled):
		return err
	case os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded):
		return &fetchError{"timed out", fmt.Sprintf("Open-Meteo did not answer within %v", fetchTimeout)}
	case errors.As(err, &dns):
		return &fetchError{"cannot find the server", dns.Error()} // names the host only
	case errors.As(err, &op):
		// The innermost error, such as connection refused.
		inner := op.Err
		for next := errors.Unwrap(inner); next != nil; next = errors.Unwrap(inner) {
			inner = next
		}
		return &fetchError{inner.Error(), op.Error()} // names the address only
	case errors.As(err, &cert):
		return &fetchError{"certificate not trusted", "Open-Meteo's certificate is not trusted"}
	}
	detail := err.Error()
	var u *url.Error
	if errors.As(err, &u) {
		detail = u.Err.Error() // without the URL
	}
	return &fetchError{"request failed", "the request to Open-Meteo failed: " + detail}
}
