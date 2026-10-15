// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix && !linux

package pluginhost

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts the plugin in a process group of its own, so
// killGroup reaches its children too. Unlike on Linux, a plugin outlives
// a host that dies without shutting it down.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
