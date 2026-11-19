// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"context"
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
	Dir     string `json:"dir"`
	Display string `json:"display"`
	XPID    int    `json:"x_pid"`
	HostPID int    `json:"host_pid"`
}

func (s session) configPath() string { return filepath.Join(s.Dir, "config.yaml") }
func (s session) socket() string     { return filepath.Join(s.Dir, "debug.sock") }
func (s session) hostLog() string    { return filepath.Join(s.Dir, "host.log") }

// sessionDir is where the session called name keeps its files.
func sessionDir(name string) string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "tessera-uidrive", name)
}

// loadSession reads the session called name, which must be running.
func loadSession(name string) (session, error) {
	dir := sessionDir(name)
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
	if !alive(s.HostPID) {
		return s, fmt.Errorf("session %q: the host is not running; see %s, then uidrive stop", name, s.hostLog())
	}
	return s, nil
}

func (s session) save() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, "state.json"), b, 0o600)
}

func alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil && !zombie(pid)
}

// zombie reports whether pid has exited but not been reaped.
func zombie(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// The state follows the command name, which is in parentheses.
	i := strings.LastIndexByte(string(b), ')')
	return i >= 0 && i+2 < len(b) && b[i+2] == 'Z'
}

// startOptions are start's flags.
type startOptions struct {
	host, plugins, config string
	size                  image.Point
}

// startSession starts Xvfb and the host on it, with a copy of the
// config, and waits for the window.
func startSession(name string, o startOptions) (session, error) {
	dir := sessionDir(name)
	if s, err := loadSession(name); err == nil {
		return session{}, fmt.Errorf("session %q is running (display %s); uidrive stop first", name, s.Display)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return session{}, err
	}
	s := session{Dir: dir}
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
	if s.Display, s.XPID, err = startX(dir, o.size); err != nil {
		return session{}, err
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
	cmd.Env = append(withoutEnv(os.Environ(), "WAYLAND_DISPLAY", "DISPLAY"), "DISPLAY="+s.Display)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return s, err
	}
	s.HostPID = cmd.Process.Pid
	cmd.Process.Release()
	if err := s.save(); err != nil {
		return s, err
	}
	d, err := openDisplay(s.Display, 15*time.Second)
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
// the display's name and the server's process id.
func startX(dir string, size image.Point) (string, int, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", 0, err
	}
	defer r.Close()
	logFile, err := os.Create(filepath.Join(dir, "xvfb.log"))
	if err != nil {
		w.Close()
		return "", 0, err
	}
	defer logFile.Close()
	cmd := exec.Command("Xvfb", "-displayfd", "3", "-nolisten", "tcp",
		"-screen", "0", fmt.Sprintf("%dx%dx24", size.X, size.Y))
	cmd.ExtraFiles = []*os.File{w}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	w.Close()
	if err != nil {
		return "", 0, fmt.Errorf("starting Xvfb: %w", err)
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(r).ReadString('\n')
		line <- strings.TrimSpace(l)
	}()
	select {
	case l := <-line:
		if _, err := strconv.Atoi(l); err != nil {
			syscall.Kill(pid, syscall.SIGTERM)
			return "", 0, fmt.Errorf("the X server gave no display; see %s", logFile.Name())
		}
		return ":" + l, pid, nil
	case <-time.After(10 * time.Second):
		syscall.Kill(pid, syscall.SIGTERM)
		return "", 0, fmt.Errorf("the X server did not start; see %s", logFile.Name())
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
func (s session) stop() error {
	var errs []error
	for _, pid := range []int{s.HostPID, s.XPID} {
		if !alive(pid) {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			errs = append(errs, err)
			continue
		}
		deadline := time.Now().Add(10 * time.Second)
		for alive(pid) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if alive(pid) {
			errs = append(errs, syscall.Kill(pid, syscall.SIGKILL))
		}
	}
	errs = append(errs, os.Remove(filepath.Join(s.Dir, "state.json")))
	return errors.Join(errs...)
}

// tree asks the host for its semantic tree, retrying for up to wait
// while the host starts.
func (s session) tree(wait time.Duration) (uidebug.Tree, error) {
	client := http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", s.socket())
		}},
	}
	deadline := time.Now().Add(wait)
	for {
		t, err := func() (uidebug.Tree, error) {
			resp, err := client.Get("http://host/tree")
			if err != nil {
				return uidebug.Tree{}, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return uidebug.Tree{}, fmt.Errorf("semantic tree: %s", resp.Status)
			}
			var t uidebug.Tree
			return t, json.NewDecoder(resp.Body).Decode(&t)
		}()
		if err == nil || time.Now().After(deadline) || !alive(s.HostPID) {
			if err != nil {
				err = fmt.Errorf("%w; see %s", err, s.hostLog())
			}
			return t, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
