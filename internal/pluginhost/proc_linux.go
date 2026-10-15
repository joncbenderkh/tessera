// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts the plugin in a process group of its own, so
// killGroup reaches its children too, and has the kernel kill it if
// the host dies without shutting it down. Pdeathsig fires when the OS
// thread that started the plugin exits; Go only ends a thread when a
// goroutine locked to it exits, which nothing in the host does.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
