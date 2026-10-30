// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
