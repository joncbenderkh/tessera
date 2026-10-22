// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// settleDelay groups the burst of events an editor or an atomic rename
// produces into a single reload.
const settleDelay = 100 * time.Millisecond

// linkCheck is how often Watch checks where a symlinked config, or its
// symlinked directory, points, whether its directory was replaced, and
// rereads the file a link points to. kqueue (macOS, the BSDs)
// watches the file a link points to, not the link, so it reports no event
// when the link is replaced, and no backend reports a link above the
// watched directory, or the directory itself, being replaced.
const linkCheck = time.Second

// Watch calls onChange whenever the file at path changes content, until ctx
// is done. It reports invalid configs as errors rather than stopping, so a
// half-finished edit does not end the watch. Saves that leave the content
// unchanged, including the host's own Save of an identical config, are
// ignored. from is the content the caller last read, as Read returns it;
// if the file no longer holds it when Watch starts, that is a change too,
// so an edit made between the two is not missed.
//
// It watches the directory rather than the file, because atomic saves
// replace the file and editors often do the same. It watches the
// directory by its real path, so if path's directory, or one above it, is
// a link, as with a dotfiles manager, it watches the directory the link
// points to, and follows the link when it is pointed elsewhere. If path
// itself is a link, the file it points to is checked every linkCheck
// rather than watched: its directory can be as large as /nix/store, and
// kqueue opens a file for each entry of a watched directory.
func Watch(ctx context.Context, path string, from []byte, onChange func(Config, error)) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	path = filepath.Clean(path)
	// dir is the directory watched, by real path, with what it was when
	// it was added, so one replaced by another of the same name is
	// noticed: the watch stays on the old one. It is "" if none is.
	dir := realDir(path)
	if err := watcher.Add(dir); err != nil {
		return err
	}
	dirInfo := stat(dir)
	// target is where a link at path points, with every link resolved,
	// or "" if path is not one or the file is not there.
	var target string
	follow := func() {
		target = linkTarget(path)
		want := realDir(path)
		if dir != "" && (dir != want || !same(dirInfo, stat(dir))) {
			watcher.Remove(dir)
			dir = ""
		}
		// A directory that is gone is retried at the next check.
		if info := stat(want); dir == "" && info != nil && watcher.Add(want) == nil {
			dir, dirInfo = want, info
		}
	}
	follow()
	// due reports whether the file is to be read again: path is a link,
	// whose file is checked rather than watched, or the directory to
	// watch was replaced or could not be watched yet.
	due := func() bool {
		return linkTarget(path) != "" || target != "" || dir != realDir(path) || !same(dirInfo, stat(dir))
	}

	last := from
	var lastErr string         // the read error last reported, so it is not repeated each check
	settle := time.NewTimer(0) // compare the file with from at once
	check := time.NewTicker(linkCheck)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-check.C:
			if due() {
				settle.Reset(settleDelay)
			}
		case err := <-watcher.Errors:
			onChange(Config{}, &WatchError{err})
		case event := <-watcher.Events:
			// Moving or removing the directory ends its watch, though it
			// may be back, the same directory, by the next check.
			if filepath.Clean(event.Name) == dir && event.Has(fsnotify.Rename|fsnotify.Remove) {
				dir = ""
				settle.Reset(settleDelay)
				continue
			}
			// Matched by name alone: an event names the file by the path
			// its directory was watched under, and the content check
			// below drops events for other files of the same name.
			if name := filepath.Base(event.Name); name == filepath.Base(path) || (target != "" && name == filepath.Base(target)) {
				settle.Reset(settleDelay)
			}
		case <-settle.C:
			follow()
			data, err := os.ReadFile(path)
			if err != nil {
				if err.Error() != lastErr {
					lastErr = err.Error()
					onChange(Config{}, err)
				}
				continue
			}
			// After a read error, the content it last had is reported
			// again, so the error is known to be over.
			recovered := lastErr != ""
			lastErr = ""
			if bytes.Equal(data, last) && !recovered {
				continue
			}
			last = data
			onChange(Parse(data))
		}
	}
}

// WatchError is an error in watching the file, such as missed events,
// rather than in its content.
type WatchError struct{ Err error }

func (e *WatchError) Error() string { return "watching for changes: " + e.Err.Error() }
func (e *WatchError) Unwrap() error { return e.Err }

// same reports whether a and b are the same file.
func same(a, b os.FileInfo) bool { return a != nil && b != nil && os.SameFile(a, b) }

// stat is what is at path now, or nil if nothing is.
func stat(path string) os.FileInfo {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	// On Windows, SameFile reads the file's ID by path on its first call;
	// read it now, before the path can name another file.
	os.SameFile(info, info)
	return info
}

// realDir is the directory path is in, with symlinks resolved.
func realDir(path string) string {
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return filepath.Dir(path)
	}
	return dir
}

// linkTarget returns the file a symlink at path points to, or "" if path is
// not a symlink or does not resolve. A link only above path, such as a
// linked config directory or a /home that links to /var/home, does not
// count: the directory watch sees through it.
func linkTarget(path string) string {
	if info, err := os.Lstat(path); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return ""
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	return filepath.Clean(target)
}
