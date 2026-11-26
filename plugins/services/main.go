// SPDX-License-Identifier: GPL-3.0-or-later

// Command services is the services panel: the state of the systemd
// units the settings name, of the system's manager or the user's, and
// of any other that has failed. Activating one shows its details, and,
// for a user's own service, a Restart; system units are read-only, as
// restarting one needs root.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	godbus "github.com/godbus/dbus/v5"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/plugins/internal/metric"
	"github.com/joncbenderkh/tessera/sdk"
)

const defaultInterval = "10s"

var schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "units": {
      "type": "array",
      "description": "The systemd units to show, such as sshd.service, or user:syncthing.service for one of your own; .service is added to a name unless it ends in a unit type, such as .timer. Any other unit that has failed shows too.",
      "items": {"type": "string"}
    },
    ` + metric.IntervalSchema(defaultInterval) + `
  },
  "additionalProperties": false
}`

type settings struct {
	Units    []string `json:"units"`
	Interval string   `json:"interval"`
}

// ref names a unit and the manager it belongs to.
type ref struct {
	scope scope
	name  string
}

// key is the ref's key in the view and its actions: its manager, and
// its name after a slash, which no unit name has, as a system unit's
// name can start with user:.
func (r ref) key() string { return r.scope.String() + "/" + r.name }

// String is the ref as the settings write it.
func (r ref) String() string {
	if r.scope == user {
		return "user:" + r.name
	}
	return r.name
}

// parseRef reads a unit as the settings name it.
func parseRef(s string) (ref, error) {
	name := strings.TrimSpace(s)
	r := ref{scope: system}
	if rest, ok := strings.CutPrefix(name, "user:"); ok {
		r.scope, name = user, rest
	}
	bare := name == "" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".")
	if !bare && !slices.Contains(unitTypes, unitType(name)) {
		name += ".service"
	}
	if bare || !validName(name) {
		return ref{}, fmt.Errorf("setting \"units\": want unit names, such as sshd.service or user:syncthing.service, not %q", s)
	}
	r.name = name
	return r, nil
}

// unitTypes are the types a unit name can end in.
var unitTypes = []string{"service", "socket", "target", "device", "mount", "automount", "swap", "timer", "path", "slice", "scope"}

// unitType is what follows name's last dot, or "" if it has none.
func unitType(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return ""
}

// validName reports whether name is a systemd unit name, as systemd
// checks one: letters, digits, and :-_.\@, no longer than 255, a type
// after the last dot, and, after an @, an instance, as the manager
// reports nothing of a template.
func validName(name string) bool {
	dot := strings.LastIndexByte(name, '.')
	if len(name) > 255 || dot <= 0 || !slices.Contains(unitTypes, name[dot+1:]) {
		return false
	}
	for _, c := range name {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(":-_.\\@", c)
		if !ok {
			return false
		}
	}
	at := strings.IndexByte(name, '@')
	return at != 0 && (at < 0 || at+1 < dot)
}

// config is the settings, checked.
type config struct {
	units    []ref
	interval time.Duration
}

func check(s settings) (config, error) {
	interval, err := metric.ParseInterval(cmp.Or(strings.TrimSpace(s.Interval), defaultInterval))
	if err != nil {
		return config{}, err
	}
	cfg := config{interval: interval}
	for _, u := range s.Units {
		r, err := parseRef(u)
		if err != nil {
			return config{}, err
		}
		if !slices.Contains(cfg.units, r) {
			cfg.units = append(cfg.units, r)
		}
	}
	return cfg, nil
}

// Action IDs: a unit's row is its ref after unitPrefix, as a ref
// itself can start with restart:; the others name the details' items.
const (
	unitPrefix    = "unit:"
	hideAction    = "hide"
	restartPrefix = "restart:"
)

// services is the panel's state. Configure, Activate, the poller, and
// restarts run concurrently, so mu guards it. Polling starts on the
// first Configure.
type services struct {
	views  chan<- sdk.View
	poller metric.Poller
	// repoll polls now, starting the poller the first time.
	repoll func()

	mu       sync.Mutex
	cfg      config
	gen      int // counts the settings, so a poll of old ones is dropped
	managers [2]manager
	dialing  [2]*dial // connections being made
	rows     []unit
	errs     [2]error // why each manager could not be read, or nil
	polled   bool
	selected string               // the ref of the unit whose details show
	busy     map[string]bool      // units being restarted
	results  map[string]string    // why a unit's last restart failed
	failedAt map[string]time.Time // and when
}

func main() {
	views := make(chan sdk.View)
	s := &services{views: views, busy: map[string]bool{}, results: map[string]string{}, failedAt: map[string]time.Time{}}
	s.repoll = func() { s.poller.Start(s.poll) }
	sdk.Serve(sdk.Plugin{
		Name:         "services",
		Version:      tessera.Version,
		Description:  "The state of systemd services, the system's and your own.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 1, Rows: 2},
		MinBody:      minBody,
		Views:        views,
		Configure:    s.configure,
		Activate:     s.activate,
	})
}

func (s *services) configure(_ context.Context, c sdk.Config) error {
	var set settings
	if err := c.Decode(&set); err != nil {
		return err
	}
	cfg, err := check(set)
	if err != nil {
		return sdk.InvalidSettings("%v", err)
	}
	s.mu.Lock()
	changed := !slices.Equal(cfg.units, s.cfg.units) || cfg.interval != s.cfg.interval || s.gen == 0
	s.cfg = cfg
	if changed {
		s.gen++
	}
	s.show()
	s.mu.Unlock()
	if changed {
		s.repoll()
	}
	return nil
}

// poll reads the units from both managers and shows them.
func (s *services) poll(ctx context.Context) time.Duration {
	s.mu.Lock()
	cfg, gen := s.cfg, s.gen
	s.mu.Unlock()

	// Each manager in its own time, so one slow to answer does not use
	// up the other's.
	var reads [2]read
	var wg sync.WaitGroup
	for _, sc := range []scope{system, user} {
		var names []string
		for _, r := range cfg.units {
			if r.scope == sc {
				names = append(names, r.name)
			}
		}
		wg.Go(func() { reads[sc] = s.read(ctx, sc, names, metric.Limit(cfg.interval)) })
	}
	wg.Wait()
	var rows, failed []unit
	var errs [2]error
	for sc, r := range reads {
		rows = append(rows, r.named...)
		failed = append(failed, r.failed...)
		errs[sc] = r.err
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return 0 // the settings changed; the poller was woken
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen {
		return 0
	}
	// A manager that could not be read keeps its units as last read,
	// under its warning: those still named, and the others failed.
	for _, u := range s.rows {
		if errs[u.ref.scope] != nil {
			switch {
			case slices.Contains(cfg.units, u.ref):
				rows = append(rows, u)
			case u.active == "failed":
				failed = append(failed, u)
			}
		}
	}
	// The units named, in the settings' order, then the others failed.
	order := func(r ref) int { return slices.Index(cfg.units, r) }
	slices.SortStableFunc(rows, func(a, b unit) int { return order(a.ref) - order(b.ref) })
	for _, u := range failed {
		if !slices.ContainsFunc(rows, func(r unit) bool { return r.ref.scope == u.ref.scope && r.id == u.id }) {
			rows = append(rows, u)
		}
	}
	for i, err := range errs {
		if err != nil && (s.errs[i] == nil || err.Error() != s.errs[i].Error()) {
			log.Printf("%s manager: %v", scope(i), err)
		}
	}
	// A restart's failure is news until the unit changes state after,
	// or is no longer listed.
	for key, at := range s.failedAt {
		i := slices.IndexFunc(rows, func(u unit) bool { return u.ref.key() == key })
		if i < 0 || rows[i].since.After(at) {
			delete(s.results, key)
			delete(s.failedAt, key)
		}
	}
	s.rows, s.errs, s.polled = rows, errs, true
	s.show()
	return cfg.interval
}

// read is what a poll read of one manager.
type read struct {
	named, failed []unit
	err           error
}

// read reads the units named, and those failed, from sc's manager,
// within limit.
func (s *services) read(ctx context.Context, sc scope, names []string, limit time.Duration) read {
	rctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	m, err := s.manager(rctx, sc)
	if err != nil {
		return read{err: err}
	}
	var r read
	if r.named, r.err = m.units(rctx, names); r.err == nil {
		ids := make([]string, len(r.named))
		for i, u := range r.named {
			ids[i] = u.id
		}
		r.failed, r.err = m.failed(rctx, ids)
	}
	if r.err == nil {
		return r
	}
	// A poll cut short, as the settings changed, says nothing of the
	// connection, which a restart may be waiting on.
	if ctx.Err() == nil {
		s.drop(sc, m)
	}
	// Half a read is none: the manager's units stay as last read.
	return read{err: r.err}
}

// manager is the connection to sc's manager, made if there is none.
// Only one is made at a time: those who need it meanwhile wait for it,
// each as long as its ctx lets it.
func (s *services) manager(ctx context.Context, sc scope) (manager, error) {
	s.mu.Lock()
	if m := s.managers[sc]; m != nil {
		if m.alive() {
			s.mu.Unlock()
			return m, nil
		}
		s.managers[sc] = nil // closed, as its bus went away
	}
	d := s.dialing[sc]
	if d == nil {
		d = &dial{done: make(chan struct{})}
		s.dialing[sc] = d
		go s.dial(sc, d)
	}
	s.mu.Unlock()
	select {
	case <-d.done:
		return d.m, d.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// dial is a connection being made.
type dial struct {
	done chan struct{} // closed when made, or failed
	m    manager
	err  error
}

// dial connects to sc's manager, which can take as long as the bus
// does: neither it nor the connection, which lives on after the poll or
// restart that wanted it, is bound to theirs.
func (s *services) dial(sc scope, d *dial) {
	d.m, d.err = connect(context.Background(), sc)
	s.mu.Lock()
	if d.err == nil {
		s.managers[sc] = d.m
	}
	s.dialing[sc] = nil
	s.mu.Unlock()
	close(d.done)
}

// drop closes m, sc's connection, which failed, so the next poll makes
// another.
func (s *services) drop(sc scope, m manager) {
	s.mu.Lock()
	if s.managers[sc] == m {
		s.managers[sc] = nil
	}
	s.mu.Unlock()
	m.close()
}

func (s *services) activate(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case id == hideAction:
		s.selected = ""
	case strings.HasPrefix(id, restartPrefix):
		key := strings.TrimPrefix(id, restartPrefix)
		i := slices.IndexFunc(s.rows, func(u unit) bool { return u.ref.key() == key })
		// Only a user's own service, listed now, and not being restarted.
		if i < 0 || !restartable(s.rows[i]) || s.busy[key] {
			return nil
		}
		s.busy[key] = true
		delete(s.results, key)
		go s.restart(s.rows[i].ref)
	case strings.HasPrefix(id, unitPrefix):
		s.selected = strings.TrimPrefix(id, unitPrefix)
	default:
		return nil
	}
	s.show()
	return nil
}

// restartable reports whether the panel restarts u: a user's own
// service, there to restart; system units need root, and other types
// are not restarted.
func restartable(u unit) bool {
	return u.ref.scope == user && unitType(u.ref.name) == "service" && u.load != "not-found" && u.load != "masked"
}

// restartTimeout bounds a restart, which waits for its job to end: a
// stop and a start, each by default allowed 90 s.
const restartTimeout = 4 * time.Minute

// restart restarts r, then shows how it went and polls again.
func (s *services) restart(r ref) {
	ctx, cancel := context.WithTimeout(context.Background(), restartTimeout)
	defer cancel()
	m, err := s.manager(ctx, r.scope)
	asked := time.Now()
	if err == nil {
		err = m.restart(ctx, r.name)
	}
	if errors.Is(err, errWaitLost) && s.restartedSince(r, asked) {
		err = nil
	}
	s.mu.Lock()
	delete(s.busy, r.key())
	if err != nil {
		log.Printf("restart %s: %v", r, err)
		s.results[r.key()] = brief(err)
		s.failedAt[r.key()] = time.Now()
	}
	s.show()
	s.mu.Unlock()
	s.repoll()
}

// brief is why a restart failed, in few words: the details have little
// room.
func brief(err error) string {
	var job jobError
	var e godbus.Error
	pe := (*godbus.Error)(nil)
	switch {
	case errors.Is(err, errWaitLost):
		return "no word came of its end"
	case errors.As(err, &job):
		switch job.result {
		case "canceled":
			return "its job was canceled"
		case "timeout":
			return "its job timed out"
		case "dependency":
			return "a dependency failed"
		case "skipped":
			return "its job was skipped"
		}
		return "its job " + job.result
	case errors.As(err, &pe):
		e = *pe
	case !errors.As(err, &e):
		return err.Error()
	}
	if len(e.Body) > 0 {
		if msg, ok := e.Body[0].(string); ok {
			return msg // systemd's own, such as "Unit x.service not found."
		}
	}
	return err.Error()
}

// restartedSince reports whether r, read again, is running and has
// changed state since t: a restart whose end was not heard of.
func (s *services) restartedSince(r ref, t time.Time) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m, err := s.manager(ctx, r.scope)
	if err != nil {
		return false
	}
	us, err := m.units(ctx, []string{r.name})
	if err != nil {
		s.drop(r.scope, m)
		return false
	}
	return len(us) == 1 && us[0].active == "active" && us[0].since.After(t)
}

// show sends the view. s.mu is held.
func (s *services) show() { s.views <- view(time.Now(), s.state()) }

// state is what the view shows. s.mu is held.
func (s *services) state() panelState {
	return panelState{
		rows:     s.rows,
		errs:     s.errs,
		named:    s.cfg.units,
		polled:   s.polled,
		selected: s.selected,
		busy:     s.busy,
		results:  s.results,
	}
}
