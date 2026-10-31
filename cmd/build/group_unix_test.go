// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix

package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func checkOwnGroup(t *testing.T, pid int) {
	t.Helper()
	if group, err := syscall.Getpgid(pid); err != nil || group != pid {
		t.Errorf("host %d is in process group %d (%v), want its own", pid, group, err)
	}
}

// backgroundSafe reports whether the terminal cannot stop this process
// for being in the background: whether it started with SIGTTOU and
// SIGTTIN ignored, which only Linux shows (in /proc), and which
// signal.Ignored does not report.
func backgroundSafe() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(status)) {
		if mask, ok := strings.CutPrefix(line, "SigIgn:"); ok {
			ignored, err := strconv.ParseUint(strings.TrimSpace(mask), 16, 64)
			want := uint64(1)<<(syscall.SIGTTOU-1) | uint64(1)<<(syscall.SIGTTIN-1)
			return err == nil && ignored&want == want
		}
	}
	return false
}
