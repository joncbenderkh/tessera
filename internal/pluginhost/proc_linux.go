// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// ownProcessGroup puts the plugin in a process group of its own, so
// killGroup reaches its children too, and has the kernel kill it if
// the host dies without shutting it down. Pdeathsig fires when the OS
// thread that started the plugin exits, so plugins are started on a
// thread that never does (forkThread).
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

// canAwaitExit says awaitExit can wait without reaping.
const canAwaitExit = true

// awaitExit waits for p to exit without reaping it, so its PID and
// process group ID stay reserved, and reports whether it did.
func awaitExit(p *os.Process) bool {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, p.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err == nil
		}
	}
}
