// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix

package pluginhost

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// killStatus is the status of a plugin killGroup killed, and
// procKillStatus of one os.Process.Kill killed.
const (
	killStatus     = "signal: killed"
	procKillStatus = killStatus
)

// badExecutable reports whether err, from starting the plugin, says its
// file is not a program this system runs: not one at all, a script
// whose interpreter is missing (find saw the file itself), or one on a
// file system that forbids running it.
func badExecutable(err error) bool {
	return errors.Is(err, syscall.ENOEXEC) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EACCES)
}

// killGroup kills the plugin's process group and returns the status
// that gives the plugin, or "" if the group is gone. An exited plugin
// not yet reaped still counts, so one killed by something else (the
// OOM killer) before watch sees it exit reads as the host's kill.
func killGroup(p *os.Process) string {
	if syscall.Kill(-p.Pid, syscall.SIGKILL) != nil {
		return ""
	}
	return killStatus
}

// reapPID reaps the exited process pid without closing anything of the
// exec.Cmd that started it, unlike Cmd.Wait.
func reapPID(pid int) error {
	var ws syscall.WaitStatus
	for {
		_, err := syscall.Wait4(pid, &ws, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	switch {
	case ws.Signaled() && ws.CoreDump(): // as ProcessState.String
		return fmt.Errorf("signal: %v (core dumped)", ws.Signal())
	case ws.Signaled():
		return fmt.Errorf("signal: %v", ws.Signal())
	case ws.ExitStatus() != 0:
		return fmt.Errorf("exit status %d", ws.ExitStatus())
	}
	return nil
}
