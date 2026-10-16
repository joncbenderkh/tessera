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
	"time"

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
	// childEnds are the write ends of the pipes, which the host closes
	// once the plugin has started with its own copies.
	childEnds []*os.File

	mu       sync.Mutex
	proc     *os.Process
	pid      int // proc.Pid, kept since Release clears it
	canceled bool
	killed   bool          // kill signalled it before watch saw it exit
	exited   bool          // reaped: its PID, and so its group ID, may be reused
	reaping  bool          // Wait is reaping it without watch
	exitSeen bool          // watch saw it exit, unreaped
	reaped   chan struct{} // closed once the plugin is reaped
	watched  chan struct{} // closed by watch; Wait reaps only after it
	drained  chan struct{} // closed by Wait: go-plugin has read both pipes to the end

	reapOnce sync.Once
	waitErr  error
	reapedOK bool // waitErr describes the plugin's exit; under mu

	exitCh   chan struct{} // closed once the plugin has exited
	exitOnce sync.Once
}

// After the plugin exits, a child that left its process group can hold
// its stdout and stderr open, and go-plugin reaps the plugin only once
// both close. watch then reaps it after pipeGrace and, drainTimeout
// later, closes the pipes so go-plugin finishes; output still unread by
// then, which only a stalled reader leaves, is lost.
const (
	pipeGrace    = time.Second
	drainTimeout = 1500 * time.Millisecond
)

var _ runner.Runner = (*procRunner)(nil)

// runnerFunc is go-plugin's ClientConfig.RunnerFunc for r. spec carries
// the environment go-plugin prepared (cookie, certificate, socket dir).
func (r *procRunner) runnerFunc(_ hclog.Logger, spec *exec.Cmd, tmpDir string) (runner.Runner, error) {
	r.tmpDir = tmpDir
	r.exitCh = make(chan struct{})
	r.cmd = exec.Command(r.path)
	r.cmd.Env = spec.Env
	// No stdin: go-plugin would pass the host's, and a plugin in its own
	// process group that read a terminal would be stopped by SIGTTIN.
	ownProcessGroup(r.cmd)
	// The pipes are made here rather than by Cmd.StdoutPipe, so the host
	// holds both ends and can close them all if the launch is canceled
	// before the process starts.
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderr, childStderr, err := os.Pipe()
	if err != nil {
		stdout.Close()
		childStdout.Close()
		return nil, err
	}
	r.cmd.Stdout, r.cmd.Stderr = childStdout, childStderr
	r.childEnds = []*os.File{childStdout, childStderr}
	r.stdout = &lineLimiter{ReadCloser: stdout}
	r.stderr = stderr
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
		r.closeChildEnds()
		r.closePipes()
		return errors.New("launch canceled")
	}
	r.reaped = make(chan struct{})
	r.drained = make(chan struct{})
	err := startOnOwnThread(r.cmd, r.reaped)
	r.closeChildEnds() // the plugin has its own copies, if it started
	if err != nil {
		r.closePipes()
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.proc = r.cmd.Process
	r.pid = r.proc.Pid
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
// ending children it left behind, before anything reaps the plugin, so
// its group ID cannot have been reused. It alone handles pipes that a
// child that left the group holds open (see pipeGrace): neither Close
// nor a canceled launch touches them.
func (r *procRunner) watch() {
	seen := awaitExitHook(r.proc)
	if seen {
		r.mu.Lock()
		killGroup(r.proc)
		r.exitSeen = true
		r.mu.Unlock()
		r.markExit()
	}
	close(r.watched)
	if !seen {
		return
	}
	select {
	case <-r.reaped:
		return
	case <-time.After(pipeGrace):
	}
	r.mu.Lock()
	r.exited = true
	r.mu.Unlock()
	// wait4, unlike Cmd.Wait, leaves the pipes for go-plugin to read.
	r.reapOnce.Do(func() {
		r.waitErr = reapPID(r.pid)
		// Cmd.Wait, which no longer runs, would release the handle.
		r.mu.Lock()
		r.proc.Release()
		r.reapedOK = true
		r.mu.Unlock()
		close(r.reaped)
		r.markExit()
	})
	select {
	case <-r.drained:
	case <-time.After(drainTimeout):
	}
	r.closePipes()
}

// closeChildEnds closes the host's copies of the pipe ends the plugin
// writes to.
func (r *procRunner) closeChildEnds() {
	for _, f := range r.childEnds {
		f.Close()
	}
}

// closePipes closes the plugin's pipes, ending go-plugin's readers even
// if a child that left the plugin's group holds them.
func (r *procRunner) closePipes() {
	r.stdout.Close()
	r.stderr.Close()
}

// reap reaps the plugin once, for Wait or watch, whichever comes first.
func (r *procRunner) reap() error {
	r.reapOnce.Do(func() {
		r.waitErr = r.cmd.Wait()
		r.closePipes() // go-plugin has read them to the end (see Wait)
		r.mu.Lock()
		r.reapedOK = true
		r.mu.Unlock()
		close(r.reaped)
		r.markExit()
	})
	return r.waitErr
}

// markExit closes exitCh, once.
func (r *procRunner) markExit() { r.exitOnce.Do(func() { close(r.exitCh) }) }

// status describes the plugin's exit once it is reaped, or is "".
func (r *procRunner) status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case !r.reapedOK:
		return ""
	case r.waitErr == nil:
		return "exit status 0"
	}
	return r.waitErr.Error()
}

// sawExit reports whether watch has seen the plugin exit.
func (r *procRunner) sawExit() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exitSeen
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
		r.killed = true
		_ = r.proc.Kill()
	default:
		r.killed = r.killed || !r.exitSeen
		killGroup(r.proc)
	}
}

// started reports whether the plugin's process started.
func (r *procRunner) started() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proc != nil
}

// ownExit reports whether status, from status(), is an exit the plugin
// made itself rather than the host's kill.
func (r *procRunner) ownExit(status string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.killed || status != "" && status != killStatus
}

// Wait reaps the plugin. When watch saw it exit (Linux, macOS, the
// BSDs, and Windows), its group has been killed, so it is marked exited
// before the reap and kill never reaches a released group ID. Otherwise
// (other Unix systems, where children it left in its group are killed
// only by a forced Close; or watch could not watch) Wait cannot know the
// plugin has exited before reaping it, so from the start of the reap
// kill signals only the plugin.
func (r *procRunner) Wait(context.Context) error {
	close(r.drained) // go-plugin calls Wait once its pipe readers are done
	if r.watched != nil {
		<-r.watched
	}
	r.mu.Lock()
	if r.exitSeen {
		r.exited = true
		r.mu.Unlock()
		return r.reap()
	}
	r.reaping = true
	r.mu.Unlock()
	err := r.reap()
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
	return strconv.Itoa(r.pid)
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
