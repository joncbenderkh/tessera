// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"testing"
)

func TestDeviceOfType(t *testing.T) {
	if _, err := os.Stat("/proc/self/mountinfo"); err != nil {
		t.Skip(err)
	}
	device, fstype, err := deviceOf("/")
	if err != nil {
		t.Fatal(err)
	}
	if fstype == "" {
		t.Errorf("deviceOf(/) = %q, no type", device)
	}
}

func TestMountedAt(t *testing.T) {
	mountinfo := []byte(`1 1 0:1 / / rw - rootfs rootfs rw
22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw
40 22 0:38 / /mnt/a"b\040c rw,relatime shared:20 - nfs4 server:/export\040dir rw
41 22 0:39 / /mnt/` + "\xff" + ` rw - tmpfs tmpfs rw
42 22 0:40 / /auto rw - autofs systemd-1 rw
43 42 0:41 / /auto rw master:3 - xfs /dev/sdb1 rw
44 22 0:42 / /empty rw - tmpfs  rw,inode64
45 47 0:43 / /moved rw - tmpfs older rw
47 22 0:44 / /moved rw - ramfs newer rw
50 50 0:45 / /solo rw - tmpfs solo rw
`)
	for _, tc := range []struct {
		mount, device, fstype string
		found                 bool
	}{
		{"/", "/dev/nvme0n1p2", "ext4", true},
		{`/mnt/a"b c`, "server:/export dir", "nfs4", true},
		{"/mnt/\xff", "tmpfs", "tmpfs", true},
		{"/auto", "/dev/sdb1", "xfs", true},
		{"/empty", "", "tmpfs", true},
		{"/moved", "older", "tmpfs", true},
		{"/solo", "solo", "tmpfs", true},
		{"/mnt", "", "", false},
	} {
		device, fstype, found := mountedAt(mountinfo, tc.mount, "")
		if device != tc.device || fstype != tc.fstype || found != tc.found {
			t.Errorf("mountedAt(%q) = %q, %q, %v; want %q, %q, %v", tc.mount, device, fstype, found, tc.device, tc.fstype, tc.found)
		}
	}
	// A mount made under a covered one at the same point, which only
	// the kernel's mount ID tells from the one in sight.
	hidden := []byte(`794 1 0:50 / /mnt rw - tmpfs A rw
795 794 0:51 / /mnt rw - tmpfs C rw
796 795 0:52 / /mnt/x rw - ramfs D rw
830 794 0:53 / /mnt/x rw - tmpfs B rw
`)
	for _, tc := range []struct {
		mount, id, device string
		found             bool
	}{
		{"/mnt/x", "796", "D", true},
		{"/mnt/x/sub", "796", "", false},
		{"/mnt", "795", "C", true},
		// Without the kernel's ID, B is on A, which C covers.
		{"/mnt/x", "", "D", true},
		{"/mnt", "", "C", true},
	} {
		if device, _, found := mountedAt(hidden, tc.mount, tc.id); device != tc.device || found != tc.found {
			t.Errorf("mountedAt(%q, %s) = %q, %v; want %q, %v", tc.mount, tc.id, device, found, tc.device, tc.found)
		}
	}
	if got := unescape(`a\134b\040c\0`); got != `a\b c\0` {
		t.Errorf("unescape = %q", got)
	}
}

func TestResolve(t *testing.T) {
	// In a chroot whose root is no mount point, the mount holding it is
	// not listed, as its own mount point is out of sight.
	mountinfo := []byte(`60 59 0:50 / /proc rw - proc proc rw
`)
	for _, tc := range []struct {
		mount, id, device string
		notMounted        bool
	}{
		{"/", "59", "", false},
		{"/home", "59", "", true},
		{"/proc", "60", "proc", false},
		{"/proc/sys", "60", "", true},
		{"/", "", "", false},
		{"/home", "", "", true},
	} {
		device, _, err := resolve(mountinfo, tc.mount, tc.id)
		if device != tc.device || errors.Is(err, errNotMounted) != tc.notMounted || err != nil && !tc.notMounted {
			t.Errorf("resolve(%q, %s) = %q, %v; want %q, not mounted %v", tc.mount, tc.id, device, err, tc.device, tc.notMounted)
		}
	}
}
