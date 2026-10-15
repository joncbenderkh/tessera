// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts the plugin in a process group of its own, so
// killGroup reaches its children too, and has the kernel kill it if
// the host dies without shutting it down. Pdeathsig fires when the OS
// thread that started the plugin exits, so plugins are started on a
// thread that never does (forkThread).
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
