// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

// Command disk is the disk usage panel: how full one file system is, on
// a gauge, with the space free and the device it is on. The mount point
// and how often it samples are settings.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/plugins/internal/metric"
	"github.com/joncbenderkh/tessera/sdk"
)

const (
	defaultMount    = "/"
	defaultInterval = "30s"
)

var schema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "properties": {
    "mount": {
      "type": "string",
      "description": "The mount point of the file system to show, such as /home; by default /."
    },
    ` + metric.IntervalSchema(defaultInterval) + `
  },
  "additionalProperties": false
}`

type settings struct {
	Mount    string `json:"mount"`
	Interval string `json:"interval"`
}

// The gauge's warning and error thresholds, in percent used.
const (
	warnAt  = 80
	errorAt = 95
)

// diskPanel is the panel's state. Configure and the poller run
// concurrently, so mu guards it. Polling starts on the first Configure,
// so no view shows a file system the settings do not ask for.
type diskPanel struct {
	views  chan<- sdk.View
	poller metric.Poller

	mu       sync.Mutex
	mount    string
	interval time.Duration
	lastErr  string
	// samplers has each mount's own, so a sample stuck on a mount that
	// stopped answering holds up no other, and switching back to that
	// mount does not start a second; others are dropped.
	samplers map[string]*metric.Sampler
}

func main() {
	views := make(chan sdk.View)
	d := &diskPanel{views: views}
	sdk.Serve(sdk.Plugin{
		Name:         "disk",
		Version:      tessera.Version,
		Description:  "How full a file system is.",
		ConfigSchema: schema,
		DefaultSize:  sdk.Size{Columns: 1, Rows: 2},
		MinBody:      minBody,
		Views:        views,
		Configure:    d.configure,
	})
}

func (d *diskPanel) configure(_ context.Context, cfg sdk.Config) error {
	s := settings{Interval: defaultInterval}
	if err := cfg.Decode(&s); err != nil {
		return err
	}
	interval, err := metric.ParseInterval(s.Interval)
	if err != nil {
		return err
	}
	mount := s.Mount
	if mount == "" {
		mount = defaultMount
	}
	if !filepath.IsAbs(mount) || strings.ContainsRune(mount, 0) {
		return sdk.InvalidSettings("mount %q is not an absolute path", s.Mount)
	}
	d.mu.Lock()
	changed := mount != d.mount || interval != d.interval
	if d.samplers[mount] == nil {
		if d.samplers == nil {
			d.samplers = map[string]*metric.Sampler{}
		}
		d.samplers[mount] = new(metric.Sampler)
	}
	d.mount, d.interval = mount, interval
	d.mu.Unlock()
	if changed {
		d.poller.Start(d.poll)
	}
	return nil
}

func (d *diskPanel) poll(ctx context.Context) time.Duration {
	d.mu.Lock()
	mount, interval, sampler := d.mount, d.interval, d.samplers[d.mount]
	d.mu.Unlock()

	r, err := metric.Sample(ctx, sampler, metric.Limit(interval), func(ctx context.Context) (reading, error) {
		u, err := disk.UsageWithContext(ctx, mount)
		if err != nil {
			return reading{}, fmt.Errorf("%s: %w", mount, err)
		}
		device, fstype, err := deviceOf(mount)
		if fstype != "" {
			u.Fstype = fstype
		}
		return reading{u, device}, err
	})

	if ctx.Err() != nil {
		// The settings changed, perhaps away from a mount that stopped
		// answering; the poller was woken.
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	// Others' samplers are dropped here, once this sample is no longer
	// about to start, rather than in configure, where a poll may still
	// start one on a sampler dropped, so a second could get stuck on a
	// mount switched back to.
	for m, s := range d.samplers {
		if m != d.mount && !s.Busy() {
			delete(d.samplers, m)
		}
	}
	if mount != d.mount {
		// The settings changed while sampling; the poller was woken and
		// samples the new mount next.
		return 0
	}
	if err != nil {
		if err.Error() != d.lastErr {
			log.Print(err)
		}
		d.lastErr = err.Error()
		d.views <- failed(mount, errors.Is(err, errNotMounted))
	} else {
		d.lastErr = ""
		d.views <- view(mount, r.device, r.usage)
	}
	return d.interval
}

// reading is a sample of the file system: its usage, and the device
// it is on.
type reading struct {
	usage  *disk.UsageStat
	device string
}

// errNotMounted is the error when the mount setting names a directory
// with nothing mounted on it, whose usage would be that of the file
// system it is in.
var errNotMounted = errors.New("not a mount point")

func view(mount, device string, u *disk.UsageStat) sdk.View {
	used := u.UsedPercent
	// Rounded down, so a file system with space left never reads 100.
	display := strconv.FormatFloat(math.Floor(used), 'f', 0, 64)
	fstype := strings.ToValidUTF8(u.Fstype, "\uFFFD")
	detail := strings.Join(slices.DeleteFunc([]string{device, fstype}, func(s string) bool { return s == "" }), " · ")
	// Weighted, so the dial grows into what the detail line leaves
	// rather than taking its room.
	body := sdk.Column(
		sdk.Weighted(1, sdk.Gauge("", used).Unit("%").Display(display).
			WarnAt(warnAt).ErrorAt(errorAt).
			Caption(metric.BytesString(u.Free)+" free of "+metric.BytesString(u.Total))),
	).Gap(sdk.SpacingSmall)
	if detail != "" {
		body.Add(sdk.Text(detail).Size(sdk.SizeSmall).Tone(sdk.ToneMuted).Monospace())
	}
	return sdk.View{Title: title(mount), Body: body}
}

// title names the panel after the mount point it shows, so panels for
// several file systems tell apart.
func title(mount string) string { return "Disk " + mount }

// failed is the view when mount cannot be read, or when nothing is
// mounted there, perhaps yet.
func failed(mount string, notMounted bool) sdk.View {
	text := "Cannot read the file system at " + mount
	if notMounted {
		text = "Nothing is mounted at " + mount
	}
	return sdk.View{
		Title: title(mount),
		Body: sdk.Column(
			sdk.Text(text).Tone(sdk.ToneError),
			sdk.Text("Retrying; the details are in the panel's logs.").Size(sdk.SizeSmall).Tone(sdk.ToneMuted),
		).Gap(sdk.SpacingSmall),
	}
}

// minBody is the least room the panel's view needs, in dp: room for
// the dial at its least size, its caption, and the device line.
var minBody = sdk.BodySize{Width: 120, Height: 144}
