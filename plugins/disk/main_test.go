// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/joncbenderkh/tessera/internal/plugintest"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

func TestMain(m *testing.M) { plugintest.Main(m, main) }

// gauge returns the first gauge in v.
func gauge(v *tesserav1.View) *tesserav1.Gauge {
	for _, w := range plugintest.Widgets(v.GetBody()) {
		if g := w.GetGauge(); g != nil {
			return g
		}
	}
	return nil
}

func TestView(t *testing.T) {
	u := &disk.UsageStat{Fstype: "ext4", Total: 1 << 40, Free: 320 << 30, UsedPercent: 68.04}
	v := view("/", "/dev/nvme0n1p2", u).Proto()
	g := gauge(v)
	if g.GetDisplay() != "68" || g.GetUnit() != "%" || g.GetCaption() != "320 GiB free of 1.0 TiB" ||
		g.GetWarnAt() != warnAt || g.GetErrorAt() != errorAt {
		t.Errorf("gauge %v", g)
	}
	if v.GetTitle() != "Disk /" {
		t.Errorf("title %q, want Disk /", v.GetTitle())
	}
	if got, want := plugintest.Texts(v.GetBody()), []string{"/dev/nvme0n1p2 · ext4"}; !slices.Equal(got, want) {
		t.Errorf("texts %q, want %q", got, want)
	}
	v = view("/", "", &disk.UsageStat{}).Proto()
	if got := plugintest.Texts(v.GetBody()); len(got) != 0 {
		t.Errorf("texts with no device or type %q, want none", got)
	}
}

func TestDisk(t *testing.T) {
	p := plugintest.Launch(t, "disk")
	if got := p.Info.GetName(); got != "disk" {
		t.Errorf("name %q, want disk", got)
	}
	for _, s := range []map[string]any{{"mount": "relative/path"}, {"mount": defaultMount + "a\x00"}, {"mount": 1.0}, {"interval": "0s"}} {
		if err := p.Configure(s); status.Code(err) != codes.InvalidArgument {
			t.Errorf("settings %v: %v, want INVALID_ARGUMENT", s, err)
		}
	}
	if err := p.Configure(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	mount := defaultMount
	p.Await(func(v *tesserav1.View) bool { return gauge(v) != nil && v.GetTitle() == "Disk "+mount })
	missing := t.TempDir() + string(os.PathSeparator) + "missing"
	if err := p.Configure(map[string]any{"mount": missing}); err != nil {
		t.Fatal(err)
	}
	p.AwaitTextsFunc(func(texts []string) bool {
		return len(texts) > 0 && texts[0] == "Cannot read the file system at "+missing
	})
	unmounted := t.TempDir()
	if err := p.Configure(map[string]any{"mount": unmounted}); err != nil {
		t.Fatal(err)
	}
	p.AwaitTextsFunc(func(texts []string) bool {
		return len(texts) > 0 && texts[0] == "Nothing is mounted at "+unmounted
	})
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	if err := p.Configure(map[string]any{"mount": link}); err != nil {
		t.Fatal(err)
	}
	p.Await(func(v *tesserav1.View) bool { return gauge(v) != nil && v.GetTitle() == "Disk "+link })
}
