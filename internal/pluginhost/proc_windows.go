// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// ownProcessGroup does nothing on Windows: killGroup kills only the
// plugin, not its children, and a plugin outlives a host that dies
// without shutting it down.
func ownProcessGroup(*exec.Cmd) {}

// killCode is the exit code killGroup gives the plugin: 128 plus
// SIGKILL's number, as a Unix shell reports a killed process, and
// unlike the 1 of os.Process.Kill, which plugins also exit with.
const killCode = 137

// killStatus is the status of a plugin TerminateProcess killed with
// killCode, and procKillStatus of one os.Process.Kill killed.
const (
	killStatus     = "exit status 137"
	procKillStatus = "exit status 1"
)

// badExecutable reports whether err, from starting the plugin, says its
// file is not a program this system runs.
func badExecutable(err error) bool {
	return errors.Is(err, windows.ERROR_BAD_EXE_FORMAT) || errors.Is(err, windows.ERROR_EXE_MACHINE_TYPE_MISMATCH)
}

// killGroup kills the plugin and returns the status that gives it, or
// "" if it was not killed: one that has exited cannot be. Go holds its
// handle until Wait, so the PID still names it.
func killGroup(p *os.Process) string {
	if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(p.Pid)); err == nil {
		defer windows.CloseHandle(h)
		if windows.TerminateProcess(h, killCode) == nil {
			return killStatus
		}
	}
	if p.Kill() == nil {
		return procKillStatus
	}
	return ""
}

// canAwaitExit says awaitExit can wait without reaping.
const canAwaitExit = true

// awaitExit waits on a handle of its own to p, which leaves Go's handle,
// and so the process's exit code and PID, in place. It reports whether
// it saw the exit; false means it could not watch.
func awaitExit(p *os.Process) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(p.Pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, windows.INFINITE)
	return err == nil && ev == windows.WAIT_OBJECT_0
}

// reapPID reports how the exited process pid ended. Windows has no
// zombies to reap; Release closes Go's handle afterwards.
func reapPID(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("exit status %d", code)
	}
	return nil
}
