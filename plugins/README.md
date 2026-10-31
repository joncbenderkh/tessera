# First-party plugins

Each subdirectory is one panel plugin: its own `main` package, built as a
separate executable with `CGO_ENABLED=0`. They use the same protocol and SDK
as third-party plugins; the host treats them no differently.

Each one's version is tessera's, from `./VERSION`. Their tests run the
test binary as the plugin through `internal/plugintest`. The system
panels (`system`, `disk`, `network`, and `processes`) run on Linux
only: they read the system through `/proc` and
`github.com/shirou/gopsutil/v4`, which needs no cgo, and share
`internal/metric`: the `interval` setting, sample history, polling,
and units.

`go run ./cmd/build --no-host`, from the repository root, builds them all
and installs them into the plugins directory; without `--no-host` it
builds the host as well.

| Plugin | Shows | Settings |
|---|---|---|
| `clock` | The time to the second, the date, and the time zone | `timezone`: an IANA name such as `Europe/Berlin`, in any case, or `Local` (the default) |
| `greeting` | A heading, a line below it, and the computer's name | `text` and `secondary`, each hidden when set to `""` (left out or empty, as in `text:`, it keeps its default); `host_info` (default `true`) |
| `notes` | A note | `title` and `body`; a body keeps its line breaks |
| `calendar` | The rest of today's events in iCalendar feeds, including those under way; activating one shows its details below the list, which a Hide details item closes | `sources` (required): feeds, each an `http`, `https`, or `webcal` URL, an absolute path, or a path starting with `~/`; `refresh` (`1m`–`24h`, default `15m`); `title` (default `Next events`; empty keeps it) |
| `worldclock` | Up to four clocks to the minute, each with its label, its time zone's abbreviation, how far it is ahead of or behind local time, and its day when that is not today's | `clocks`: a list of 1 to 4, each with a `timezone` (as for `clock`; `Local` if left out) and a `label`, on one line (if left out, the city in the zone's name, or the UTC offset for a numbered `Etc/GMT` zone such as `Etc/GMT+5`, or `GMT` at zero); one local clock if `clocks` is left out or empty (`clocks:`), while `clocks: []` is rejected. `utc_offsets` (default `false`) names a zone with no letter abbreviation by its UTC offset, such as `UTC+7`, on a clock that is not compact (below) |
| `system` | CPU utilization and memory in use, each with a sparkline of the last 60 samples | `interval` (default `2s`) |
| `disk` | How full a file system is, on a gauge (warning from 80%, error from 95%), with the space free, its device, and its file system type | `mount` (default `/`); `interval` (default `30s`) |
| `network` | Download and upload rates, in bits per second, each with a sparkline | `interface` (default: the physical interfaces added up, or every one but loopback if there is none); `interval` (default `2s`) |
| `processes` | A table of processes; activating a row shows the process's details below the table, and activating it again hides them, though the row stays highlighted until another is activated | `sort`: `cpu` (default), `memory`, `pid`, or `name`; `rows`, 1 to 100 (default 10); `interval` (default `2s`) |

`interval` is a duration from `1s` to `1h`, such as `2s` or `1m`.
Changing it, or what a panel shows, clears its sparklines. A process's
CPU is its share of all the CPUs, as in the `system` panel, so the
column adds up to about 100% at most (each process is read at a
slightly different moment); a process new since the last sample
shows its CPU as —. The kernel keeps 15 bytes of a process's name;
a longer one is its executable's, which may not be read of another
user's processes, whose names stay cut short, as systemd-journal for
systemd-journald. Sizes are in binary units (KiB, MiB, GiB). A
missing mount point or interface shows in the panel, which keeps
trying, since it may appear later; so does a directory with nothing
mounted on it. A sample that takes longer than twice the interval
(from 5 to 30 seconds), as on a network mount that stopped answering,
shows as an error, and so do a process's details that take as long. A
selected process that exits shows so once, and its details then
close.

Text settings are strings: quote one that would read as a number or
boolean, such as `title: "2026"`, or the panel rejects it.

```yaml
panels:
  - id: notes
    plugin: notes
    x: 0
    y: 1
    w: 2
    h: 1
    settings:
      title: Before Friday
      body: |
        Renew the certificate.
        Book the review.
  - id: offices
    plugin: worldclock
    x: 2
    y: 1
    w: 1
    h: 1
    settings:
      clocks:
        - timezone: America/New_York
          label: New York
        - timezone: Asia/Tokyo
        - timezone: UTC
          label: Servers
```

## calendar

The calendar reads iCalendar (`.ics`) feeds, such as the secret iCal
address of a Google, Outlook, Fastmail, or Nextcloud calendar, or a file
another program keeps up to date. It never writes to them. It lists the
rest of today's events from all its feeds together, those under way
included, and drops each as it ends; a feed that fails to refresh keeps
its last events and shows the error, naming only the URL's host, since
calendar links carry a token.

iCalendar feeds were chosen over CalDAV (#21): every major calendar
service publishes one, reading it needs no password in `config.yaml`,
and the panel only shows events. CalDAV would add credentials and a
server dependency for writes the panel does not make.

A `webcal://` link is read over https, never plain http, since the
link's token would otherwise cross the network in the clear.

```yaml
  - id: calendar
    plugin: calendar
    x: 3
    y: 0
    w: 1
    h: 2
    settings:
      sources:
        - https://calendar.google.com/calendar/ical/.../basic.ics
        - ~/calendars/family.ics
      title: Today
```

Repeating events (`RRULE`, `RDATE`, `EXDATE`, and changed or cancelled
occurrences) are expanded, but a repeating event is followed for at most
1,000 occurrences from today, and is left out if more than 1,000,000
of its occurrences have already ended (an hourly event that began over a
century ago). A rule repeating more often than hourly, one with `BYSETPOS` on an
hourly or daily frequency, or one the panel cannot read, is ignored, so
its event shows only its start and `RDATE`s. A change made
to an occurrence "and all following" (`RANGE=THISANDFUTURE`) applies
only to that occurrence. Times are shown in the system's time zone. Time
zones are read by their IANA names, or by their Windows names as Outlook
and Exchange write them (mapped through CLDR's table, which `go test
./plugins/calendar -run TestWindowsZones -update` refreshes); a time in
a zone known by neither is read as local time.

## worldclock

The clocks share one list of time zone names, in
`plugins/internal/zones`, so a setting means the same on every system.
The world clock lays its clocks out in rows to suit the panel's shape:
four go two by two in a one-cell panel, side by side in a wide one, and
one above another in a tall one. A clock is compact, with a smaller time
and a short line such as `+1 d · +13 h` (the local clock's time zone,
such as `CEST`), when it has less than a cell to itself or shares a row
with two or more others, since a narrow window squeezes a wide panel
into one column. So at the default `row_height` and text size four
clocks fit a one-cell panel in any column, and a panel one row tall in a
narrow window. With larger text, give them a taller panel. A label too
wide for one line, such as the default `Santo Domingo`, can push the
last line out of a column narrower than 220 dp, which grids of 5 or more
columns have at some window widths.
