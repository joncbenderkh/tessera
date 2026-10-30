// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// deviceOf is the device and file system type mounted at mount, or at
// the path it links to, or errNotMounted if nothing is. The type is
// the mount table's: Usage names it from statfs, which tells no ext4
// from ext2.
func deviceOf(mount string) (device, fstype string, err error) {
	if target, err := filepath.EvalSymlinks(mount); err == nil {
		mount = target
	}
	// The ID first, so a mount made since is listed in the mounts of
	// the plugin's own mount namespace, which Usage sees.
	id := mountID(mount)
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", "", err
	}
	return resolve(data, filepath.Clean(mount), id)
}

// resolve is deviceOf for a mountinfo file, the clean path mount, and
// the ID of the mount it is on, if known. A root that is no mount
// point, as a chroot's, is on a mount out of sight, not listed: its
// usage is read, but not its device.
func resolve(mountinfo []byte, mount, id string) (device, fstype string, err error) {
	device, fstype, found := mountedAt(mountinfo, mount, id)
	if !found {
		if mount == "/" {
			return "", "", nil
		}
		return "", "", fmt.Errorf("%s: %w", mount, errNotMounted)
	}
	return strings.ToValidUTF8(device, "�"), strings.ToValidUTF8(fstype, "�"), nil
}

// mountID is the ID of the mount the path mount is on, as the kernel
// resolves it, or "" if the kernel, older than 5.8, does not tell.
func mountID(mount string) string {
	var st unix.Statx_t
	if unix.Statx(unix.AT_FDCWD, mount, 0, unix.STATX_MNT_ID, &st) != nil || st.Mask&unix.STATX_MNT_ID == 0 {
		return ""
	}
	return strconv.FormatUint(st.Mnt_id, 10)
}

// mountedAt finds the mount at the clean path mount in a mountinfo
// file, described in proc_pid_mountinfo(5): the one whose ID is id, if
// known, and mounted there. Otherwise, of mounts stacked on mount, the
// one in sight: no other is mounted on it there, whatever order they
// are listed in, as one moved there later is listed before those it
// covers, and none is on a mount up its chain of parents that is
// covered so. An automount's placeholder on top is no file system yet.
func mountedAt(mountinfo []byte, mount, id string) (device, fstype string, found bool) {
	type entry struct{ id, parent, point, device, fstype string }
	var mounts []entry
	for line := range strings.SplitSeq(string(mountinfo), "\n") {
		// ID, parent ID, major:minor, root, mount point, options,
		// optional fields, "-", type, source, super options. Fields
		// are split by single spaces, as the kernel escapes any in
		// them, and the source may be empty.
		fields := strings.Split(line, " ")
		sep := slices.Index(fields, "-")
		if len(fields) < 5 || sep < 6 || sep+2 >= len(fields) {
			continue
		}
		mounts = append(mounts, entry{fields[0], fields[1], unescape(fields[4]), unescape(fields[sep+2]), unescape(fields[sep+1])})
	}
	byID := make(map[string]entry, len(mounts))
	for _, e := range mounts {
		byID[e.id] = e
	}
	// coveredBut reports whether a mount other than e and by is
	// mounted on e where e is mounted, hiding it.
	coveredBut := func(e entry, by string) bool {
		return slices.ContainsFunc(mounts, func(o entry) bool {
			return o.parent == e.id && o.point == e.point && o.id != e.id && o.id != by
		})
	}
	inSight := func(e entry) bool {
		// Up the chain of parents, each must be uncovered but by the
		// mount below it. The chain ends at the namespace's root, whose
		// parent is not listed or is itself; len(mounts) steps bound
		// it in a malformed file.
		by := ""
		for range len(mounts) {
			if coveredBut(e, by) {
				return false
			}
			parent, ok := byID[e.parent]
			if !ok || parent.id == e.id {
				return true
			}
			by, e = e.id, parent
		}
		return false
	}
	for _, e := range mounts {
		if e.point != mount || e.fstype == "autofs" {
			continue
		}
		if id != "" && e.id == id || id == "" && inSight(e) {
			device, fstype, found = e.device, e.fstype, true
		}
	}
	return device, fstype, found
}

// unescape decodes the octal escapes, such as \040 for a space, the
// kernel writes in mountinfo for space, tab, newline, and backslash.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return '0' <= c && c <= '7' }
