// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix

package pluginhost

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// killStatus is the status of a plugin the host killed.
const killStatus = "signal: killed"

// badExecutable reports whether err, from starting the plugin, says its
// file is not a program this system runs.
func badExecutable(err error) bool { return errors.Is(err, syscall.ENOEXEC) }

// killGroup kills the plugin's process group.
func killGroup(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
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
	case ws.Signaled():
		return fmt.Errorf("signal: %v", ws.Signal())
	case ws.ExitStatus() != 0:
		return fmt.Errorf("exit status %d", ws.ExitStatus())
	}
	return nil
}
