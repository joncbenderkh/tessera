// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// settleDelay groups the burst of events an editor or an atomic rename
// produces into a single reload.
const settleDelay = 100 * time.Millisecond

// Watch calls onChange whenever the file at path changes content, until ctx
// is done. It reports invalid configs as errors rather than stopping, so a
// half-finished edit does not end the watch. Saves that leave the content
// unchanged, including the host's own Save of an identical config, are
// ignored.
//
// It watches the directory rather than the file, because atomic saves
// replace the file and editors often do the same.
func Watch(ctx context.Context, path string, onChange func(Config, error)) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		return err
	}

	last, _ := os.ReadFile(path)
	settle := time.NewTimer(0)
	<-settle.C
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-watcher.Errors:
			onChange(Config{}, err)
		case event := <-watcher.Events:
			if filepath.Clean(event.Name) == filepath.Clean(path) {
				settle.Reset(settleDelay)
			}
		case <-settle.C:
			data, err := os.ReadFile(path)
			if err != nil {
				onChange(Config{}, err)
				continue
			}
			if bytes.Equal(data, last) {
				continue
			}
			last = data
			onChange(Parse(data))
		}
	}
}
