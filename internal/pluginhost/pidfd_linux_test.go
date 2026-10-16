// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"os"
	"path/filepath"
)

// pidfds counts this process's open pidfds, or returns -1 if it cannot.
func pidfds() int {
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	n := 0
	for _, fd := range fds {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name())); err == nil && target == "anon_inode:[pidfd]" {
			n++
		}
	}
	return n
}
