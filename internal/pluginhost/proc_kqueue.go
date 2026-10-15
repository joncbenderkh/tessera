// SPDX-License-Identifier: GPL-3.0-or-later

//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package pluginhost

import (
	"os"

	"golang.org/x/sys/unix"
)

// canAwaitExit says awaitExit can wait without reaping.
const canAwaitExit = true

// awaitExit waits for p to exit, through a kqueue NOTE_EXIT event, which
// leaves it unreaped so its PID and process group ID stay reserved. It
// reports whether it saw the exit; false means it could not watch.
func awaitExit(p *os.Process) bool {
	kq, err := unix.Kqueue()
	if err != nil {
		return false
	}
	defer unix.Close(kq)
	var ev unix.Kevent_t
	unix.SetKevent(&ev, p.Pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	ev.Fflags = unix.NOTE_EXIT
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, []unix.Kevent_t{ev}, events, nil)
		switch {
		case err == unix.EINTR:
			continue
		case err == unix.ESRCH:
			return true // exited before it was watched; Wait has not reaped it
		case err != nil || n != 1:
			return false
		}
		// A registration error comes back as an event too.
		if events[0].Flags&unix.EV_ERROR != 0 {
			return unix.Errno(events[0].Data) == unix.ESRCH
		}
		return events[0].Fflags&unix.NOTE_EXIT != 0
	}
}
