// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// ownProcessGroup does nothing on Windows: killGroup kills only the
// plugin, not its children, and a plugin outlives a host that dies
// without shutting it down.
func ownProcessGroup(*exec.Cmd) {}

// killStatus is the status of a plugin the host killed: Kill ends it
// with exit code 1.
const killStatus = "exit status 1"

// killGroup kills the plugin.
func killGroup(p *os.Process) { _ = p.Kill() }

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
