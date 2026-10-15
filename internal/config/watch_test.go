// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type change struct {
	config Config
	err    error
}

func TestWatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	changes := make(chan change, 8)
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, path, func(c Config, err error) { changes <- change{c, err} })
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch: %v", err)
		}
	}()
	time.Sleep(50 * time.Millisecond) // let the watcher register before writing

	next := func() change {
		t.Helper()
		select {
		case c := <-changes:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("no change reported")
			return change{}
		}
	}

	if err := Save(path, Default()); err != nil {
		t.Fatal(err)
	}
	wider := Default()
	wider.Grid.Columns = 6
	if err := Save(path, wider); err != nil {
		t.Fatal(err)
	}
	if c := next(); c.err != nil || c.config.Grid.Columns != 6 {
		t.Fatalf("after saving 6 columns: got %+v", c)
	}

	if err := os.WriteFile(path, []byte("version: 1\ngrid: {columns: 0, row_height: 1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := next(); c.err == nil {
		t.Fatal("expected an error for an invalid edit")
	}

	select {
	case c := <-changes:
		t.Fatalf("unexpected extra change: %+v", c)
	case <-time.After(3 * settleDelay):
	}
}
