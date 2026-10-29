// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/joncbenderkh/tessera"
)

const (
	// maxFeedBytes is the largest feed read.
	maxFeedBytes = 16 << 20
	// fetchTimeout bounds the reading of one feed.
	fetchTimeout = 30 * time.Second
)

// source is where a feed is read from: an http or https URL, or a
// file.
type source struct {
	url  string // the URL, if it is one
	path string // the file, otherwise
}

// parseSource reads a source setting: an http, https, webcal, or
// webcals URL, an absolute path, or a path starting with ~/.
func parseSource(s string) (source, error) {
	s = strings.TrimSpace(s)
	if hasScheme(s) {
		u, err := url.Parse(s)
		if err != nil {
			// The error would quote s, and with it any token in it.
			return source{}, fmt.Errorf("source %s: not a valid URL", redact(s))
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
		case "webcal", "webcals":
			// Calendar links that ask for an application; the feed is
			// served over https.
			u.Scheme = "https"
		default:
			return source{}, fmt.Errorf("source %s: only http, https, and webcal URLs are read", redact(s))
		}
		if u.Host == "" {
			return source{}, fmt.Errorf("source %s: not a valid URL", redact(s))
		}
		return source{url: u.String()}, nil
	}
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return source{}, fmt.Errorf("source %q: %v", s, err)
		}
		return source{path: filepath.Join(home, rest)}, nil
	}
	if !filepath.IsAbs(s) {
		// Not quoted: it may be a URL missing its scheme, token and all.
		return source{}, errors.New("a source is neither a URL nor an absolute path: give http, https, or webcal URLs, absolute paths, or paths starting with ~/")
	}
	return source{path: filepath.Clean(s)}, nil
}

// hasScheme reports whether s starts with a URL scheme: a letter, then
// at least one more letter, digit, +, -, or ., then a colon. A Windows
// drive letter is too short to be one.
func hasScheme(s string) bool {
	scheme, _, ok := strings.Cut(s, ":")
	if !ok || len(scheme) < 2 {
		return false
	}
	for i, r := range scheme {
		letter := 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z'
		if !letter && (i == 0 || !('0' <= r && r <= '9' || r == '+' || r == '-' || r == '.')) {
			return false
		}
	}
	return true
}

// String names the source without anything secret: a URL's host, or a
// file's name. Calendar links often carry a token in their path.
func (s source) String() string {
	// A view must be UTF-8, and a host or path need not be.
	if s.path != "" {
		return strings.ToValidUTF8(filepath.Base(s.path), "\uFFFD")
	}
	u, err := url.Parse(s.url)
	if err != nil {
		return "calendar"
	}
	return strings.ToValidUTF8(u.Hostname(), "\uFFFD")
}

// redact keeps a URL's scheme and host, and drops the rest.
func redact(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		scheme, _, _ := strings.Cut(s, ":")
		return scheme + ":…"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// requestError says why a request failed, in words that never quote a
// URL: Go's errors for one quote the URL, and for a redirect the
// Location header, either of which may hold a token.
func requestError(err error) error {
	var dns *net.DNSError
	var op *net.OpError
	var cert *tls.CertificateVerificationError
	switch {
	case errors.Is(err, errRedirects):
		return errRedirects
	case errors.Is(err, errDowngrade):
		return errDowngrade
	case errors.Is(err, context.Canceled):
		return errors.New("cancelled")
	case os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded):
		return errors.New("timed out")
	case errors.As(err, &dns):
		return dns // names the host only
	case errors.As(err, &op):
		return op // names the address only
	case errors.As(err, &cert):
		return errors.New("certificate not trusted")
	default:
		return errors.New("request failed")
	}
}

// client reads the feeds at URLs.
var client = &http.Client{Timeout: fetchTimeout, CheckRedirect: checkRedirect}

// checkRedirect follows up to 10 redirects, as Go does by default, but
// never from https to http, which would send the URL, and any token in
// it, in the clear, and never with the URL redirected from as the
// Referer.
func checkRedirect(req *http.Request, via []*http.Request) error {
	req.Header.Del("Referer")
	if len(via) >= 10 {
		return errRedirects
	}
	for _, prev := range via {
		if prev.URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errDowngrade
		}
	}
	return nil
}

// reading holds the files whose reads have not returned.
var reading sync.Map

var errStillReading = errors.New("the last read of the file has not finished")

var (
	errRedirects = errors.New("too many redirects")
	errDowngrade = errors.New("redirected from https to http")
)

// fetch reads and parses the feed at s.
func (s source) fetch(ctx context.Context) (feed, error) {
	var data []byte
	var err error
	if s.path != "" {
		// A read left waiting holds a thread; another of the same file
		// would only wait too.
		if _, busy := reading.LoadOrStore(s.path, true); busy {
			return feed{}, errStillReading
		}
		data, err = within(ctx, func() ([]byte, error) {
			defer reading.Delete(s.path)
			return readFile(s.path)
		})
	} else {
		data, err = s.get(ctx)
	}
	if err != nil {
		return feed{}, err
	}
	if len(data) > maxFeedBytes {
		return feed{}, fmt.Errorf("the feed is larger than %d MiB", maxFeedBytes>>20)
	}
	// Some Windows tools start a UTF-8 file with a byte order mark.
	return parse(bytes.NewReader(bytes.TrimPrefix(data, []byte("\uFEFF"))))
}

// readFile reads the file at path.
func readFile(path string) ([]byte, error) {
	// Opening a pipe would wait for a writer, and a device may never
	// end.
	if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		// The panel names the file already.
		var perr *os.PathError
		if errors.As(err, &perr) {
			err = perr.Err
		}
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxFeedBytes+1))
}

// within returns read's outcome, or gives up when ctx ends or after
// fetchTimeout: a file on a network share that went away may never
// answer, and the read is left waiting.
func within(ctx context.Context, read func() ([]byte, error)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := read()
		done <- result{data, err}
	}()
	select {
	case r := <-done:
		return r.data, r.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("timed out")
		}
		return nil, errors.New("cancelled")
	}
}

// get reads the feed at s's URL.
func (s source) get(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	req.Header.Set("Accept", "text/calendar")
	req.Header.Set("User-Agent", "tessera-calendar/"+tessera.Version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, requestError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Not resp.Status: its reason phrase is the server's to write.
		return nil, errors.New(strings.TrimSpace(fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		// A malformed response's errors may quote what it sent.
		return nil, requestError(err)
	}
	return data, nil
}
