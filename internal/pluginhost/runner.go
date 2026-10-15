// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
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
// a Shutdown that left children behind.
type procRunner struct {
	path   string
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser

	mu       sync.Mutex
	proc     *os.Process
	canceled bool
}

var _ runner.Runner = (*procRunner)(nil)

// runnerFunc is go-plugin's ClientConfig.RunnerFunc for r. spec carries
// the environment go-plugin prepared (cookie, certificate, socket dir).
func (r *procRunner) runnerFunc(_ hclog.Logger, spec *exec.Cmd, _ string) (runner.Runner, error) {
	r.cmd = exec.Command(r.path)
	r.cmd.Env = spec.Env
	// No stdin: go-plugin would pass the host's, and a plugin in its own
	// process group that read a terminal would be stopped by SIGTTIN.
	ownProcessGroup(r.cmd)
	var err error
	if r.stdout, err = r.cmd.StdoutPipe(); err != nil {
		return nil, err
	}
	if r.stderr, err = r.cmd.StderrPipe(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *procRunner) Start(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.canceled {
		return errors.New("launch canceled")
	}
	if err := forkThread(r.cmd.Start); err != nil {
		return err
	}
	r.proc = r.cmd.Process
	return nil
}

// kill kills the plugin's process group and stops a pending Start.
// Called after the plugin has exited, it reaches children left in the
// group; the group ID could only belong to another process if the
// plugin's PID were reused, by a new group leader, in between.
func (r *procRunner) kill() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.canceled = true
	if r.proc != nil {
		killGroup(r.proc)
	}
}

func (r *procRunner) Wait(context.Context) error { return r.cmd.Wait() }

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

var (
	forkOnce sync.Once
	forks    = make(chan func())
)

// forkThread runs start on one OS thread that never exits. On Linux a
// plugin's Pdeathsig fires when the thread that started it exits, not
// only the host, and Go ends a thread when a goroutine locked to it
// exits (Gio's window goroutine locks one), so plugins are started from
// a thread kept for the purpose.
func forkThread(start func() error) error {
	forkOnce.Do(func() {
		go func() {
			runtime.LockOSThread()
			for f := range forks {
				f()
			}
		}()
	})
	errc := make(chan error, 1)
	forks <- func() { errc <- start() }
	return <-errc
}
