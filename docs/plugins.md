# Writing a plugin in Go

Every tessera panel is a plugin: a separate executable the host
launches, one process per panel. The Go SDK,
`github.com/joncbenderkh/tessera/sdk`, hides the transport, so a plugin
is a description, a channel of views, and a few callbacks. Plugins in
other languages follow [the protocol](protocol.md) directly.

`sdk/example/counter` is a complete plugin; this page walks through it.

## A first plugin

```go
package main

import (
	"time"

	"github.com/joncbenderkh/tessera/sdk"
)

func main() {
	views := make(chan sdk.View)
	go func() {
		for t := range time.Tick(time.Second) {
			views <- sdk.View{
				Title: "Clock",
				Body:  sdk.Text(t.Format("15:04:05")).Size(sdk.SizeDisplay),
			}
		}
	}()
	sdk.Serve(sdk.Plugin{
		Name:        "clock",
		Version:     "0.1.0",
		Description: "The time, to the second.",
		Views:       views,
	})
}
```

Build it without cgo, under the name it gives itself, and install it
by renaming it into the plugins directory, so a panel never starts a
half-written file:

```sh
CGO_ENABLED=0 go build -o clock.new .
mv clock.new ~/.local/share/tessera/plugins/clock
```

The plugins directory is `tessera/plugins` under the user data
directory: `$XDG_DATA_HOME` (or `~/.local/share`) on Linux,
`~/Library/Application Support` on macOS, and `%LOCALAPPDATA%` on
Windows, where the file is `clock.exe`. Then add a panel to the config
(see [config.md](config.md)):

```yaml
panels:
  - id: clock
    plugin: clock
    x: 0
    y: 0
    w: 1
    h: 1
```

Run by hand, a plugin prints a short message and exits: only the host
can start it.

## Describing the plugin

`sdk.Plugin` holds what the panel picker and the host need:

- `Name` must equal the executable's file name: 1 to 64 lowercase
  letters, digits, and hyphens, starting with a letter or digit.
- `Version` is the plugin's semver version.
- `Description` is one line for the panel picker.
- `ConfigSchema` is a JSON Schema (draft 2020-12) for the panel's
  settings, if it takes any.
- `DefaultSize` is the size, in grid cells, a new panel gets.

## Views

A view is a title and a tree of widgets. Each builder returns a widget
whose methods set its options and return it, so calls chain:

| Builder | Shows |
|---|---|
| `Text(value)` | A run of text: `.Size`, `.Tone`, `.Monospace` |
| `Stat(label, value)` | A headline number: `.Unit`, `.Caption`, `.CaptionTone` |
| `Gauge(label, value)` | A value on an arc, 0 to 100 unless `.Range`: `.Unit`, `.Display`, `.WarnAt`, `.ErrorAt`, `.Caption` |
| `Sparkline(label, samples...)` | Recent samples, oldest first: `.Range`, `.Unit`, `.Labels`, `.Tone` |
| `Table(TableColumn(title)...)` | Rows and columns: `.Row`, `.ActionRow`, `.Sort`, `.Empty`; columns take `.AlignEnd` and `.Weight` |
| `List(Item(title)...)` | A vertical list: `.Add`, `.Empty`; items take `.Leading`, `.Detail`, `.Action` |
| `Row(children...)`, `Column(children...)` | Children side by side or stacked: `.Gap`, `.Dividers` |

Every widget also takes `.ID` and `.Loading`. In a row or column, a
child takes its natural size unless wrapped in `Weighted(n, child)`,
which gives it a share of the space left; `Space()` is empty space.
Across a row, a sparkline, table, or list needs a weight to sit beside
other children. `Raw` wraps a widget built from the protocol messages
directly.

```go
sdk.Column(
	sdk.Row(
		sdk.Weighted(1, sdk.Stat("CPU", "24.8").Unit("%")),
		sdk.Weighted(1, sdk.Stat("Memory", "6.1").Unit("GB")),
	),
	sdk.Table(sdk.TableColumn("Process").Weight(3), sdk.TableColumn("CPU").AlignEnd()).
		ActionRow("pid:4821", "firefox", "12.0").
		Sort(1, sdk.SortDescending).
		ID("procs"),
)
```

The host owns all styling: a plugin picks a tone or a step on the type
and spacing scales, never a color or a pixel size, and formats its own
numbers. How the host draws each widget is in
[protocol.md](protocol.md#view-schema-v1).

Send each new view on `Plugin.Views` when the content changes; it
replaces the last one. The SDK hands the host only the newest, so a
plugin never waits for the host, and sending views faster than the host
redraws (30 times a second) gains nothing. Do not change a view's
widgets, table columns, or list items after sending it; the builders
copy the slices they are given, so a plugin may reuse its own, such as a
buffer of samples. Until the first view arrives the panel shows as
starting; if none comes within half the heartbeat interval (2.5
seconds), the SDK sends an empty one, as the protocol requires. A plugin
still fetching data sends a view with its widgets marked `.Loading()`
first.

A view is limited to 1024 widgets, nested at most 16 deep, with at most
64 columns per table and 4 MiB encoded, and its strings must be valid
UTF-8. The host would refuse a view over a limit and read no more until
the panel restarted, and one that cannot be encoded would fail the
plugin, so the SDK drops such a view instead and logs why: the panel
keeps showing the last view it accepted.

Keep `Plugin.Views` open while the plugin runs. Closing it fails the
plugin, and the host restarts it.

## Settings

`Configure` is called with the panel's settings and size when the
plugin starts and again whenever they change. `Config.Decode` reads
the settings into a struct as `encoding/json` would, but rejects a
setting the struct has no field for, and matches names exactly: a
setting's name must equal the field's `json` tag, or else its Go name,
so `Step:` in the config does not fill a field tagged `json:"step"`.

```go
func (c *counter) configure(_ context.Context, cfg sdk.Config) error {
	s := settings{Step: 1}
	if err := cfg.Decode(&s); err != nil {
		return err
	}
	if s.Step < 1 {
		return sdk.InvalidSettings("step must be at least 1, not %d", s.Step)
	}
	...
}
```

A `time.Duration` field takes a number of nanoseconds, as in
`encoding/json`; for a setting such as `5s`, decode a string and call
`time.ParseDuration`. `Decode` may fill part of the struct before it
finds an error, so decode into a fresh value, as above, not the plugin's
live settings. An error from `Decode` or `InvalidSettings` rejects the
settings: the panel shows the message until the user changes them. Any
other error fails the plugin, which the host restarts, so a plugin whose
data source is down accepts its settings and shows the outage in its
view.

## Input events

`Activate` is called with the action ID of a table row (`.ActionRow`)
or list item (`.Action`) the user clicks, taps, or presses Enter on.
Action IDs are unique within a view. `Scroll` is called when the
visible rows of a table or list with an ID change, so a plugin can
send more rows as the user nears the end. Show the effect of an event
by sending a new view. An error from either is logged in the panel's
Logs.

## Concurrency and deadlines

`Configure`, `Activate`, and `Scroll` run concurrently with whatever
goroutine sends views, so guard the state they share, as
`sdk/example/counter` does with a mutex. The host calls them one at a
time, in order, with a 5-second deadline; a call that misses it restarts
the plugin. Do slow work in the background and send a view when it is
done.

A plugin that crashes or hangs is restarted with backoff, and after
five failures in a row its panel shows as crashed until the user
restarts it.

## Logs

Anything a plugin writes to stderr goes to its panel's Logs, which
keep the last 500 lines. So does what it writes to stdout once
`sdk.Serve` has started, but not before: until then stdout carries the
handshake, and a line of anything else there fails the launch. Log to
stderr, as the `log` package does by default.

## Testing

`sdk/example/counter/main_test.go` tests a plugin end to end: started by
the host, the test binary serves the plugin, and the test launches a
copy of itself through `internal/pluginhost` as the host does. That
package is internal, so this works only inside the tessera module.
Elsewhere, test the plugin's own logic, and check views with
`View.Proto`.
