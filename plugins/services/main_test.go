// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sd "github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	"github.com/joncbenderkh/tessera/internal/ui"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
	"github.com/joncbenderkh/tessera/sdk"
)

// testFake names the environment variable that has a copy of the test
// binary, run as the plugin, use the fake managers.
const testFake = "TESSERA_SERVICES_TEST_FAKE"

// realConnect is connect before any test replaces it.
var realConnect = connect

func TestMain(m *testing.M) {
	plugintest.Main(m, func() {
		if os.Getenv(testFake) != "" {
			useFakes(newFakes())
		}
		main()
	})
}

var changed = time.Date(2026, 11, 25, 9, 30, 0, 0, time.Local)

// fakeManager is a manager with units it knows by name, and aliases.
type fakeManager struct {
	sc scope

	mu        sync.Mutex
	known     map[string]unit
	aliases   map[string]string
	err       error // what every call returns, if set
	restarted []string
}

func (f *fakeManager) units(_ context.Context, names []string) ([]unit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []unit
	for _, n := range names {
		id := n
		if a, ok := f.aliases[n]; ok {
			id = a
		}
		u, ok := f.known[id]
		if !ok {
			u = unit{id: id, load: "not-found", active: "inactive", sub: "dead"}
		}
		u.ref = ref{f.sc, n}
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeManager) failed(context.Context) ([]unit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var out []unit
	for _, id := range slicesSorted(f.known) {
		if u := f.known[id]; u.active == "failed" {
			u.ref = ref{f.sc, id}
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeManager) restart(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarted = append(f.restarted, name)
	if u, ok := f.known[name]; ok {
		u.active, u.sub = "active", "running"
		f.known[name] = u
	}
	return f.err
}

func (f *fakeManager) close() {}

func slicesSorted(m map[string]unit) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// newFakes is a system manager with dbus.service, an alias of
// dbus-broker.service, sshd, and a failed nfs mount; and a user one with
// syncthing, and a failed backup.
func newFakes() [2]*fakeManager {
	sys := &fakeManager{sc: system, known: map[string]unit{
		"dbus-broker.service": {id: "dbus-broker.service", description: "D-Bus System Message Bus", load: "loaded", active: "active", sub: "running", since: changed},
		"sshd.service":        {id: "sshd.service", description: "OpenSSH server daemon", load: "loaded", active: "active", sub: "running", since: changed},
		"mnt-nas.mount":       {id: "mnt-nas.mount", description: "NAS", load: "loaded", active: "failed", sub: "failed", since: changed},
	}, aliases: map[string]string{"dbus.service": "dbus-broker.service"}}
	usr := &fakeManager{sc: user, known: map[string]unit{
		"syncthing.service": {id: "syncthing.service", description: "Syncthing", load: "loaded", active: "active", sub: "running", since: changed},
		"backup.service":    {id: "backup.service", description: "Nightly backup", load: "loaded", active: "failed", sub: "failed", since: changed},
	}}
	return [2]*fakeManager{sys, usr}
}

// useFakes has connect give fakes; a nil one fails to connect.
func useFakes(fakes [2]*fakeManager) {
	connect = func(_ context.Context, sc scope) (manager, error) {
		if fakes[sc] == nil {
			return nil, errors.New("no bus")
		}
		return fakes[sc], nil
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]ref{
		"sshd":                    {system, "sshd.service"},
		" sshd.service ":          {system, "sshd.service"},
		"user:syncthing":          {user, "syncthing.service"},
		"mnt-nas.mount":           {system, "mnt-nas.mount"},
		"getty@tty1.service":      {system, "getty@tty1.service"},
		`systemd-fsck@dev-x\x2d1`: {system, `systemd-fsck@dev-x\x2d1.service`},
		// A dot is no type: systemd leaves such a name out of its reply.
		"user:org.gnome.Shell@wayland": {user, "org.gnome.Shell@wayland.service"},
		"foo.bar":                      {system, "foo.bar.service"},
	} {
		if got, err := parseRef(in); err != nil || got != want {
			t.Errorf("parseRef(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	// A template, which systemd fails the whole call for, is no unit.
	for _, bad := range []string{"", "user:", "a b", "a/b.service", ".service", "x.", ".x", strings.Repeat("a", 250) + ".service", "user:foo@", "foo@.service", "@x.service"} {
		if _, err := parseRef(bad); err == nil {
			t.Errorf("parseRef(%q) succeeded", bad)
		}
	}
}

func TestCheck(t *testing.T) {
	cfg, err := check(settings{Units: []string{"sshd", "user:syncthing", "sshd.service"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []ref{{system, "sshd.service"}, {user, "syncthing.service"}}; !slices.Equal(cfg.units, want) || cfg.interval != 10*time.Second {
		t.Errorf("check = %+v", cfg)
	}
	for _, s := range []settings{{Units: []string{"a b"}}, {Interval: "1ms"}} {
		if _, err := check(s); err == nil {
			t.Errorf("check(%+v) succeeded", s)
		}
	}
}

// panel is a services panel whose views go nowhere, and the texts of
// its view now.
func panel(t *testing.T, units ...string) (*services, func() []string) {
	t.Helper()
	views := make(chan sdk.View)
	go func() {
		for range views {
		}
	}()
	cfg, err := check(settings{Units: units})
	if err != nil {
		t.Fatal(err)
	}
	s := &services{views: views, cfg: cfg, gen: 1, busy: map[string]bool{}, results: map[string]string{}, failedAt: map[string]time.Time{}, repoll: func() {}}
	return s, func() []string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return plugintest.Texts(view(time.Now(), s.state()).Proto().GetBody())
	}
}

func rowNames(s *services) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, u := range s.rows {
		out = append(out, u.ref.String()+"="+state(u))
	}
	return out
}

// The units named come in their order, under their names, then the
// others failed, each once.
func TestPoll(t *testing.T) {
	useFakes(newFakes())
	s, _ := panel(t, "user:syncthing", "dbus", "nosuch", "user:backup")
	s.poll(t.Context())
	want := []string{"user:syncthing.service=active", "dbus.service=active", "nosuch.service=missing", "user:backup.service=failed", "mnt-nas.mount=failed"}
	if got := rowNames(s); !slices.Equal(got, want) {
		t.Errorf("rows %q, want %q", got, want)
	}
}

// A manager that cannot be reached is a warning only if a unit named is
// its, or the other cannot be reached either.
func TestUnreachable(t *testing.T) {
	fakes := newFakes()
	useFakes([2]*fakeManager{fakes[system], nil})
	s, last := panel(t, "sshd")
	s.poll(t.Context())
	texts := last()
	if slices.ContainsFunc(texts, func(x string) bool { return strings.Contains(x, "Cannot read") }) {
		t.Errorf("warned of the user manager, which no unit named needs: %q", texts)
	}
	s, last = panel(t, "sshd", "user:syncthing")
	s.poll(t.Context())
	texts = last()
	if !slices.Contains(texts, "Cannot read the user manager's services: no bus") {
		t.Errorf("no warning of the user manager: %q", texts)
	}
	if got := rowNames(s); !slices.Equal(got, []string{"sshd.service=active", "mnt-nas.mount=failed"}) {
		t.Errorf("rows %q", got)
	}
}

// Restart goes to a user's own unit only, and polls again after.
func TestRestart(t *testing.T) {
	fakes := newFakes()
	useFakes(fakes)
	fakes[user].known["media.mount"] = unit{id: "media.mount", load: "loaded", active: "failed", sub: "failed"}
	s, _ := panel(t, "sshd", "user:backup")
	s.poll(t.Context())
	for _, id := range []string{restartPrefix + "sshd.service", restartPrefix + "user:nosuch.service", restartPrefix + "user:media.mount", restartPrefix + "user:backup.service"} {
		if err := s.activate(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		fakes[user].mu.Lock()
		got := slices.Clone(fakes[user].restarted)
		fakes[user].mu.Unlock()
		if len(got) > 0 {
			if !slices.Equal(got, []string{"backup.service"}) {
				t.Errorf("restarted %v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not restarted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !slices.Equal(fakes[user].restarted, []string{"backup.service"}) {
		t.Errorf("restarted %v, want backup.service alone", fakes[user].restarted)
	}
	if len(fakes[system].restarted) != 0 {
		t.Errorf("a system unit was restarted: %v", fakes[system].restarted)
	}
}

func TestView(t *testing.T) {
	now := changed.Add(2 * time.Hour)
	backup := unit{ref: ref{user, "backup.service"}, id: "backup.service", description: "Nightly backup", load: "loaded", active: "failed", sub: "failed", since: changed}
	dbus := unit{ref: ref{system, "dbus.service"}, id: "dbus-broker.service", load: "loaded", active: "active", sub: "running", since: changed}
	s := panelState{rows: []unit{dbus, backup}, polled: true, busy: map[string]bool{}, results: map[string]string{}}
	v := view(now, s)
	if got := plugintest.Texts(v.Proto().GetBody()); !slices.Equal(got, []string{"2 units · 1 failed"}) {
		t.Errorf("texts %q", got)
	}
	var items []*tesserav1.ListItem
	for _, w := range plugintest.Widgets(v.Proto().GetBody()) {
		items = append(items, w.GetList().GetItems()...)
	}
	if len(items) != 2 || items[1].GetLeading() != "failed" || items[1].GetDetail() != "user · failed · since 09:30" || items[1].GetActionId() != unitPrefix+"user:backup.service" {
		t.Errorf("items %v", items)
	}

	// Details: a user's unit has Restart, a system one does not; an
	// alias says so.
	actions := func(s panelState) []string {
		var out []string
		for _, w := range plugintest.Widgets(view(now, s).Proto().GetBody()) {
			for _, it := range w.GetList().GetItems() {
				out = append(out, it.GetActionId())
			}
		}
		return out
	}
	s.selected = "user:backup.service"
	if got := actions(s); !slices.Contains(got, restartPrefix+"user:backup.service") || !slices.Contains(got, hideAction) {
		t.Errorf("user unit's actions %q", got)
	}
	s.busy["user:backup.service"] = true
	if got := actions(s); slices.Contains(got, restartPrefix+"user:backup.service") {
		t.Errorf("Restart offered while restarting: %q", got)
	}
	mount := unit{ref: ref{user, "media.mount"}, id: "media.mount", load: "loaded", active: "failed", sub: "failed"}
	s.rows, s.selected = append(s.rows, mount), "user:media.mount"
	if got := actions(s); slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, restartPrefix) }) {
		t.Errorf("a user's mount's actions %q", got)
	}
	s.rows = s.rows[:2]
	s.selected = "dbus.service"
	if got := actions(s); slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, restartPrefix) }) {
		t.Errorf("system unit's actions %q", got)
	}
	if got := plugintest.Texts(view(now, s).Proto().GetBody()); !slices.Contains(got, "An alias of dbus-broker.service") {
		t.Errorf("no alias in %q", got)
	}
}

func TestSince(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(-time.Hour), "11:00"},
		{now.AddDate(0, 0, -2), "Mon 12:00"},
		{now.AddDate(0, 0, -30), "Oct 26"},
	} {
		if got := since(tc.t, now); got != tc.want {
			t.Errorf("since(%v) = %q, want %q", tc.t, got, tc.want)
		}
	}
}

// The plugin, run as the host runs it, rejects bad settings and lists
// the units.
func TestPlugin(t *testing.T) {
	t.Setenv(testFake, "1")
	p := plugintest.Launch(t, "services")
	for _, s := range []map[string]any{
		{"units": []any{"a b"}},
		{"units": "sshd"},
		{"interval": "1ms"},
		{"colour": "red"},
	} {
		if err := p.Configure(s); status.Code(err) != codes.InvalidArgument {
			t.Errorf("settings %v: %v, want INVALID_ARGUMENT", s, err)
		}
	}
	if err := p.Configure(map[string]any{"units": []any{"sshd", "user:syncthing"}}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool {
		var titles []string
		for _, w := range plugintest.Widgets(v.GetBody()) {
			for _, it := range w.GetList().GetItems() {
				titles = append(titles, it.GetTitle())
			}
		}
		return slices.Equal(titles, []string{"sshd.service", "syncthing.service", "mnt-nas.mount", "backup.service"})
	})
}

// The views fit the least body the plugin asks for, minBody.
func TestMinBody(t *testing.T) {
	now := changed.Add(time.Hour)
	long := unit{ref: ref{user, "systemd-tmpfiles-clean-and-something.service"}, id: "x", description: strings.Repeat("A long description ", 6), load: "loaded", active: "deactivating", sub: "stop-sigterm", since: changed.AddDate(0, -1, 0)}
	rows := []unit{long, long, long, long}
	views := []sdk.View{
		view(now, panelState{}),
		view(now, panelState{polled: true}),
		view(now, panelState{polled: true, rows: rows, errs: [2]error{errors.New("connection refused"), errors.New("no bus")}}),
		view(now, panelState{polled: true, rows: rows, selected: long.ref.String(), results: map[string]string{long.ref.String(): "Access denied"}}),
	}
	for _, width := range []int{200, 300, 432, 600} {
		for i, v := range views {
			if !ui.ViewFits(v.Proto(), image.Pt(width, minBody.Height), 1) {
				t.Errorf("view %d does not fit %d by %d dp", i, width, minBody.Height)
			}
		}
	}
}

// A connection is made with a context that outlives the poll, as the
// D-Bus connection closes when it ends.
func TestConnectionOutlivesPoll(t *testing.T) {
	fakes := newFakes()
	var mu sync.Mutex
	var made []context.Context
	connect = func(ctx context.Context, sc scope) (manager, error) {
		mu.Lock()
		defer mu.Unlock()
		made = append(made, ctx)
		return fakes[sc], nil
	}
	s, _ := panel(t, "sshd")
	ctx, cancel := context.WithCancel(t.Context())
	s.poll(ctx)
	cancel()
	if len(made) != 2 {
		t.Fatalf("%d connections made, want 2", len(made))
	}
	for i, c := range made {
		if c.Err() != nil {
			t.Errorf("connection %d's context ended with the poll: %v", i, c.Err())
		}
	}
}

// A manager that cannot be read for a while keeps its units as last
// read, in their places.
func TestUnreachableKeepsUnits(t *testing.T) {
	fakes := newFakes()
	useFakes(fakes)
	s, last := panel(t, "user:syncthing", "sshd")
	s.poll(t.Context())
	fakes[user].mu.Lock()
	fakes[user].err = errors.New("connection closed")
	fakes[user].mu.Unlock()
	s.poll(t.Context())
	want := []string{"user:syncthing.service=active", "sshd.service=active", "mnt-nas.mount=failed", "user:backup.service=failed"}
	if got := rowNames(s); !slices.Equal(got, want) {
		t.Errorf("rows %q, want %q", got, want)
	}
	if !slices.Contains(last(), "Cannot read the user manager's services: connection closed") {
		t.Errorf("no warning: %q", last())
	}
}

// A unit no longer named, read while its manager was reachable, goes
// when the settings change, unless it failed.
func TestUnnamedGoes(t *testing.T) {
	fakes := newFakes()
	useFakes(fakes)
	s, _ := panel(t, "sshd")
	s.poll(t.Context())
	fakes[system].mu.Lock()
	fakes[system].err = errors.New("connection closed")
	fakes[system].mu.Unlock()
	cfg, err := check(settings{Units: []string{"user:syncthing"}})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.cfg, s.gen = cfg, s.gen+1
	s.mu.Unlock()
	s.poll(t.Context())
	want := []string{"user:syncthing.service=active", "user:backup.service=failed", "mnt-nas.mount=failed"}
	if got := rowNames(s); !slices.Equal(got, want) {
		t.Errorf("rows %q, want %q", got, want)
	}
}

// A connection being made is made once, however many wait for it, each
// within its own time; and kept once made.
func TestConnectOnce(t *testing.T) {
	release := make(chan struct{})
	m := newFakes()[system]
	var mu sync.Mutex
	calls := 0
	connect = func(context.Context, scope) (manager, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		return m, nil
	}
	s, _ := panel(t, "sshd")
	for range 3 {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		start := time.Now()
		if _, err := s.manager(ctx, system); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("manager = %v, want the deadline", err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("waited %v for a connection", d)
		}
		cancel()
	}
	close(release)
	got, err := s.manager(t.Context(), system)
	if err != nil || got != manager(m) {
		t.Errorf("manager = %v, %v; want the one made", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("connected %d times, want once", calls)
	}
}

// stallingManager is a fake that answers nothing until ctx ends.
type stallingManager struct{ *fakeManager }

func (stallingManager) units(ctx context.Context, _ []string) ([]unit, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A poll canceled, as the settings change, keeps the connection, which a
// restart may be waiting on.
func TestCanceledPollKeeps(t *testing.T) {
	fakes := newFakes()
	connect = func(_ context.Context, sc scope) (manager, error) {
		return stallingManager{fakes[sc]}, nil
	}
	s, _ := panel(t, "sshd", "user:syncthing")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		s.poll(ctx)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.managers[system] == nil || s.managers[user] == nil {
		t.Errorf("connections dropped: %v", s.managers)
	}
}

// A manager slow to answer does not take the other's time.
func TestSlowManager(t *testing.T) {
	fakes := newFakes()
	connect = func(_ context.Context, sc scope) (manager, error) {
		if sc == system {
			return stallingManager{fakes[sc]}, nil
		}
		return fakes[sc], nil
	}
	s, _ := panel(t, "sshd", "user:syncthing")
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	s.poll(ctx)
	s.mu.Lock()
	errs := s.errs
	s.mu.Unlock()
	if errs[user] != nil || !slices.Contains(rowNames(s), "user:syncthing.service=active") {
		t.Errorf("user manager not read: %v, rows %q", errs[user], rowNames(s))
	}
}

// With units named, none read is no news of failures.
func TestEmptyText(t *testing.T) {
	empty := func(s panelState) string {
		for _, w := range plugintest.Widgets(view(time.Now(), s).Proto().GetBody()) {
			if l := w.GetList(); l.GetEmptyText() != "" {
				return l.GetEmptyText()
			}
		}
		return ""
	}
	if got := empty(panelState{polled: true}); got != "No failed services" {
		t.Errorf("with none named: %q", got)
	}
	if got := empty(panelState{polled: true, named: []ref{{system, "sshd.service"}}, errs: [2]error{errors.New("no bus")}}); got != "No units read" {
		t.Errorf("with one named: %q", got)
	}
}

func TestListed(t *testing.T) {
	rows := make([]unit, maxItems+5)
	for i := range rows {
		rows[i] = unit{ref: ref{system, fmt.Sprintf("u%d.service", i)}, active: "failed"}
	}
	v := view(time.Now(), panelState{rows: rows, polled: true})
	if got := plugintest.Texts(v.Proto().GetBody()); !slices.Contains(got, "100 of 105 units · 105 failed") {
		t.Errorf("texts %q", got)
	}
}

// A name the manager leaves out, or fails the call for, is not found,
// and the others keep their places; a failure to reach it is an error.
func TestByName(t *testing.T) {
	list := func(_ context.Context, names []string) ([]sd.UnitStatus, error) {
		if len(names) > 4 {
			return nil, godbus.Error{Name: "org.freedesktop.DBus.Error.LimitsExceeded"}
		}
		var out []sd.UnitStatus
		for _, n := range names {
			switch n {
			case "bad.service":
				continue
			case "tmpl@.service":
				return nil, godbus.Error{Name: "org.freedesktop.DBus.Error.InvalidArgs"}
			case "gone.service":
				return nil, errors.New("connection closed")
			case "nobody.service":
				return nil, godbus.Error{Name: "org.freedesktop.DBus.Error.ServiceUnknown"}
			}
			out = append(out, sd.UnitStatus{Name: n, LoadState: "loaded", ActiveState: "active"})
		}
		return out, nil
	}
	st, err := byName(t.Context(), []string{"a.service", "bad.service", "tmpl@.service", "b.service"}, list)
	var got []string
	for _, u := range st {
		got = append(got, u.Name+"="+u.LoadState)
	}
	if want := []string{"a.service=loaded", "bad.service=not-found", "tmpl@.service=not-found", "b.service=loaded"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("byName = %q, %v; want %q", got, err, want)
	}
	if st, err := byName(t.Context(), []string{"a.service", "b.service", "c.service", "d.service", "e.service"}, list); err != nil || len(st) != 5 || st[4].LoadState != "loaded" {
		t.Errorf("byName over the limit = %v, %v", st, err)
	}
	for _, n := range []string{"gone.service", "nobody.service"} {
		if _, err := byName(t.Context(), []string{"a.service", n}, list); err == nil {
			t.Errorf("byName hid a failure to reach the manager: %s", n)
		}
	}
}

// failingFailed is a fake whose failed units cannot be read.
type failingFailed struct{ *fakeManager }

func (failingFailed) failed(context.Context) ([]unit, error) {
	return nil, errors.New("no reply")
}

// A manager read in half keeps its units as last read, each once.
func TestHalfRead(t *testing.T) {
	fakes := newFakes()
	useFakes(fakes)
	s, _ := panel(t, "user:syncthing", "sshd")
	s.poll(t.Context())
	connect = func(_ context.Context, sc scope) (manager, error) {
		if sc == user {
			return failingFailed{fakes[sc]}, nil
		}
		return fakes[sc], nil
	}
	s.mu.Lock()
	s.managers = [2]manager{}
	s.mu.Unlock()
	s.poll(t.Context())
	want := []string{"user:syncthing.service=active", "sshd.service=active", "mnt-nas.mount=failed", "user:backup.service=failed"}
	if got := rowNames(s); !slices.Equal(got, want) {
		t.Errorf("rows %q, want %q", got, want)
	}
}

// A unit whose name starts as an action's does is selected, not acted on.
func TestActionNames(t *testing.T) {
	fakes := newFakes()
	fakes[system].known["restart:foo.service"] = unit{id: "restart:foo.service", load: "loaded", active: "active", sub: "running"}
	useFakes(fakes)
	s, _ := panel(t, "restart:foo.service")
	s.poll(t.Context())
	if err := s.activate(t.Context(), unitPrefix+"restart:foo.service"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selected != "restart:foo.service" || len(s.busy) != 0 {
		t.Errorf("selected %q, busy %v", s.selected, s.busy)
	}
}

// A bus that takes the connection but never answers is given up on.
func TestHandshake(t *testing.T) {
	sock := t.TempDir() + "/bus"
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+sock)
	defer func(d time.Duration) { handshake = d }(handshake)
	handshake = 100 * time.Millisecond
	done := make(chan error, 1)
	connect := realConnect
	go func() {
		_, err := connect(context.Background(), user)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errHandshake) {
			t.Errorf("connect = %v, want %v", err, errHandshake)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connect waited on a bus that never answers")
	}
}

// lostWait is a fake whose restarts are asked for, but whose end is not
// heard of; ran says whether the restart ran.
type lostWait struct {
	*fakeManager
	ran bool
}

func (l lostWait) restart(_ context.Context, name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if u, ok := l.known[name]; ok && l.ran {
		u.active, u.sub, u.since = "active", "running", time.Now().Add(time.Millisecond)
		l.known[name] = u
	}
	return errWaitLost
}

// A restart whose end was not heard of is read again: it failed only
// if the unit is not running anew.
func TestLostWait(t *testing.T) {
	for _, ran := range []bool{true, false} {
		fakes := newFakes()
		connect = func(_ context.Context, sc scope) (manager, error) {
			if sc == user {
				return lostWait{fakes[sc], ran}, nil
			}
			return fakes[sc], nil
		}
		s, _ := panel(t, "user:syncthing")
		s.poll(t.Context())
		s.restart(ref{user, "syncthing.service"})
		s.mu.Lock()
		result := s.results["user:syncthing.service"]
		s.mu.Unlock()
		if ran && result != "" || !ran && result == "" {
			t.Errorf("ran %v: result %q", ran, result)
		}
	}
}

// Restart is offered only where there is a unit to restart.
func TestRestartable(t *testing.T) {
	for _, tc := range []struct {
		u    unit
		want bool
	}{
		{unit{ref: ref{user, "a.service"}, load: "loaded"}, true},
		{unit{ref: ref{user, "a.service"}, load: "not-found"}, false},
		{unit{ref: ref{user, "a.service"}, load: "masked"}, false},
		{unit{ref: ref{user, "a.timer"}, load: "loaded"}, false},
		{unit{ref: ref{system, "a.service"}, load: "loaded"}, false},
	} {
		if got := restartable(tc.u); got != tc.want {
			t.Errorf("restartable(%+v) = %v", tc.u, got)
		}
	}
}

// A restart's failure goes once the unit changes state after it.
func TestFailureClears(t *testing.T) {
	fakes := newFakes()
	useFakes(fakes)
	s, _ := panel(t, "user:syncthing")
	key := "user:syncthing.service"
	s.mu.Lock()
	s.results[key], s.failedAt[key] = "Access denied", changed.Add(-time.Hour)
	s.mu.Unlock()
	s.poll(t.Context())
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.results[key]; ok {
		t.Errorf("failure kept after the unit changed state: %v", s.results)
	}
}
