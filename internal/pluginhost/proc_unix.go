// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix

package pluginhost

import (
	"os"
	"syscall"
)

// killGroup kills the plugin's process group.
func killGroup(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
}
