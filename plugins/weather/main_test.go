// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	"github.com/joncbenderkh/tessera/internal/ui"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
	"github.com/joncbenderkh/tessera/sdk"
)

// testAPI names the environment variable that points a copy of the test
// binary, run as the plugin, at a fake Open-Meteo.
const testAPI = "TESSERA_WEATHER_TEST_API"

func TestMain(m *testing.M) {
	plugintest.Main(m, func() {
		if base := os.Getenv(testAPI); base != "" {
			forecastURL, geocodingURL = base+"/v1/forecast", base+"/v1/search"
		}
		main()
	})
}

// fakeOpenMeteo serves the forecast and the place-name search as
// Open-Meteo does, for Berlin and two Springfields; temperatures are in
// °F when asked for.
func fakeOpenMeteo(t *testing.T) *httptest.Server {
	t.Helper()
	places := []map[string]any{
		{"name": "Springfield", "latitude": 37.21, "longitude": -93.29, "country": "United States", "country_code": "US", "admin1": "Missouri"},
		{"name": "Springfield", "latitude": 39.80, "longitude": -89.64, "country": "United States", "country_code": "US", "admin1": "Illinois", "admin2": "Sangamon", "admin3": "Capital Township"},
		{"name": "Springfield Park", "latitude": 41.90, "longitude": -88.10, "country": "United States", "country_code": "US", "admin1": "Illinois", "admin2": "DuPage County"},
		{"name": "Berlin", "latitude": 52.52, "longitude": 13.41, "country": "Germany", "country_code": "DE", "admin1": "Land Berlin"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/v1/search":
			// As Open-Meteo: a state or country after a comma narrows
			// the search, which then finds names like the one asked
			// for too; another region finds nothing.
			parts := strings.Split(q.Get("name"), ",")
			var found []map[string]any
		places:
			for _, p := range places {
				name := p["name"].(string)
				if !strings.EqualFold(name, parts[0]) && (len(parts) == 1 || !strings.HasPrefix(name, parts[0])) {
					continue
				}
				// Only the first: Open-Meteo ignores the rest.
				for _, w := range parts[1:min(len(parts), 2)] {
					w = strings.TrimSpace(w)
					if !strings.EqualFold(w, p["admin1"].(string)) && !strings.EqualFold(w, p["country"].(string)) && !strings.EqualFold(w, p["country_code"].(string)) &&
						!strings.EqualFold(usStates[strings.ToUpper(w)], p["admin1"].(string)) {
						continue places
					}
				}
				found = append(found, p)
			}
			if n, _ := strconv.Atoi(q.Get("count")); n < len(found) {
				found = found[:n]
			}
			answer := map[string]any{"generationtime_ms": 0.5}
			if found != nil {
				answer["results"] = found
			}
			json.NewEncoder(w).Encode(answer)
		case "/v1/forecast":
			if q.Get("latitude") == "" || q.Get("timeformat") != "unixtime" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":true,"reason":"Parameter latitude is missing"}`))
				return
			}
			temp, feels := 12.4, 9.6
			if q.Get("temperature_unit") == "fahrenheit" {
				temp, feels = 54.3, 49.3
			}
			json.NewEncoder(w).Encode(map[string]any{"current": map[string]any{
				"time": time.Now().Add(-5 * time.Minute).Unix(), "interval": 900,
				"temperature_2m": temp, "apparent_temperature": feels,
				"relative_humidity_2m": 81, "weather_code": 2, "is_day": 1,
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// useFake points this process at server.
func useFake(t *testing.T, server *httptest.Server) {
	t.Helper()
	f, g := forecastURL, geocodingURL
	forecastURL, geocodingURL = server.URL+"/v1/forecast", server.URL+"/v1/search"
	t.Cleanup(func() { forecastURL, geocodingURL = f, g })
}

func ptr(f float64) *float64 { return &f }

func TestCheck(t *testing.T) {
	for _, s := range []settings{
		{},
		{Location: " , Illinois"},
		{Location: "Berlin", Latitude: ptr(52)},
		{Latitude: ptr(52)},
		{Latitude: ptr(91), Longitude: ptr(0)},
		{Latitude: ptr(0), Longitude: ptr(-181)},
		{Location: "Berlin", Units: "kelvin"},
		{Location: "Berlin", Refresh: "1m"},
		{Location: "Berlin", Refresh: "7h"},
		{Location: "Berlin", Refresh: "often"},
	} {
		if _, err := check(s); err == nil {
			t.Errorf("check(%+v) succeeded", s)
		}
	}
	cfg, err := check(settings{Location: " Springfield, Illinois ", Units: "Imperial", Refresh: " ", Label: " Home "})
	if err != nil {
		t.Fatal(err)
	}
	want := config{query: query{location: "Springfield, Illinois", imperial: true}, label: "Home", refresh: 15 * time.Minute}
	if cfg != want {
		t.Errorf("check = %+v, want %+v", cfg, want)
	}
	cfg, err = check(settings{Latitude: ptr(-33.87), Longitude: ptr(151.21)})
	if err != nil || cfg.query != (query{lat: -33.87, lon: 151.21}) {
		t.Errorf("coordinates: %+v, %v", cfg, err)
	}
}

// A name is the first place found by it; regions and countries after
// commas pick among those of the name.
func TestLookup(t *testing.T) {
	useFake(t, fakeOpenMeteo(t))
	for location, want := range map[string]place{
		"berlin":                        {name: "Berlin", region: "Germany", lat: 52.52, lon: 13.41},
		"Springfield":                   {name: "Springfield", region: "Missouri", lat: 37.21, lon: -93.29},
		"Springfield, illinois":         {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Springfield, IL, US":           {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Springfield, US, IL":           {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Springfield, Sangamon, IL":     {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Springfield, US, MO":           {name: "Springfield", region: "Missouri", lat: 37.21, lon: -93.29},
		"Springfield, Sangamon":         {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Springfield, Sangamon, US":     {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Springfield, Cook":             {},
		"Springfield, Sangamon County":  {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Springfield Park, DuPage, US":  {name: "Springfield Park", region: "Illinois", lat: 41.90, lon: -88.10},
		"Springfield, Illinois, France": {},
		"Springfield, Illinois, US":     {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Springfield, capital township": {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Springfield, Illinois,":        {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Springfield, US, Illinois":     {name: "Springfield", region: "Illinois", lat: 39.80, lon: -89.64},
		"Atlantis":                      {},
	} {
		p, err := lookup(t.Context(), location)
		if want.name == "" {
			if !errors.Is(err, errNoPlace) {
				t.Errorf("%q: %v, %v, want errNoPlace", location, p, err)
			}
			continue
		}
		if err != nil || *p != want {
			t.Errorf("%q: %+v, %v, want %+v", location, p, err, want)
		}
	}
}

func TestFetch(t *testing.T) {
	useFake(t, fakeOpenMeteo(t))
	p, c, err := fetch(t.Context(), query{location: "Berlin", imperial: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.name != "Berlin" || c.temperature != 54.3 || c.feelsLike != 49.3 || c.humidity != 81 || c.code != 2 || !c.day {
		t.Errorf("fetch = %+v, %+v", p, c)
	}
	// A place found before is not looked up again.
	known := &place{name: "Here", lat: 1, lon: 2}
	if p, _, err := fetch(t.Context(), query{location: "Atlantis"}, known); err != nil || p != known {
		t.Errorf("known place: %v, %v", p, err)
	}
	if _, c, err := fetch(t.Context(), query{lat: 1, lon: 2}, nil); err != nil || c == nil {
		t.Errorf("coordinates: %v, %v", c, err)
	}
}

// Open-Meteo's own reason shows for a refused request, and an answer
// without the conditions is an error.
func TestFetchErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("latitude") == "1" {
			w.Write([]byte(`{"current": {"time": 1}}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":true,"reason":"Latitude must be in range of -90 to 90°."}`))
	}))
	t.Cleanup(server.Close)
	useFake(t, server)
	if _, _, err := fetch(t.Context(), query{lat: 2}, nil); err == nil || brief(err) != "HTTP 400" || !strings.Contains(err.Error(), "HTTP 400 Bad Request: Latitude must be") {
		t.Errorf("refused: %v", err)
	}
	if _, _, err := fetch(t.Context(), query{lat: 1}, nil); err == nil || brief(err) != "unreadable answer" || !strings.Contains(err.Error(), "lacks the current conditions") {
		t.Errorf("incomplete: %v", err)
	}
	server.Close()
	// A connection refused says so in a few words, and its detail names
	// the address only, never the URL with the place in it.
	_, _, err := fetch(t.Context(), query{lat: 2}, nil)
	if err == nil || brief(err) != "connection refused" || strings.Contains(err.Error(), "latitude") || strings.Contains(err.Error(), "http") {
		t.Errorf("closed: %q, %v", brief(err), err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := fetch(ctx, query{lat: 2}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}

func texts(v sdk.View) []string { return plugintest.Texts(v.Proto().GetBody()) }

func stat(v sdk.View) *tesserav1.Stat {
	for _, w := range plugintest.Widgets(v.Proto().GetBody()) {
		if s := w.GetStat(); s != nil {
			return s
		}
	}
	return nil
}

func TestView(t *testing.T) {
	now := time.Date(2026, 11, 24, 13, 20, 0, 0, time.Local)
	berlin := &place{name: "Berlin", region: "Germany"}
	cur := &current{at: now.Add(-5 * time.Minute), temperature: -0.4, feelsLike: -3.5, humidity: 80.6, code: 0, day: false}
	s := panelState{query: query{location: "berlin"}, refresh: 15 * time.Minute}

	if v := view(now, s); v.Title != "berlin" || !slices.Equal(texts(v), []string{"Fetching the weather"}) {
		t.Errorf("loading: %q %q", v.Title, texts(v))
	}
	s.err = &fetchError{"connection refused", "dial tcp 1.2.3.4:443: connect: connection refused"}
	if got := texts(view(now, s)); got[0] != "Cannot get the weather: connection refused" {
		t.Errorf("failed: %q", got)
	}
	s.err = errNoPlace
	if got := texts(view(now, s)); !strings.Contains(got[0], "check the location setting") {
		t.Errorf("no place: %q", got)
	}

	s.err, s.place, s.cur = nil, berlin, cur
	v := view(now, s)
	if st := stat(v); v.Title != "Berlin, Germany" || st.GetValue() != "0" || st.GetUnit() != "°C" || st.GetCaption() != "Clear" {
		t.Errorf("view: %q %v", v.Title, st)
	}
	if want := []string{"Feels like -4°C · Humidity 81%", "As of 13:15 · Open-Meteo"}; !slices.Equal(texts(v), want) {
		t.Errorf("texts %q, want %q", texts(v), want)
	}

	// Stale: a failed fetch, or conditions long not updated, still
	// credited.
	s.err = &fetchError{"timed out", "Open-Meteo did not answer within 30s"}
	if got := texts(view(now, s)); got[1] != "As of 13:15 · Open-Meteo · timed out" {
		t.Errorf("failed: %q", got)
	}
	s.err = nil
	if got := texts(view(now.Add(46*time.Minute), s)); got[1] != "As of 13:15 · Open-Meteo · not updated" {
		t.Errorf("old: %q", got)
	}
	if got := texts(view(now.Add(24*time.Hour), s)); !strings.HasPrefix(got[1], "As of Nov 24 13:15") {
		t.Errorf("yesterday: %q", got)
	}

	s = panelState{query: query{lat: -33.8688, lon: 151.2093, imperial: true}, cur: cur, label: "Sydney"}
	v = view(now, s)
	if v.Title != "Sydney" || stat(v).GetUnit() != "°F" {
		t.Errorf("coordinates: %q %q", v.Title, texts(v))
	}
}

func TestCondition(t *testing.T) {
	for _, tc := range []struct {
		code int
		day  bool
		want string
	}{{0, true, "Sunny"}, {0, false, "Clear"}, {2, false, "Partly cloudy"}, {63, true, "Rain"}, {99, true, "Thunderstorm with hail"}, {42, true, "Weather code 42"}} {
		if got := condition(tc.code, tc.day); got != tc.want {
			t.Errorf("condition(%d, %t) = %q, want %q", tc.code, tc.day, got, tc.want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	refresh := 15 * time.Minute
	for failures, want := range []time.Duration{refresh, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, refresh, refresh} {
		if got := retryAfter(failures, refresh); got != want {
			t.Errorf("retryAfter(%d) = %v, want %v", failures, got, want)
		}
	}
}

// The plugin looks the place up, fetches, and shows the weather; a new
// place or new units fetch again, and bad settings are rejected.
func TestPlugin(t *testing.T) {
	server := fakeOpenMeteo(t)
	t.Setenv(testAPI, server.URL)
	p := plugintest.Launch(t, "weather")
	if got := p.Info.GetName(); got != "weather" {
		t.Errorf("name %q, want weather", got)
	}
	for _, s := range []map[string]any{
		{},
		{"location": "Berlin", "latitude": 52.5, "longitude": 13.4},
		{"latitude": 52.5},
		{"location": "Berlin", "units": "kelvin"},
		{"location": "Berlin", "refresh": "1s"},
		{"location": "Berlin", "colour": "red"},
	} {
		if err := p.Configure(s); status.Code(err) != codes.InvalidArgument {
			t.Errorf("settings %v: %v, want INVALID_ARGUMENT", s, err)
		}
	}
	if err := p.Configure(map[string]any{"location": "Springfield, Illinois"}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		texts := plugintest.Texts(v.GetBody())
		return v.GetTitle() == "Springfield, Illinois" && len(texts) == 2 && texts[0] == "Feels like 10°C · Humidity 81%" &&
			strings.HasSuffix(texts[1], " · Open-Meteo")
	})
	if err := p.Configure(map[string]any{"location": "Springfield, Illinois", "units": "imperial", "label": "Home"}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		texts := plugintest.Texts(v.GetBody())
		return v.GetTitle() == "Home" && len(texts) == 2 && texts[0] == "Feels like 49°F · Humidity 81%"
	})
	if err := p.Configure(map[string]any{"location": "Atlantis"}); err != nil {
		t.Fatal(err)
	}
	p.AwaitTextsFunc(func(texts []string) bool {
		return len(texts) == 2 && strings.HasPrefix(texts[0], `no such place named "Atlantis"`)
	})
}

// The views fit the least body the plugin asks for, minBody, at the
// default text size.
func TestMinBody(t *testing.T) {
	now := time.Now()
	cur := &current{at: now.Add(-24 * time.Hour), temperature: -12, feelsLike: -20, humidity: 100, code: 56}
	// The longest error a fetch gives in a few words.
	refused := describe(&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)})
	views := []sdk.View{
		view(now, panelState{query: query{location: "Berlin"}}),
		view(now, panelState{query: query{location: "Berlin"}, err: refused}),
		view(now, panelState{query: query{lat: -33.8688, lon: -151.2093, imperial: true}, cur: cur, refresh: 15 * time.Minute}),
		view(now, panelState{place: &place{name: "Springfield", region: "Illinois"}, cur: cur, err: refused}),
		view(now, panelState{place: &place{name: "Springfield", region: "Illinois"}, cur: cur, err: describe(&net.DNSError{Name: "api.open-meteo.com", Err: "no such host"})}),
	}
	for _, width := range []int{124, 168, 300, 432, 600, 900} {
		width = max(width, minBody.Width)
		for i, v := range views {
			if !ui.ViewFits(v.Proto(), image.Pt(width, minBody.Height), 1) {
				t.Errorf("view %d does not fit %d by %d dp", i, width, minBody.Height)
			}
		}
	}
}

func TestTitle(t *testing.T) {
	for _, tc := range []struct {
		s    panelState
		want string
	}{
		{panelState{label: "Home", place: &place{name: "Berlin", region: "Germany"}}, "Home"},
		{panelState{place: &place{name: "Springfield", region: "Illinois"}}, "Springfield, Illinois"},
		{panelState{place: &place{name: "Singapore"}}, "Singapore"},
		{panelState{query: query{location: "Berlin"}}, "Berlin"},
		{panelState{query: query{lat: 52.52, lon: -13.41}}, "52.52° N, 13.41° W"},
	} {
		if got := title(tc.s); got != tc.want {
			t.Errorf("title(%+v) = %q, want %q", tc.s, got, tc.want)
		}
	}
	for _, tc := range [][4]string{
		{"Berlin", "State of Berlin", "Germany", "Germany"},
		{"Springfield", "Illinois", "United States", "Illinois"},
		{"Singapore", "", "Singapore", ""},
		{"Monaco", "", "Monaco", ""},
		{"Atlantis", "Western Cape", "South Africa", "Western Cape"},
		{"York", "New York", "United States", "New York"},
		{"Hamburg", "Hamburg", "Germany", "Germany"},
		{"Hamburg", "Land Hamburg", "Germany", "Germany"},
		{"Mexico City", "Mexico City", "Mexico", "Mexico"},
	} {
		if got := region(tc[0], tc[1], tc[2]); got != tc[3] {
			t.Errorf("region(%q, %q, %q) = %q, want %q", tc[0], tc[1], tc[2], got, tc[3])
		}
	}
}

// The loop keeps the last conditions on show when a fetch fails,
// retries, and shows fresh ones when it succeeds again; a new label
// alone fetches nothing.
func TestLoop(t *testing.T) {
	var failing atomic.Bool
	var forecasts, searches atomic.Int32
	fake := fakeOpenMeteo(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/forecast":
			forecasts.Add(1)
			if failing.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		case "/v1/search":
			searches.Add(1)
		}
		fake.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	useFake(t, server)
	tick, base := tickEvery, retryBase
	tickEvery, retryBase = 5*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { tickEvery, retryBase = tick, base })

	views := make(chan sdk.View)
	w := &weather{views: views, wake: make(chan struct{}, 1), quit: make(chan struct{}), stopped: make(chan struct{})}
	// Stopped before the fakes and the waits go back, reading its views
	// meanwhile.
	t.Cleanup(func() {
		close(w.quit)
		for {
			select {
			case <-views:
			case <-w.stopped:
				return
			}
		}
	})
	// Each view the plugin sends, read by the test until ok.
	await := func(what string, ok func(v sdk.View) bool) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case v := <-views:
				if ok(v) {
					return
				}
			case <-deadline:
				t.Fatalf("no view with %s", what)
			}
		}
	}
	configure := func(settings map[string]any) {
		t.Helper()
		done := make(chan error)
		go func() { done <- w.configure(t.Context(), sdk.Config{Settings: settings}) }()
		for {
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
				return
			case <-views:
			}
		}
	}
	footer := func(v sdk.View) string {
		if t := texts(v); len(t) == 2 {
			return t[1]
		}
		return ""
	}

	configure(map[string]any{"location": "Berlin"})
	await("the conditions", func(v sdk.View) bool { return strings.HasSuffix(footer(v), " · Open-Meteo") })
	if f, s := forecasts.Load(), searches.Load(); f != 1 || s != 1 {
		t.Errorf("%d forecasts and %d searches, want 1 and 1", f, s)
	}
	configure(map[string]any{"location": "Berlin", "label": "Home"})
	await("the label", func(v sdk.View) bool { return v.Title == "Home" && strings.HasSuffix(footer(v), " · Open-Meteo") })
	if f, s := forecasts.Load(), searches.Load(); f != 1 || s != 1 {
		t.Errorf("after a new label: %d forecasts and %d searches, want 1 and 1", f, s)
	}

	// Due again at once, and failing: the conditions stay, marked.
	failing.Store(true)
	w.mu.Lock()
	w.cfg.refresh = 20 * time.Millisecond
	w.mu.Unlock()
	await("the failure", func(v sdk.View) bool {
		return stat(v).GetValue() == "12" && strings.HasSuffix(footer(v), " · Open-Meteo · HTTP 503")
	})
	n := forecasts.Load()
	await("a retry", func(sdk.View) bool { return forecasts.Load() > n })
	failing.Store(false)
	await("fresh conditions", func(v sdk.View) bool { return strings.HasSuffix(footer(v), " · Open-Meteo") })
	if s := searches.Load(); s != 1 {
		t.Errorf("Berlin looked up %d times, want once", s)
	}
}

// A failure describe does not know keeps its cause in the detail, but
// not the URL.
func TestDescribeOther(t *testing.T) {
	err := describe(&url.Error{Op: "Get", URL: "https://api.open-meteo.com/v1/forecast?latitude=52.52", Err: io.ErrUnexpectedEOF})
	if brief(err) != "request failed" || err.Error() != "the request to Open-Meteo failed: unexpected EOF" {
		t.Errorf("describe = %q, %q", brief(err), err)
	}
}

// A clock set back after a fetch makes the next one due at once.
func TestClockSetBack(t *testing.T) {
	tick := tickEvery
	tickEvery = time.Millisecond
	t.Cleanup(func() { tickEvery = tick })
	views := make(chan sdk.View, 1)
	w := &weather{views: views, cfg: config{refresh: time.Hour}, tried: time.Now().Add(8 * time.Hour)}
	done := make(chan bool)
	go func() { done <- w.waitDue() }()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case due := <-done:
			if !due {
				t.Error("waitDue reported quit")
			}
			return
		case <-views:
		case <-deadline:
			t.Fatal("not due with the latest fetch in the future")
		}
	}
}
