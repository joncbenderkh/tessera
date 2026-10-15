// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin/runner"
)

// procRunner starts a plugin process for go-plugin, in place of its
// built-in runner, so the host controls how the process starts and
// can kill it, with its process group, at any time: during the
// handshake, which go-plugin does not let a context cancel, and after
// the plugin exits, to end children it left in its group.
type procRunner struct {
	path   string
	tmpDir string // go-plugin's socket directory, which Close removes
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser

	mu       sync.Mutex
	proc     *os.Process
	canceled bool
	exited   bool          // reaped: its PID, and so its group ID, may be reused
	reaping  bool          // Wait is reaping it without watch
	exitSeen bool          // watch saw it exit, unreaped
	reaped   chan struct{} // closed once Wait has reaped it
	watched  chan struct{} // closed by watch; Wait reaps only after it
}

var _ runner.Runner = (*procRunner)(nil)

// runnerFunc is go-plugin's ClientConfig.RunnerFunc for r. spec carries
// the environment go-plugin prepared (cookie, certificate, socket dir).
func (r *procRunner) runnerFunc(_ hclog.Logger, spec *exec.Cmd, tmpDir string) (runner.Runner, error) {
	r.tmpDir = tmpDir
	r.cmd = exec.Command(r.path)
	r.cmd.Env = spec.Env
	// No stdin: go-plugin would pass the host's, and a plugin in its own
	// process group that read a terminal would be stopped by SIGTTIN.
	ownProcessGroup(r.cmd)
	stdout, err := r.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	r.stdout = &lineLimiter{ReadCloser: stdout}
	if r.stderr, err = r.cmd.StderrPipe(); err != nil {
		return nil, err
	}
	return r, nil
}

// Start starts the plugin. It does not hold mu while the process
// starts, so kill can mark the launch canceled meanwhile; the plugin is
// then killed as soon as it exists.
func (r *procRunner) Start(context.Context) error {
	r.mu.Lock()
	canceled := r.canceled
	r.mu.Unlock()
	if canceled {
		return errors.New("launch canceled")
	}
	r.reaped = make(chan struct{})
	if err := startOnOwnThread(r.cmd, r.reaped); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.proc = r.cmd.Process
	if r.canceled {
		killGroup(r.proc)
	}
	if canAwaitExit {
		r.watched = make(chan struct{})
		go r.watch()
	}
	return nil
}

// watch kills the plugin's process group as soon as the plugin exits,
// ending children it left behind. go-plugin calls Wait only once the
// plugin's stderr closes, so a child holding it open would otherwise
// keep an exited plugin looking alive. The plugin is not reaped until
// watch is done, so its group ID cannot have been reused.
func (r *procRunner) watch() {
	defer close(r.watched)
	if awaitExitHook(r.proc) {
		r.mu.Lock()
		killGroup(r.proc)
		r.exitSeen = true
		r.mu.Unlock()
	}
}

// kill kills the plugin's process group and stops a pending Start.
// Once Wait may have reaped the plugin it kills only the plugin itself,
// which os.Process guards against PID reuse, and once Wait has reaped
// it nothing.
func (r *procRunner) kill() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.canceled = true
	switch {
	case r.proc == nil || r.exited:
	case r.reaping:
		_ = r.proc.Kill()
	default:
		killGroup(r.proc)
	}
}

// Wait reaps the plugin. When watch saw it exit (Linux, macOS, the
// BSDs), its group has been killed, so it is marked exited before the
// reap and kill never reaches a released group ID. Otherwise (Windows;
// other Unix systems, where children it left in its group are killed
// only by a forced Close; or watch could not watch) Wait cannot know the
// plugin has exited before reaping it, so from the start of the reap
// kill signals only the plugin.
func (r *procRunner) Wait(context.Context) error {
	defer close(r.reaped)
	if r.watched != nil {
		<-r.watched
	}
	r.mu.Lock()
	if r.exitSeen {
		r.exited = true
		r.mu.Unlock()
		return r.cmd.Wait()
	}
	r.reaping = true
	r.mu.Unlock()
	err := r.cmd.Wait()
	r.mu.Lock()
	r.exited = true
	r.mu.Unlock()
	return err
}

func (r *procRunner) Kill(context.Context) error {
	r.kill()
	return nil
}

func (r *procRunner) Stdout() io.ReadCloser { return r.stdout }

func (r *procRunner) Stderr() io.ReadCloser { return r.stderr }

func (r *procRunner) Name() string { return r.path }

func (r *procRunner) ID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc == nil {
		return ""
	}
	return strconv.Itoa(r.proc.Pid)
}

func (r *procRunner) Diagnose(context.Context) string { return "" }

func (r *procRunner) PluginToHost(network, addr string) (string, string, error) {
	return network, addr, nil
}

func (r *procRunner) HostToPlugin(network, addr string) (string, string, error) {
	return network, addr, nil
}

// maxStdoutLine caps a stdout line below bufio.Scanner's default
// limit. go-plugin scans stdout for the handshake line and discards the
// rest, but stops reading at a line its scanner cannot hold, after
// which a plugin that keeps writing there blocks on the full pipe.
const maxStdoutLine = 32 << 10

// lineLimiter breaks lines longer than maxStdoutLine with a newline.
type lineLimiter struct {
	io.ReadCloser
	n int // bytes since the last newline
}

func (l *lineLimiter) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if l.n == maxStdoutLine {
		p[0] = '\n'
		l.n = 0
		return 1, nil
	}
	p = p[:min(len(p), maxStdoutLine-l.n)]
	k, err := l.ReadCloser.Read(p)
	if i := bytes.LastIndexByte(p[:k], '\n'); i >= 0 {
		l.n = k - i - 1
	} else {
		l.n += k
	}
	return k, err
}

// awaitExitHook is awaitExit, replaceable by tests.
var awaitExitHook = awaitExit

// startOnOwnThread starts cmd from a goroutine locked to an OS thread,
// which it keeps until reaped is closed. On Linux a plugin's Pdeathsig
// fires when the thread that started it exits, not only the host, and
// Go ends a thread when a goroutine locked to it exits (Gio's window
// goroutine locks one), so no other goroutine may run, and exit, on the
// starting thread while the plugin lives. One thread per plugin also
// keeps a start that hangs from holding up the others.
func startOnOwnThread(cmd *exec.Cmd, reaped <-chan struct{}) error {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		err := cmd.Start()
		errc <- err
		if err == nil {
			<-reaped
		}
		runtime.UnlockOSThread()
	}()
	return <-errc
}
