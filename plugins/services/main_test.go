// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"image"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	} {
		if got, err := parseRef(in); err != nil || got != want {
			t.Errorf("parseRef(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "user:", "a b", "a/b.service", ".service", "x.", strings.Repeat("a", 250) + ".service"} {
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
	s := &services{views: views, cfg: cfg, gen: 1, busy: map[string]bool{}, results: map[string]string{}, repoll: func() {}}
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
	s, _ := panel(t, "sshd", "user:backup")
	s.poll(t.Context())
	for _, id := range []string{restartPrefix + "sshd.service", restartPrefix + "user:nosuch.service", restartPrefix + "user:backup.service"} {
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
	if got := plugintest.Texts(v.Proto().GetBody()); !slices.Equal(got, []string{"2 services · 1 failed"}) {
		t.Errorf("texts %q", got)
	}
	var items []*tesserav1.ListItem
	for _, w := range plugintest.Widgets(v.Proto().GetBody()) {
		items = append(items, w.GetList().GetItems()...)
	}
	if len(items) != 2 || items[1].GetLeading() != "failed" || items[1].GetDetail() != "user · failed · since 09:30" || items[1].GetActionId() != "user:backup.service" {
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
	var made []context.Context
	connect = func(ctx context.Context, sc scope) (manager, error) {
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
