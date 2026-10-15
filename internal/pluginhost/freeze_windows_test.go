// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

const canFreeze = false

func freeze() {}

func leaveGroup(*exec.Cmd) {}

// alive reports whether a process with this PID is running.
func alive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	const stillActive = 259
	return windows.GetExitCodeProcess(h, &code) == nil && code == stillActive
}
