// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package pluginhost

import (
	"os"
	"syscall"
)

const canFreeze = true

// freeze stops the process, which then answers nothing until killed.
func freeze() { _ = syscall.Kill(os.Getpid(), syscall.SIGSTOP) }
