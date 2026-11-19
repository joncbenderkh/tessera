// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"os"
	"os/exec"
	"syscall"
)

// freeze stops the process, which then answers nothing until killed.
func freeze() { _ = syscall.Kill(os.Getpid(), syscall.SIGSTOP) }

// leaveGroup makes cmd start in a session, and process group, of its own.
func leaveGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// alive reports whether a process with this PID exists.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
