// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/shirou/gopsutil/v4/host"
	"github.com/tklauser/go-sysconf"
)

// sample reads the table's fields of a process, and its CPU time, from
// its stat file, where gopsutil reads three or four files; ok is false
// if the process is gone.
func sample(pid int32) (pr proc, t cpuTime, ok bool) {
	st, err := readStat(pid)
	if err != nil {
		return proc{}, cpuTime{}, false
	}
	pr = proc{pid: pid, name: valid(nameOf(pid, st.comm)), rss: st.rss, start: st.start}
	return pr, cpuTime{start: st.start, seconds: st.seconds}, true
}

// nameOf is the name of the process pid, whose name in its stat file
// is comm. The kernel cuts that to 15 bytes, so a name that long is
// taken from its executable's, if that starts with it, as it does
// unless the process renamed itself. gopsutil reads the command line
// instead, which waits on the process's memory, so a process stuck in
// the kernel, as on a network mount that stopped answering, would
// hold up the whole table.
func nameOf(pid int32, comm string) string {
	if len(comm) < 15 {
		return comm
	}
	exe, err := os.Readlink(filepath.Join(hostProc, strconv.FormatInt(int64(pid), 10), "exe"))
	if err != nil {
		return comm
	}
	if base := filepath.Base(strings.TrimSuffix(exe, " (deleted)")); strings.HasPrefix(base, comm) {
		return base
	}
	return comm
}

// stat is what the panel takes from a process's stat file.
type stat struct {
	comm    string
	seconds float64 // CPU time, user and system
	start   uint64  // clock ticks from boot to the process's start
	rss     uint64  // bytes
}

var errStat = errors.New("malformed stat file")

// hostProc is where proc is mounted: /proc, or HOST_PROC if set, as
// gopsutil reads it, which lists the processes.
var hostProc = cmp.Or(os.Getenv("HOST_PROC"), "/proc")

// clockTicks is the kernel's clock ticks per second, in which stat
// counts time.
var clockTicks = sync.OnceValue(func() int64 {
	if n, err := sysconf.Sysconf(sysconf.SC_CLK_TCK); err == nil && n > 0 {
		return n
	}
	return 100
})

// boot is when the system booted, in seconds since the epoch, once
// read, so the start times made from it stay the same.
var boot struct {
	sync.Mutex
	secs uint64
}

// readBootTime reads when the system booted.
var readBootTime = host.BootTime

// bootTime is when the system booted, read until a read succeeds.
func bootTime() (uint64, error) {
	boot.Lock()
	defer boot.Unlock()
	if boot.secs == 0 {
		secs, err := readBootTime()
		if err != nil {
			return 0, err
		}
		boot.secs = secs
	}
	return boot.secs, nil
}

// created is when the process was created, in milliseconds since the
// epoch.
func (st stat) created() (int64, error) {
	boot, err := bootTime()
	if err != nil {
		return 0, err
	}
	return int64(boot)*1000 + int64(st.start)*1000/clockTicks(), nil
}

func readStat(pid int32) (stat, error) {
	data, err := os.ReadFile(filepath.Join(hostProc, strconv.FormatInt(int64(pid), 10), "stat"))
	if err != nil {
		return stat{}, err
	}
	return parseStat(data)
}

// parseStat parses a stat file, described in proc_pid_stat(5). The
// name, in parentheses, may itself hold spaces and parentheses, so the
// fields after it start after the last ")".
func parseStat(data []byte) (stat, error) {
	open, end := bytes.IndexByte(data, '('), bytes.LastIndexByte(data, ')')
	if open < 0 || end < open {
		return stat{}, errStat
	}
	// fields[0] is field 3, the state.
	fields := bytes.Fields(data[end+1:])
	if len(fields) < 22 {
		return stat{}, errStat
	}
	num := func(n int) (uint64, error) { return strconv.ParseUint(string(fields[n-3]), 10, 64) }
	utime, err1 := num(14)
	stime, err2 := num(15)
	start, err3 := num(22)
	rss, err4 := num(24)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return stat{}, err
	}
	return stat{
		comm:    string(data[open+1 : end]),
		seconds: float64(utime+stime) / float64(clockTicks()),
		start:   start,
		rss:     rss * uint64(os.Getpagesize()),
	}, nil
}
