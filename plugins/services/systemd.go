// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	sd "github.com/coreos/go-systemd/v22/dbus"
)

// scope is which systemd manager a unit belongs to: the system's or
// the user's own.
type scope int

const (
	system scope = iota
	user
)

func (s scope) String() string {
	if s == user {
		return "user"
	}
	return "system"
}

// unit is a unit's status as its manager reports it.
type unit struct {
	ref         ref    // as the settings name it, perhaps by an alias
	id          string // its manager's name for it, the alias resolved
	description string
	load        string // loaded, not-found, masked…
	active      string // active, inactive, failed, activating…
	sub         string // running, exited, dead…
	since       time.Time
}

// manager talks to one systemd manager.
type manager interface {
	// units is the status of the units named, in that order.
	units(ctx context.Context, names []string) ([]unit, error)
	// failed is the units that have failed.
	failed(ctx context.Context) ([]unit, error)
	// restart restarts the unit named and waits for the job to end.
	restart(ctx context.Context, name string) error
	close()
}

// connect connects to the manager of s; tests replace it.
var connect = func(ctx context.Context, s scope) (manager, error) {
	var c *sd.Conn
	var err error
	if s == user {
		c, err = sd.NewUserConnectionContext(ctx)
	} else {
		c, err = sd.NewSystemConnectionContext(ctx)
	}
	if err != nil {
		return nil, err
	}
	return &dbusManager{scope: s, conn: c}, nil
}

// dbusManager is a manager reached over D-Bus.
type dbusManager struct {
	scope scope
	conn  *sd.Conn
}

func (m *dbusManager) units(ctx context.Context, names []string) ([]unit, error) {
	if len(names) == 0 {
		return nil, nil
	}
	st, err := m.conn.ListUnitsByNamesContext(ctx, names)
	if err != nil {
		return nil, err
	}
	us := m.withSince(ctx, st)
	// In the order asked, each under the name asked for: an alias,
	// such as dbus.service, comes back as the unit it names.
	for i := range us {
		if i < len(names) {
			us[i].ref.name = names[i]
		}
	}
	return us, nil
}

func (m *dbusManager) failed(ctx context.Context) ([]unit, error) {
	st, err := m.conn.ListUnitsFilteredContext(ctx, []string{"failed"})
	if err != nil {
		return nil, err
	}
	return m.withSince(ctx, st), nil
}

// withSince is the statuses st with when each unit last changed state,
// where its manager says.
func (m *dbusManager) withSince(ctx context.Context, st []sd.UnitStatus) []unit {
	out := make([]unit, 0, len(st))
	for _, s := range st {
		u := unit{
			ref:         ref{scope: m.scope, name: s.Name},
			id:          s.Name,
			description: s.Description,
			load:        s.LoadState,
			active:      s.ActiveState,
			sub:         s.SubState,
		}
		if s.LoadState != "not-found" {
			if p, err := m.conn.GetUnitPropertyContext(ctx, s.Name, "StateChangeTimestamp"); err == nil {
				if us, ok := p.Value.Value().(uint64); ok && us > 0 {
					u.since = time.UnixMicro(int64(us))
				}
			}
		}
		out = append(out, u)
	}
	return out
}

// errJobFailed is a restart whose job did not end done.
var errJobFailed = errors.New("the restart did not complete")

func (m *dbusManager) restart(ctx context.Context, name string) error {
	done := make(chan string, 1)
	if _, err := m.conn.RestartUnitContext(ctx, name, "replace", done); err != nil {
		return err
	}
	select {
	case result := <-done:
		if result != "done" {
			return fmt.Errorf("%w: %s", errJobFailed, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *dbusManager) close() { m.conn.Close() }
