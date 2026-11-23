// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joncbenderkh/tessera/internal/uidebug"
)

// session is a host running on a private X server, as start leaves it
// for the other commands. It is kept as state.json in its directory.
type session struct {
	Dir     string  `json:"dir"`
	Display string  `json:"display"`
	X       process `json:"x"`
	Host    process `json:"host"`
}

// process is a process uidrive started: its id, and when it started, so
// that an id the system has since given another process is not taken
// for it.
type process struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start"` // in clock ticks after boot, from /proc/PID/stat
}

// started is the process pid, if it is running.
func started(pid int) (process, error) {
	st, err := readStat(pid)
	if err != nil {
		return process{}, err
	}
	return process{pid, st.start}, nil
}

func (s session) configPath() string { return filepath.Join(s.Dir, "config.yaml") }
func (s session) socket() string     { return filepath.Join(s.Dir, "debug.sock") }
func (s session) hostLog() string    { return filepath.Join(s.Dir, "host.log") }
func (s session) xauth() string      { return filepath.Join(s.Dir, "Xauthority") }

// writeXauth writes an Xauthority file at path with a new random
// MIT-MAGIC-COOKIE-1 for any display, which Xvfb's -auth makes the only
// way in: a display without one takes any local client, another user's
// too.
func writeXauth(path string) error {
	cookie := make([]byte, 16)
	if _, err := rand.Read(cookie); err != nil {
		return err
	}
	var b bytes.Buffer
	field := func(data []byte) {
		binary.Write(&b, binary.BigEndian, uint16(len(data)))
		b.Write(data)
	}
	binary.Write(&b, binary.BigEndian, uint16(0xffff)) // FamilyWild: any address
	field(nil)                                         // the address
	field(nil)                                         // the display number: any
	field([]byte("MIT-MAGIC-COOKIE-1"))
	field(cookie)
	return os.WriteFile(path, b.Bytes(), 0o600)
}

// lockSession holds an exclusive lock on the session whose directory is
// dir until the returned function is called, so that two starts or
// stops of one session do not interleave. The locks live apart from
// the sessions, in .locks beside them.
func lockSession(dir string) (func(), error) {
	locks := filepath.Join(filepath.Dir(dir), ".locks")
	if err := os.Mkdir(locks, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(locks, filepath.Base(dir)), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// sessionDir is where the session called name keeps its files, in a
// directory of the user's own, which it creates.
func sessionDir(name string) (string, error) {
	base := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "tessera-uidrive")
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		// The temporary directory is shared, so another user could
		// have made this one: it must be the user's alone.
		base = filepath.Join(os.TempDir(), fmt.Sprintf("tessera-uidrive-%d", os.Getuid()))
	}
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("session name %q: want a plain name, not starting with a dot", name)
	}
	if err := os.Mkdir(base, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	fi, err := os.Lstat(base)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s must be a directory of yours that no one else can open (mode 0700)", base)
	}
	return filepath.Join(base, name), nil
}

// loadSession reads the session called name, which must be running.
func loadSession(name string) (session, error) {
	dir, err := sessionDir(name)
	if err != nil {
		return session{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return session{}, fmt.Errorf("no session %q: start one with uidrive start", name)
	}
	if err != nil {
		return session{}, err
	}
	var s session
	if err := json.Unmarshal(b, &s); err != nil {
		return session{}, err
	}
	if !s.Host.alive() {
		return s, fmt.Errorf("session %q: the host is not running; see %s, then uidrive stop", name, s.hostLog())
	}
	return s, nil
}

func (s session) save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Written whole or not at all: a state file cut short would leave
	// running processes no command can find.
	f, err := os.CreateTemp(s.Dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // after the rename, there is none
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(s.Dir, "state.json"))
}

// kill signals a process; tests replace it.
var kill = syscall.Kill

// alive reports whether p is still running: its id is in use, by the
// process that started then, which has not exited.
func (p process) alive() bool {
	if p.PID <= 0 {
		return false
	}
	st, err := readStat(p.PID)
	return err == nil && st.start == p.Start && st.state != 'Z' && st.state != 'X'
}

type stat struct {
	state byte
	start uint64
}

// readStat reads the state and start time of process pid from
// /proc/PID/stat.
func readStat(pid int) (stat, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return stat{}, err
	}
	return parseStat(string(b))
}

// parseStat reads a /proc/PID/stat line. The fields after the command
// name, which is in parentheses and may hold anything, are the third
// onward: the state, then, as the 22nd, the start time.
func parseStat(line string) (stat, error) {
	i := strings.LastIndexByte(line, ')')
	if i < 0 {
		return stat{}, errors.New("malformed /proc stat")
	}
	f := strings.Fields(line[i+1:])
	if len(f) < 20 || len(f[0]) != 1 {
		return stat{}, errors.New("malformed /proc stat")
	}
	start, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return stat{}, err
	}
	return stat{state: f[0][0], start: start}, nil
}

// startOptions are start's flags.
type startOptions struct {
	host, plugins, config string
	size                  image.Point
}

// startSession starts Xvfb and the host on it, with a copy of the
// config, and waits for the window.
func startSession(name string, o startOptions) (s session, err error) {
	dir, err := sessionDir(name)
	if err != nil {
		return session{}, err
	}
	unlock, err := lockSession(dir)
	if err != nil {
		return session{}, err
	}
	defer unlock()
	// Undone under the lock, so another start cannot slip in between.
	defer func() {
		if err != nil && s.Dir != "" {
			if serr := s.stop(); serr != nil {
				// What survives may be in no state file: name it.
				err = fmt.Errorf("%w; stopping what it started: %v (host pid %d, Xvfb pid %d)", err, serr, s.Host.PID, s.X.PID)
			}
		}
	}()
	switch old, err := loadSession(name); {
	case err == nil:
		return session{}, fmt.Errorf("session %q is running (display %s); uidrive stop first", name, old.Display)
	case old.Dir != "":
		// Its host has stopped; its X server may not have.
		if err := old.stop(); err != nil {
			return session{}, fmt.Errorf("stopping the last session %q: %w", name, err)
		}
	default:
		// No state to go by: start only if there is none at all, since
		// a state file that cannot be read may name running processes.
		if _, serr := os.Stat(filepath.Join(dir, "state.json")); !errors.Is(serr, os.ErrNotExist) {
			return session{}, fmt.Errorf("session %q: %w; stop its processes and remove %s", name, err, dir)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return session{}, err
	}
	s = session{Dir: dir}
	// The host must be able to bind it.
	if err := uidebug.CheckPath(s.socket()); err != nil {
		return session{}, fmt.Errorf("%w; use a shorter --session or TMPDIR", err)
	}
	config := []byte(defaultConfig)
	if o.config != "" {
		var err error
		if config, err = os.ReadFile(o.config); err != nil {
			return session{}, err
		}
	}
	if err := os.WriteFile(s.configPath(), config, 0o600); err != nil {
		return session{}, err
	}
	host, err := exec.LookPath(o.host)
	if err != nil {
		return session{}, fmt.Errorf("host: %w (build it with go run ./cmd/build)", err)
	}
	if err := writeXauth(s.xauth()); err != nil {
		return session{}, err
	}
	if s.Display, s.X, err = startX(dir, s.xauth(), o.size); err != nil {
		return s, err // the deferred stop ends an X server that did start
	}
	if err := s.save(); err != nil {
		return s, err
	}
	logFile, err := os.Create(s.hostLog())
	if err != nil {
		return s, err
	}
	defer logFile.Close()
	cmd := exec.Command(host, "--config", s.configPath(), "--plugins", o.plugins, "--debug-socket", s.socket())
	cmd.Env = append(withoutEnv(os.Environ(), "WAYLAND_DISPLAY", "DISPLAY", "XAUTHORITY"), "DISPLAY="+s.Display, "XAUTHORITY="+s.xauth())
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return s, err
	}
	s.Host, err = started(cmd.Process.Pid)
	cmd.Process.Release()
	if err != nil {
		return s, err
	}
	if err := s.save(); err != nil {
		return s, err
	}
	d, err := s.openDisplay(15 * time.Second)
	if err != nil {
		return s, fmt.Errorf("%w; see %s", err, s.hostLog())
	}
	defer d.Close()
	if err := d.fill(); err != nil {
		return s, err
	}
	if _, err := s.tree(15 * time.Second); err != nil {
		return s, err
	}
	return s, nil
}

const defaultConfig = `version: 1
theme: dark
grid:
  columns: 4
  row_height: 180
panels: []
`

// startX starts Xvfb on a free display, size pixels large, and returns
// the display's name and the server's process. On an error after the
// server started, the process is returned too, for the caller to stop.
func startX(dir, auth string, size image.Point) (string, process, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", process{}, err
	}
	defer r.Close()
	logFile, err := os.Create(filepath.Join(dir, "xvfb.log"))
	if err != nil {
		w.Close()
		return "", process{}, err
	}
	defer logFile.Close()
	cmd := exec.Command("Xvfb", "-displayfd", "3", "-nolisten", "tcp", "-auth", auth,
		"-screen", "0", fmt.Sprintf("%dx%dx24", size.X, size.Y))
	cmd.ExtraFiles = []*os.File{w}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	w.Close()
	if err != nil {
		return "", process{}, fmt.Errorf("starting Xvfb: %w", err)
	}
	x, err := started(cmd.Process.Pid)
	cmd.Process.Release()
	if err != nil {
		return "", process{}, err
	}
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(r).ReadString('\n')
		line <- strings.TrimSpace(l)
	}()
	select {
	case l := <-line:
		if _, err := strconv.Atoi(l); err != nil {
			return "", x, fmt.Errorf("the X server gave no display; see %s", logFile.Name())
		}
		return ":" + l, x, nil
	case <-time.After(10 * time.Second):
		return "", x, fmt.Errorf("the X server did not start; see %s", logFile.Name())
	}
}

func withoutEnv(env []string, names ...string) []string {
	var out []string
outer:
	for _, kv := range env {
		for _, n := range names {
			if strings.HasPrefix(kv, n+"=") {
				continue outer
			}
		}
		out = append(out, kv)
	}
	return out
}

// stop ends the host, giving it time to close its plugins, and then
// the X server.
//
// The caller holds the session's lock.
func (s session) stop() error {
	var errs []error
	// awaitExit waits up to d for p to exit.
	awaitExit := func(p process, d time.Duration) {
		deadline := time.Now().Add(d)
		for p.alive() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	for _, p := range []process{s.Host, s.X} {
		if !p.alive() {
			continue
		}
		if err := kill(p.PID, syscall.SIGTERM); err != nil {
			if !errors.Is(err, syscall.ESRCH) { // gone on its own
				errs = append(errs, err)
			}
			continue
		}
		awaitExit(p, 10*time.Second)
		if p.alive() {
			if err := kill(p.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				errs = append(errs, err)
			}
			awaitExit(p, 2*time.Second) // a killed process takes a moment to go
		}
	}
	if s.Host.alive() || s.X.alive() {
		// Kept, so that stop can try again on what is still running.
		errs = append(errs, errors.New("a process outlived stop; the session's state is kept"))
		return errors.Join(errs...)
	}
	// A start that failed early wrote none.
	if err := os.Remove(filepath.Join(s.Dir, "state.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// openDisplay connects to the session's display with its cookie, and
// finds the host's window there, waiting up to wait for it.
func (s session) openDisplay(wait time.Duration) (*display, error) {
	// xgb reads the cookie's file from the environment.
	if err := os.Setenv("XAUTHORITY", s.xauth()); err != nil {
		return nil, err
	}
	return openDisplay(s.Display, wait)
}

// tree asks the host for its semantic tree, retrying for up to wait
// while the host starts.
func (s session) tree(wait time.Duration) (uidebug.Tree, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait+10*time.Second)
	defer cancel()
	return s.treeBy(ctx, wait)
}

// treeBy is tree, giving up when ctx ends.
func (s session) treeBy(ctx context.Context, wait time.Duration) (uidebug.Tree, error) {
	client := http.Client{
		// One request a connection: a long wait polls many times.
		Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", s.socket())
		}},
	}
	deadline := time.Now().Add(wait)
	for {
		t, err := func() (uidebug.Tree, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://host/tree", nil)
			if err != nil {
				return uidebug.Tree{}, err
			}
			resp, err := client.Do(req)
			if err != nil {
				return uidebug.Tree{}, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return uidebug.Tree{}, fmt.Errorf("semantic tree: %s", resp.Status)
			}
			var t uidebug.Tree
			if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
				return uidebug.Tree{}, err
			}
			return t, nil
		}()
		if err == nil || ctx.Err() != nil || time.Now().After(deadline) || !s.Host.alive() {
			if err != nil {
				err = fmt.Errorf("%w; see %s", err, s.hostLog())
			}
			return t, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
