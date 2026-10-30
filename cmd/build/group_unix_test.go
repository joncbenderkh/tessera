// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix

package main

import (
	"syscall"
	"testing"
)

func checkOwnGroup(t *testing.T, pid int) {
	t.Helper()
	if group, err := syscall.Getpgid(pid); err != nil || group != pid {
		t.Errorf("host %d is in process group %d (%v), want its own", pid, group, err)
	}
}
