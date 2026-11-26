// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sd "github.com/coreos/go-systemd/v22/dbus"
	godbus "github.com/godbus/dbus/v5"
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
	// failed is the units that have failed, but for those of ids, which
	// show already as named.
	failed(ctx context.Context, ids []string) ([]unit, error)
	// restart restarts the unit named and waits for the job to end.
	restart(ctx context.Context, name string) error
	// alive reports whether the connection is still open.
	alive() bool
	close()
}

// handshake bounds making a connection: godbus's Auth and Hello, and
// go-systemd's AddMatch, wait on the bus for as long as it takes.
var handshake = 30 * time.Second

// errHandshake is a bus that did not finish a connection in time.
var errHandshake = errors.New("the bus did not answer")

// buses opens a private connection to each scope's bus; tests replace
// them. Neither starts a bus where there is none, as go-systemd's own
// does: that runs dbus-launch, and leaves its daemon behind.
var buses = [2]func(...godbus.ConnOption) (*godbus.Conn, error){
	system: godbus.SystemBusPrivate,
	user:   godbus.SessionBusPrivateNoAutoStartup,
}

// stuck says, of each scope, whether a connection is being made.
var stuck [2]atomic.Bool

// connect connects to the manager of s; tests replace it. The
// connection lives as long as ctx. Making it takes handshake at most:
// past that, it is given up on, and closed if it is made after all.
var connect = func(ctx context.Context, s scope) (manager, error) {
	// One given up on that has not returned yet is waited for: a dial
	// godbus cannot cancel, and another would only stall beside it.
	if !stuck[s].CompareAndSwap(false, true) {
		return nil, errHandshake
	}
	open, limit := buses[s], handshake
	var mu sync.Mutex
	var opened []*godbus.Conn
	late := false
	expired := make(chan struct{})
	timer := time.AfterFunc(limit, func() {
		mu.Lock()
		defer mu.Unlock()
		late = true
		for _, c := range opened {
			c.Close()
		}
		close(expired)
	})
	type made struct {
		c   *sd.Conn
		err error
	}
	res := make(chan made, 1)
	go func() {
		c, err := sd.NewConnection(func() (*godbus.Conn, error) {
			conn, err := open(godbus.WithContext(ctx))
			if err != nil {
				return nil, err
			}
			mu.Lock()
			opened = append(opened, conn)
			if late {
				conn.Close()
			}
			mu.Unlock()
			return conn, handshakeOn(conn)
		})
		res <- made{c, err}
		stuck[s].Store(false)
	}()
	var r made
	select {
	case r = <-res:
	case <-expired:
		go func() {
			if r := <-res; r.err == nil {
				r.c.Close()
			}
		}()
		return nil, errHandshake
	}
	if !timer.Stop() {
		if r.err == nil {
			r.c.Close()
		}
		return nil, errHandshake
	}
	if r.err != nil {
		return nil, r.err
	}
	m := &dbusManager{scope: s, conn: r.c, closed: make(chan struct{})}
	// go-systemd makes two connections, one for calls and one for
	// signals, a restart's end among them; either closing, as the bus
	// went away, ends the manager, so a restart waiting knows.
	for _, conn := range opened {
		go watch(conn, m.closed, m.close)
	}
	return m, nil
}

// watch calls end when conn closes, unless closed is first.
func watch(conn interface{ Context() context.Context }, closed <-chan struct{}, end func()) {
	select {
	case <-conn.Context().Done():
		end()
	case <-closed:
	}
}

// handshakeOn authenticates on c, as go-systemd does, and closes it if
// that fails.
func handshakeOn(c *godbus.Conn) error {
	err := c.Auth([]godbus.Auth{godbus.AuthExternal(strconv.Itoa(os.Getuid()))})
	if err == nil {
		err = c.Hello()
	}
	if err != nil {
		c.Close()
	}
	return err
}

// dbusManager is a manager reached over D-Bus.
type dbusManager struct {
	scope  scope
	conn   *sd.Conn
	once   sync.Once
	closed chan struct{} // closed with conn
}

func (m *dbusManager) units(ctx context.Context, names []string) ([]unit, error) {
	st, err := byName(ctx, names, m.conn.ListUnitsByNamesContext)
	if err != nil {
		return nil, err
	}
	us, err := m.withSince(ctx, st, len(st))
	if err != nil {
		return nil, err
	}
	// Each under the name asked for: an alias, such as dbus.service,
	// comes back as the unit it names.
	for i := range us {
		us[i].ref.name = names[i]
	}
	return us, nil
}

// byName is the status of each unit named, in that order, as list
// tells. list leaves out a name its manager takes as invalid, and fails
// whole for some, so then each is asked of alone, and one the manager
// will not tell of is not found.
func byName(ctx context.Context, names []string, list func(context.Context, []string) ([]sd.UnitStatus, error)) ([]sd.UnitStatus, error) {
	if len(names) == 0 {
		return nil, nil
	}
	st, err := list(ctx, names)
	if err == nil && len(st) == len(names) {
		return st, nil
	}
	if err != nil && !refused(err) {
		return nil, err
	}
	st = st[:0]
	for _, n := range names {
		one, err := list(ctx, []string{n})
		switch {
		case err != nil && !refused(err):
			return nil, err
		case err != nil || len(one) != 1:
			one = []sd.UnitStatus{{Name: n, LoadState: "not-found", ActiveState: "inactive", SubState: "dead"}}
		}
		st = append(st, one[0])
	}
	return st, nil
}

// refused reports whether err is the manager's refusal of a name, or
// of so many at once, not a failure to reach it: the bus's own errors,
// such as ServiceUnknown or NoReply, say nothing of the names.
func refused(err error) bool {
	var e godbus.Error
	if pe := (*godbus.Error)(nil); errors.As(err, &pe) {
		e = *pe
	} else if !errors.As(err, &e) {
		return false
	}
	switch e.Name {
	case "org.freedesktop.DBus.Error.InvalidArgs", "org.freedesktop.DBus.Error.LimitsExceeded":
		return true
	}
	return strings.HasPrefix(e.Name, "org.freedesktop.systemd1.")
}

func (m *dbusManager) failed(ctx context.Context, ids []string) ([]unit, error) {
	st, err := m.conn.ListUnitsFilteredContext(ctx, []string{"failed"})
	if err != nil {
		return nil, err
	}
	st = slices.DeleteFunc(st, func(s sd.UnitStatus) bool { return slices.Contains(ids, s.Name) })
	// Since for as many as the list holds: past them, a unit costs a
	// call, and shows not.
	return m.withSince(ctx, st, maxItems)
}

// withSince is the statuses st with when each of the first n last
// changed state, where its manager says. A unit it will not say of has none; a failure
// to ask, as the time ran out or the connection closed, fails it all.
func (m *dbusManager) withSince(ctx context.Context, st []sd.UnitStatus, n int) ([]unit, error) {
	out := make([]unit, 0, len(st))
	for i, s := range st {
		u := unit{
			ref:         ref{scope: m.scope, name: s.Name},
			id:          s.Name,
			description: s.Description,
			load:        s.LoadState,
			active:      s.ActiveState,
			sub:         s.SubState,
		}
		if i < n && s.LoadState != "not-found" {
			p, err := m.conn.GetUnitPropertyContext(ctx, s.Name, "StateChangeTimestamp")
			switch {
			case err == nil:
				if us, ok := p.Value.Value().(uint64); ok && us > 0 {
					u.since = time.UnixMicro(int64(us))
				}
			case ctx.Err() != nil || !answer(err):
				return nil, err
			}
		}
		out = append(out, u)
	}
	return out, nil
}

// answer reports whether err is an error the bus delivered as a reply,
// not a failure to deliver the call.
func answer(err error) bool {
	var e godbus.Error
	if pe := (*godbus.Error)(nil); errors.As(err, &pe) {
		e = *pe
	} else if !errors.As(err, &e) {
		return false
	}
	return !slices.Contains(undelivered, e.Name)
}

// undelivered are the bus's errors for a call it could not deliver, or
// whose reply it could not: they say nothing of what the manager did.
var undelivered = []string{
	"org.freedesktop.DBus.Error.NoReply",
	"org.freedesktop.DBus.Error.ServiceUnknown",
	"org.freedesktop.DBus.Error.NameHasNoOwner",
	"org.freedesktop.DBus.Error.Disconnected",
	"org.freedesktop.DBus.Error.Timeout",
	"org.freedesktop.DBus.Error.TimedOut",
	"org.freedesktop.DBus.Error.NoServer",
	"org.freedesktop.DBus.Error.NoNetwork",
	"org.freedesktop.DBus.Error.IOError",
	"org.freedesktop.DBus.Error.NoMemory",
}

// jobError is a restart whose job did not end done, but result.
type jobError struct{ result string }

func (e jobError) Error() string { return "the restart's job ended " + e.result }

// errWaitLost is a restart asked for, whose end was not heard of: the
// connection closed, or the time ran out, first.
var errWaitLost = errors.New("the restart's end was not heard of")

func (m *dbusManager) restart(ctx context.Context, name string) error {
	return awaitJob(ctx, m.closed, func(ctx context.Context, done chan<- string) error {
		_, err := m.conn.RestartUnitContext(ctx, name, "replace", done)
		return err
	})
}

// awaitJob starts a job and waits for its end, as told on done, while
// the connection is not closed. A job whose start had no reply, or
// whose end was not heard of, may have run: errWaitLost says so.
func awaitJob(ctx context.Context, closed <-chan struct{}, start func(context.Context, chan<- string) error) error {
	done := make(chan string, 1)
	if err := start(ctx, done); err != nil {
		if !answer(err) {
			return fmt.Errorf("%w: %v", errWaitLost, err)
		}
		return err
	}
	select {
	case result := <-done:
		if result != "done" {
			return jobError{result}
		}
		return nil
	case <-closed:
		return errWaitLost
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", errWaitLost, ctx.Err())
	}
}

func (m *dbusManager) alive() bool {
	select {
	case <-m.closed:
		return false
	default:
		return true
	}
}

func (m *dbusManager) close() {
	m.once.Do(func() {
		close(m.closed)
		m.conn.Close()
	})
}
