// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix

package main

import (
	"os/exec"
	"os/signal"
	"syscall"
)

// ownGroup starts cmd in a process group of its own. That group is in
// the terminal's background, where writing to it (with stty tostop) or
// reading from it would stop the host; it inherits these signals
// ignored, so a write goes through and a read fails instead.
func ownGroup(cmd *exec.Cmd) {
	signal.Ignore(syscall.SIGTTOU, syscall.SIGTTIN)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
