// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

func TestMain(m *testing.M) { plugintest.Main(m, main) }

func TestCheck(t *testing.T) {
	for _, s := range []settings{
		{Refresh: "15m"},
		{Sources: []string{"/a.ics"}, Refresh: "30s"},
		{Sources: []string{"/a.ics"}, Refresh: "25h"},
		{Sources: []string{"/a.ics"}, Refresh: "soon"},
		{Sources: []string{"calendar.ics"}, Refresh: "15m"},
		{Sources: []string{"ftp://example.com/a.ics"}, Refresh: "15m"},
		{Sources: []string{""}, Refresh: "15m"},
	} {
		if _, err := check(s); err == nil {
			t.Errorf("check(%+v) succeeded", s)
		}
	}
	cfg, err := check(settings{Sources: []string{" webcal://example.com/a.ics ", "~/cal.ics"}, Refresh: "1m", Title: " Planned breaks "})
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := []source{{url: "https://example.com/a.ics"}, {path: filepath.Join(home, "cal.ics")}}
	if !slices.Equal(cfg.sources, want) || cfg.refresh != time.Minute || cfg.title != "Planned breaks" {
		t.Errorf("check = %+v, want sources %+v", cfg, want)
	}
}

// A source given twice is read once, and an empty refresh keeps its
// default.
func TestCheckDuplicates(t *testing.T) {
	d := t.TempDir()
	cfg, err := check(settings{Sources: []string{filepath.Join(d, "a.ics"), d + "/b/../a.ics", filepath.Join(d, "a.ics")}, Refresh: " "})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.sources) != 1 || cfg.refresh != 15*time.Minute {
		t.Errorf("check = %+v", cfg)
	}
}

func TestSourceName(t *testing.T) {
	for s, want := range map[source]string{
		{url: "https://calendar.example.com/private-abc123/basic.ics"}: "calendar.example.com",
		{path: "/home/me/work.ics"}:                                    "work.ics",
	} {
		if got := s.String(); got != want {
			t.Errorf("%+v names itself %q, want %q", s, got, want)
		}
	}
	for _, s := range []string{
		"ftp://example.com/secret-token/a.ics",
		"https://cal.example.com/private-secret%zz/basic.ics",
		"https:/cal.example.com/private-secret/basic.ics",
		"https://cal.example.com/private-secret/basic ics\x7f",
		"webcal:cal.example.com/private-secret/basic.ics",
		"https//cal.example.com/private-secret/basic.ics",
		"cal.example.com/calendar/private-secret/basic.ics",
	} {
		if _, err := parseSource(s); err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("parseSource(%q): error %v names the URL's path", s, err)
		}
	}
	// A file URL is told that only web URLs are read, not that it is
	// malformed.
	if _, err := parseSource("file:///home/me/cal.ics"); err == nil || !strings.Contains(err.Error(), "only http") {
		t.Errorf("parseSource(file URL): error %v", err)
	}
}

func TestCalendar(t *testing.T) {
	// Under way, so they are today's whatever the time of day.
	start := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Minute)
	stamp := func(t time.Time) string { return t.Format("20060102T150405Z") }
	file := filepath.Join(t.TempDir(), "work.ics")
	if err := os.WriteFile(file, []byte(ics(`
		UID:review
		SUMMARY:Review
		DTSTART:`+stamp(start)+`
		DTEND:`+stamp(start.Add(3*time.Hour))+`
		DESCRIPTION:Bring the draft`)), 0o644); err != nil {
		t.Fatal(err)
	}
	served := ics(`
		UID:sync
		SUMMARY:Sync
		DTSTART:` + stamp(start.Add(10*time.Minute)) + `
		DTEND:` + stamp(start.Add(2*time.Hour)))
	var teamReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/team.ics" {
			teamReads.Add(1)
		}
		if r.URL.Path != "/team.ics" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(served))
	}))
	t.Cleanup(server.Close)

	p := plugintest.Launch(t, "calendar")
	if got := p.Info.GetName(); got != "calendar" {
		t.Errorf("name %q, want calendar", got)
	}
	for _, s := range []map[string]any{
		{},
		{"sources": []any{"relative.ics"}},
		{"sources": "/a.ics"},
		{"sources": []any{"/a.ics"}, "refresh": "1s"},
		{"sources": []any{"/a.ics"}, "colour": "red"},
	} {
		if err := p.Configure(s); status.Code(err) != codes.InvalidArgument {
			t.Errorf("settings %v: %v, want INVALID_ARGUMENT", s, err)
		}
	}
	if err := p.Configure(map[string]any{"sources": []any{file, server.URL + "/team.ics", server.URL + "/private-token/missing.ics"}}); err != nil {
		t.Fatal(err)
	}
	failed := "127.0.0.1 (source 3): HTTP 404 Not Found"
	p.Await(func(v *tesserav1.View) bool {
		texts := plugintest.Texts(v.GetBody())
		return slices.Contains(texts, "2 events") && slices.Contains(texts, failed)
	})
	// The first settings read each feed once, not once and then again.
	time.Sleep(500 * time.Millisecond)
	if n := teamReads.Load(); n != 1 {
		t.Errorf("team.ics read %d times, want 1", n)
	}
	// A new title, or a new tile size, does not read the feeds again.
	if err := p.Configure(map[string]any{"sources": []any{file, server.URL + "/team.ics", server.URL + "/private-token/missing.ics"}, "title": "Team"}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool { return v.GetTitle() == "Team" })
	time.Sleep(500 * time.Millisecond)
	if n := teamReads.Load(); n != 1 {
		t.Errorf("team.ics read %d times after a new title, want 1", n)
	}
	key := "review@" + stamp(start)
	if err := p.Activate(key); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		texts := plugintest.Texts(v.GetBody())
		return slices.Contains(texts, "Review") && slices.Contains(texts, "Bring the draft") && slices.Contains(texts, "Work")
	})
	if err := p.Activate("hide"); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		return !slices.Contains(plugintest.Texts(v.GetBody()), "Bring the draft")
	})
	for _, tc := range []struct{ title, want string }{{"Team", "Team"}, {" ", "Next events"}} {
		if err := p.Configure(map[string]any{"sources": []any{file}, "title": tc.title}); err != nil {
			t.Fatal(err)
		}
		p.Await(func(v *tesserav1.View) bool { return v.GetTitle() == tc.want })
	}
}

// Errors from reading a URL never quote a URL, which may hold a token.
func TestFetchErrorsRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/private-token/%zz/basic.ics")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(server.Close)
	_, err := source{url: server.URL + "/private-token/start.ics"}.fetch(t.Context())
	if err == nil || strings.Contains(err.Error(), "token") {
		t.Errorf("fetch error %v", err)
	}
}

// A status line's reason phrase, which the server writes, is not shown.
func TestFetchStatusRedacted(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			conn.Read(buf)
			conn.Write([]byte("HTTP/1.1 403 Forbidden /private-token/basic.ics\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
			conn.Close()
		}
	}()
	_, err = source{url: "http://" + ln.Addr().String() + "/a.ics"}.fetch(t.Context())
	if err == nil || err.Error() != "HTTP 403 Forbidden" {
		t.Errorf("fetch error %v, want HTTP 403 Forbidden", err)
	}
}

// A file that is not a regular file, such as a pipe, is not read.
func TestFetchNotRegular(t *testing.T) {
	_, err := source{path: t.TempDir()}.fetch(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("fetch error %v", err)
	}
}

// A redirect from https to http is refused, so the URL's token is never
// sent in the clear.
func TestNoDowngrade(t *testing.T) {
	var plain atomic.Int32
	insecure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { plain.Add(1) }))
	t.Cleanup(insecure.Close)
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, insecure.URL+"/private-token/basic.ics", http.StatusFound)
	}))
	t.Cleanup(secure.Close)
	saved := client.Transport
	client.Transport = secure.Client().Transport
	t.Cleanup(func() { client.Transport = saved })
	_, err := source{url: secure.URL + "/a.ics"}.fetch(t.Context())
	if !errors.Is(err, errDowngrade) || plain.Load() != 0 {
		t.Errorf("fetch error %v, %d plain requests", err, plain.Load())
	}
}

// A redirect does not pass the URL, which may hold a token, on as the
// Referer.
func TestRedirectNoReferer(t *testing.T) {
	var referer atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/feed.ics" {
			http.Redirect(w, r, "/feed.ics", http.StatusFound)
			return
		}
		referer.Store(r.Header.Get("Referer"))
		w.Write([]byte(ics("UID:a\nSUMMARY:A\nDTSTART:20261028T100000Z")))
	}))
	t.Cleanup(server.Close)
	if _, err := (source{url: server.URL + "/private-token/start.ics"}).fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := referer.Load(); got != "" {
		t.Errorf("Referer %q", got)
	}
}

// An error reading a response's body never quotes what it sent.
func TestBodyErrorRedacted(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Read(make([]byte, 4096))
			conn.Write([]byte("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n" +
				"5\r\nBEGIN\r\n0\r\nprivate-token-without-colon\r\n\r\n"))
			conn.Close()
		}
	}()
	_, err = source{url: "http://" + ln.Addr().String() + "/a.ics"}.fetch(t.Context())
	if err == nil || strings.Contains(err.Error(), "token") {
		t.Errorf("fetch error %v", err)
	}
}

// A source that is slow to answer holds up only its own events.
func TestSlowSource(t *testing.T) {
	start := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Minute)
	stamp := func(t time.Time) string { return t.Format("20060102T150405Z") }
	file := filepath.Join(t.TempDir(), "work.ics")
	if err := os.WriteFile(file, []byte(ics("UID:r\nSUMMARY:Review\nDTSTART:"+stamp(start)+"\nDTEND:"+stamp(start.Add(3*time.Hour)))), 0o644); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	p := plugintest.Launch(t, "calendar")
	if err := p.Configure(map[string]any{"sources": []any{slow.URL + "/team.ics", file}}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool { return slices.Contains(plugintest.Texts(v.GetBody()), "1 event") })
}

func TestLabel(t *testing.T) {
	cfg, err := check(settings{Sources: []string{
		"https://calendar.example.com/a/basic.ics",
		"webcal://calendar.example.com/a/basic.ics",
		"https://work.example.com/basic.ics",
		"https://calendar.example.com/b/basic.ics",
	}})
	if err != nil {
		t.Fatal(err)
	}
	c := &calendar{cfg: cfg}
	got := []string{c.label(0), c.label(1), c.label(2)}
	// Places in the settings as written, the second a duplicate.
	want := []string{"calendar.example.com (source 1)", "work.example.com", "calendar.example.com (source 4)"}
	if !slices.Equal(got, want) {
		t.Errorf("labels %q, want %q", got, want)
	}
}

// A source kept when the sources change keeps its last feed to fall
// back on and its events, but is read again.
func TestCarry(t *testing.T) {
	a, b, c := source{url: "https://a.example/x"}, source{url: "https://b.example/x"}, source{url: "https://c.example/x"}
	fa, fb := feed{name: "A"}, feed{name: "B"}
	got := carry([]source{a, b}, []read{{feed: fa, done: true}, {feed: fb, events: []event{{key: "x"}}, done: true}}, []source{c, b})
	if len(got) != 2 || got[0].feed.name != "" || got[1].feed.name != "B" || len(got[1].events) != 1 || got[1].done {
		t.Errorf("reads %+v", got)
	}
}

// A source's name is UTF-8, as a view must be.
func TestSourceNameUTF8(t *testing.T) {
	if name := (source{url: "https://%ff.example/cal.ics"}).String(); !utf8.ValidString(name) {
		t.Errorf("name %q", name)
	}
}

// A feed whose repeating events take too long keeps what was followed,
// and says the rest was left out.
func TestReadSourceTooSlow(t *testing.T) {
	saved := expandBudget
	expandBudget = 50 * time.Millisecond
	t.Cleanup(func() { expandBudget = saved })
	now := time.Now().Local()
	stamp := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	var events []string
	for i := range 20 {
		events = append(events, fmt.Sprintf("UID:n%d\nSUMMARY:Never\nDTSTART:%s\nRRULE:FREQ=HOURLY;BYMONTH=2;BYMONTHDAY=31", i, stamp(now)))
	}
	events = append(events, "UID:ok\nSUMMARY:Lunch\nDTSTART:"+stamp(now)+"\nDTEND:"+stamp(now.Add(time.Hour)))
	file := filepath.Join(t.TempDir(), "slow.ics")
	if err := os.WriteFile(file, []byte(ics(events...)), 0o644); err != nil {
		t.Fatal(err)
	}
	r := readSource(t.Context(), source{path: file}, feed{}, now.Add(-time.Minute), now.AddDate(0, 0, 1))
	if !errors.Is(r.err, errTooSlow) || len(r.events) != 1 || r.events[0].title != "Lunch" {
		t.Errorf("err %v, events %+v", r.err, r.events)
	}
}

// The list changes when an event starts or ends, not only each minute.
func TestNextChange(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 15, 0, 0, time.UTC)
	at := func(h, m, s int) time.Time { return time.Date(2026, 10, 28, h, m, s, 0, time.UTC) }
	events := []event{
		{start: at(10, 0, 0), end: at(10, 30, 1)},
		{start: at(11, 0, 0), end: at(12, 0, 0)},
	}
	if got := nextChange(now, events); !got.Equal(at(10, 30, 1)) {
		t.Errorf("next change %v, want 10:30:01", got)
	}
	if got := nextChange(at(12, 0, 0), events); !got.IsZero() {
		t.Errorf("next change %v, want none", got)
	}
}

// A read that never returns, as from a share that went away, is given
// up on when its context ends.
func TestWithinGivesUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	block := make(chan struct{})
	defer close(block)
	_, err := within(ctx, func() ([]byte, error) { <-block; return nil, nil })
	if err == nil {
		t.Error("no error")
	}
}

// Failed reads are tried again sooner than the refresh interval, with
// a backoff.
func TestRetryAfter(t *testing.T) {
	for failures, want := range []time.Duration{15 * time.Minute, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute} {
		if got := retryAfter(failures, 15*time.Minute); got != want {
			t.Errorf("retryAfter(%d) = %v, want %v", failures, got, want)
		}
	}
	if got := retryAfter(100, time.Hour); got != time.Hour {
		t.Errorf("retryAfter(100) = %v", got)
	}
}

// A feed may start with a byte order mark.
func TestFetchBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bom.ics")
	if err := os.WriteFile(path, []byte("\uFEFF"+ics("UID:a\nSUMMARY:A\nDTSTART:20261028T100000Z")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (source{path: path}).fetch(t.Context()); err != nil {
		t.Error(err)
	}
}

// A file whose last read is still waiting is not read again.
func TestFetchStillReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hung.ics")
	reading.Store(path, true)
	defer reading.Delete(path)
	if _, err := (source{path: path}).fetch(t.Context()); !errors.Is(err, errStillReading) {
		t.Errorf("fetch error %v", err)
	}
}
