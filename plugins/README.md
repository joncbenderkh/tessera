# First-party plugins

Each subdirectory is one panel plugin: its own `main` package, built as a
separate executable with `CGO_ENABLED=0`. They use the same protocol and SDK
as third-party plugins; the host treats them no differently.

Each one's version is tessera's, from `./VERSION`. Their tests run the
test binary as the plugin through `internal/plugintest`.

| Plugin | Shows | Settings |
|---|---|---|
| `clock` | The time to the second, the date, and the time zone | `timezone`: an IANA name such as `Europe/Berlin`, in any case, or `Local` (the default) |
| `greeting` | A heading, a line below it, and the computer's name | `text` and `secondary`, each hidden when set to `""` (left out or empty, as in `text:`, it keeps its default); `host_info` (default `true`) |
| `notes` | A note | `title` and `body`; a body keeps its line breaks |
| `calendar` | The rest of today's events in iCalendar feeds, including those under way; activating one shows its details below the list, which a Hide details item closes | `sources` (required): feeds, each an `http`, `https`, or `webcal` URL, an absolute path, or a path starting with `~/`; `refresh` (`1m`–`24h`, default `15m`); `title` (default `Next events`; empty keeps it) |

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
