// SPDX-License-Identifier: GPL-3.0-or-later

// Command services is the services panel: the state of the systemd
// units the settings name, of the system's manager or the user's, and
// of any other that has failed. Activating one shows its details, and,
// for a user's own unit, a Restart; system units are read-only, as
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
      "description": "The systemd units to show, such as sshd.service, or user:syncthing.service for one of your own; .service is added to a name without a type. Any other unit that has failed shows too.",
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

// String is the ref as the settings write it, and its key in the view.
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
	if !strings.Contains(name, ".") {
		name += ".service"
	}
	if !validName(name) {
		return ref{}, fmt.Errorf("setting \"units\": want unit names, such as sshd.service or user:syncthing.service, not %q", s)
	}
	r.name = name
	return r, nil
}

// validName reports whether name is a systemd unit name: letters,
// digits, and :-_.\@, a type after a dot, and no longer than 255.
func validName(name string) bool {
	if name == "" || len(name) > 255 || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, c := range name {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(":-_.\\@", c)
		if !ok {
			return false
		}
	}
	return true
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

// Action IDs: a unit's row is its ref; these name the details' items.
const (
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
	rows     []unit
	errs     [2]error // why each manager could not be read, or nil
	polled   bool
	selected string            // the ref of the unit whose details show
	busy     map[string]bool   // units being restarted
	results  map[string]string // why a unit's last restart failed
}

func main() {
	views := make(chan sdk.View)
	s := &services{views: views, busy: map[string]bool{}, results: map[string]string{}}
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
	ctx, cancel := context.WithTimeout(ctx, metric.Limit(cfg.interval))
	defer cancel()

	var rows []unit
	var errs [2]error
	var failed []unit
	for _, sc := range []scope{system, user} {
		var names []string
		for _, r := range cfg.units {
			if r.scope == sc {
				names = append(names, r.name)
			}
		}
		m, err := s.manager(ctx, sc)
		if err == nil {
			var named, bad []unit
			if named, err = m.units(ctx, names); err == nil {
				bad, err = m.failed(ctx)
			}
			if err == nil {
				rows = append(rows, named...)
				failed = append(failed, bad...)
			} else {
				s.drop(sc, m)
			}
		}
		if err != nil {
			errs[sc] = err
		}
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
	// under its warning.
	for _, u := range s.rows {
		if errs[u.ref.scope] != nil {
			if slices.Contains(cfg.units, u.ref) {
				rows = append(rows, u)
			} else {
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
	s.rows, s.errs, s.polled = rows, errs, true
	s.show()
	return cfg.interval
}

// manager is the connection to sc's manager, made if there is none.
func (s *services) manager(ctx context.Context, sc scope) (manager, error) {
	s.mu.Lock()
	m := s.managers[sc]
	s.mu.Unlock()
	if m != nil {
		return m, nil
	}
	// The connection lives on after this poll or restart, and closes
	// when the context it was made with ends: not when ctx does.
	m, err := connect(context.WithoutCancel(ctx), sc)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.managers[sc] != nil {
		m.close()
		return s.managers[sc], nil
	}
	s.managers[sc] = m
	return m, nil
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
		i := slices.IndexFunc(s.rows, func(u unit) bool { return u.ref.String() == key })
		// Only a user's own unit, listed now, and not being restarted.
		if i < 0 || s.rows[i].ref.scope != user || s.busy[key] {
			return nil
		}
		s.busy[key] = true
		delete(s.results, key)
		go s.restart(s.rows[i].ref)
	default:
		s.selected = id
	}
	s.show()
	return nil
}

// restartTimeout bounds a restart, which waits for its job to end.
const restartTimeout = 90 * time.Second

// restart restarts r, then shows how it went and polls again.
func (s *services) restart(r ref) {
	ctx, cancel := context.WithTimeout(context.Background(), restartTimeout)
	defer cancel()
	m, err := s.manager(ctx, r.scope)
	if err == nil {
		err = m.restart(ctx, r.name)
	}
	s.mu.Lock()
	delete(s.busy, r.String())
	if err != nil {
		log.Printf("restart %s: %v", r, err)
		s.results[r.String()] = err.Error()
	}
	s.show()
	s.mu.Unlock()
	s.repoll()
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
