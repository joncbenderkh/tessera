# Plugin protocol v1

Every tessera panel is a plugin: a separate executable that the host
launches and talks to over gRPC. Plugins never draw. They describe their
content as a tree of widgets, and the host renders it in its own visual
language. Plugins can therefore be written in any language with gRPC
support, and a crashed or hung plugin cannot take the window down.

The definitions are in `proto/tessera/v1/`:

- `panel.proto`: the `PanelService` RPCs, the handshake, and input events.
- `view.proto`: the view schema, the widget vocabulary.

Every message, field, and enum value carries a comment there, which `buf
lint` enforces. That comment is the field reference; this page covers how
the pieces fit together.

## Transport

The transport follows [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin)'s
gRPC protocol, so a plugin built with go-plugin works unchanged and other
languages can follow its documented handshake:

1. The host starts the plugin with `TESSERA_PLUGIN_MAGIC_COOKIE` set to
   the value in `proto/tessera/v1/version.go`,
   `PLUGIN_PROTOCOL_VERSIONS` listing the protocol versions it supports,
   and `PLUGIN_CLIENT_CERT` holding a fresh PEM certificate. It also
   sets `PLUGIN_MIN_PORT` and `PLUGIN_MAX_PORT`, which a plugin uses only
   on Windows, as the range its TCP port must fall in. A plugin started
   without the cookie was run by hand: it prints a short message and
   exits.
2. The plugin listens on a unix socket (on Windows, TCP on 127.0.0.1),
   generates its own certificate, and prints one line to stdout:
   `1|<protocol version>|<network>|<address>|grpc|<certificate>`, where
   `<network>|<address>` is `unix|<socket path>` (on Windows,
   `tcp|127.0.0.1:<port>`) and the certificate is its DER encoding in
   unpadded standard base64.
3. The host connects over mutual TLS, each side trusting only the other's
   certificate, and makes `PanelService` calls. The host verifies the
   plugin's certificate for the name `localhost`, so it needs that DNS
   name and the server and client authentication key usages; the plugin
   requires a client certificate and verifies it against
   `PLUGIN_CLIENT_CERT`. As go-plugin requires, the plugin also serves
   the standard gRPC health service (`grpc.health.v1.Health`), reporting
   the service name `plugin` as serving, and go-plugin's
   `plugin.GRPCController`, whose `Shutdown` makes the plugin exit; a
   plugin still running 2 seconds later is killed, as is one that
   leaves `Shutdown` unanswered for 3 seconds. go-plugin's `GRPCBroker`
   and `GRPCStdio` services are optional. The plugin's stderr, and any
   output it forwards over `GRPCStdio`, go to the host's log for that
   panel.

A plugin's stdin is empty, and its stdout carries only the handshake
line: anything written there later is discarded. An output line longer
than 64 KiB is logged in pieces.

On Unix each plugin runs in a process group of its own. On Linux,
macOS, and the BSDs the host kills that group as soon as the plugin
exits, and when it shuts the plugin down, so children that stay in the
group cannot outlive the plugin or keep the host from exiting by
holding the plugin's stderr open. A child that leaves the group is
on its own, though the host still sees the plugin exit; if it holds
the plugin's stdout or stderr open, the host reads them for up to 3
more seconds and then closes them. On other Unix systems the group is
killed only when a plugin has to be killed. On Windows only the plugin
itself is killed. All of this holds while the host runs: on Linux the
kernel also kills a plugin whose host dies without shutting it down,
but not the plugin's children; elsewhere such a plugin outlives its
host.

Mutual TLS is what go-plugin calls AutoMTLS, and the host always enables
it: without it, any local process could connect to a plugin, read its
view, and trigger its actions. That matters most for TCP, which every
local user can reach, but it covers unix sockets as well.

The host side is `internal/pluginhost`; the Go SDK in `sdk/` hides
all of this from a plugin (see [plugins.md](plugins.md)).

## Versions

Two versions are negotiated, and they change independently:

| Version | Covers | Negotiated in |
|---|---|---|
| Protocol (`ProtocolVersion`, now 1) | The `PanelService` RPCs and their messages | The transport handshake; repeated in `DescribeResponse.protocol_version` |
| View schema (`ViewVersion`, now 1) | The messages in `view.proto` | `DescribeRequest.view_versions` (host offers) and `DescribeResponse.view_version` (plugin picks) |

Within a version, changes are additive only: new fields and enum values
may appear, nothing is renamed, renumbered, or repurposed. Hosts render
unknown enum values as their default and ignore unknown fields. Anything
else needs a new version. CI checks this with `buf breaking` (`FILE`
rules) on every pull request and every push to `main`, against the
previous `main`. The rules and the module are pinned in the workflow
for both sides of the comparison, so editing `proto/buf.yaml` does not
relax them. Pull requests need not be up to date with `main` to merge,
so two that pass separately are checked together only by the push to
`main` after the second merges. A failed check on `main` must be reverted
or fixed before the next merge, since later checks compare against it
and would accept the break.
An intended break is a new package version (`tessera.v2`); a released
version is never changed incompatibly.

Since a field's type cannot change, a future view schema version is a new
package (`tessera.v2.View`, say) carried in a new field of the `content`
oneof in `WatchResponse` and `RenderResponse`, beside `view`. The oneof
makes the plugin set only the field for the version it picked in
`Describe`.

## Lifecycle

The host launches **one plugin process per panel** in the config, so two
clocks with different time zones are two processes. To fill the panel
picker, it may also launch a plugin that has no panel, call only
`Describe`, and shut it down.

```
host                                    plugin
 │── Describe(host version, [1]) ───────▶│  name, version, view_version,
 │◀──────────────────────────────────────│  config_schema, default_size, min_body
 │── Configure(panel id, settings, ─────▶│  INVALID_ARGUMENT if rejected
 │     size, shown size)                 │
 │── Watch(heartbeat interval) ─────────▶│
 │◀──────── view ── view ── heartbeat ───│  first view right away
 │── HandleEvent(activate "pid:4821") ──▶│  effect arrives as a new view
```

- **Configure** is called again whenever the panel's settings or size
  change. `size` is the panel's `w` and `h` in the config, which a
  narrow window's layout does not change; `shown_size` is the size the
  panel is shown at, as wide as `size` or as the window's columns,
  whichever is less, and it changes as the window is resized across
  a column count. That configures a live plugin again, but, unlike a
  change to the config, restarts no panel that failed or whose views
  went over a limit. A plugin should lay its view out for `shown_size`;
  a host from before it leaves it unset. A watching plugin then sends a
  fresh view on its open stream, and a polled plugin is rendered again
  right away. If the plugin answers `INVALID_ARGUMENT`, the host shuts
  it down and shows the panel as crashed, with the message, until its
  settings or size change again or the user restarts it; the plugin need
  not keep its previous settings. Configure should not depend on
  anything outside the plugin: one that fails for another reason fails
  the plugin, which is restarted with backoff, and five failed launches
  in a row leave the panel crashed. A plugin whose
  data source is down (no network at login, say) answers Configure and
  shows the outage in its view, retrying on its own.
- **Watch** is the normal path: the plugin pushes a complete view each
  time its content changes, starting with the current one. The first
  response always carries a view: a plugin still fetching data sends
  widgets marked `loading`. When nothing changes for
  `heartbeat_interval` (the host asks for 5 seconds), it sends a
  `WatchResponse` without a view, so a silent stream means a hung
  plugin. The host reads one stream per plugin and cancels it before
  opening another, but the cancelled handler may still be returning when
  the next starts, so keep per-stream state inside the handler.
- **Render** is the fallback for plugins that cannot push. If Watch
  returns `UNIMPLEMENTED` before its first response, the host polls
  Render, waiting `RenderResponse.poll_interval` between calls (5
  seconds if unset, and kept between 250 milliseconds and an hour). A
  Render the plugin answers with an error leaves its last view in
  place: the panel is shown as stale, with the view's age and the
  error, and polling goes on until a Render succeeds. A plugin must
  implement one of the two; one that answers both with `UNIMPLEMENTED`
  is not restarted.
- **HandleEvent** delivers clicks and scrolls. A polled plugin is rendered
  again right after the event. An event the plugin answers with an
  error is logged, and the plugin keeps running.

Configure and HandleEvent run concurrently with the open Watch, so a
plugin guards the state they share. The host makes these unary calls one
at a time and in order, so events never overtake each other, with one
exception: a scroll event still waiting takes the newest range for its
widget, which can then arrive before events queued after it. A call that
misses its deadline counts as a hang: the host restarts the plugin
before making another call, so a timed-out handler never overlaps the
next one. A Watch stream that ends without the host cancelling it, other
than with `UNIMPLEMENTED` before its first response or a view over a
limit, is a protocol failure.

Every view is complete: it replaces the previous one rather than
patching it. Views are small, and this keeps plugins stateless about
what the host has drawn. Each view has these limits:

- A response is at most 4 MiB encoded, gRPC's default receive limit.
- Widgets nest at most 16 deep: `View.body` is depth 1, and each
  `StackItem.widget` adds 1.
- A view holds at most 1024 widgets: `View.body` and every
  `StackItem.widget`. Table rows and list items are not widgets.
- A table has at most 64 columns.

A view over any of them fails its stream or Render call; the host
shows an error tile naming the limit, does not restart the plugin, and
reads no more views from it until it is restarted, by the user or by a
change to the panel's settings or size. Until then only the health
check and the deadline on each event show that it still answers, so a
hang inside its own code goes unseen. A response over 16 MiB is not read at all, and counts as an
error the plugin returned. The host keeps only each panel's latest view
and redraws at most 30 times a second, so sending views faster gains a
plugin nothing.

The host puts a deadline on every unary call (5 seconds), treats a
Watch stream that stays silent for twice the heartbeat interval as hung,
and restarts a plugin that crashes, hangs, or breaks the protocol:

- A plugin whose process exits, that misses a call's deadline, or that
  fails the health check the host makes every 10 seconds while it is
  live (the health service above, for the service name `plugin`, also
  with a 5-second deadline; any status but `SERVING` fails it), is shut
  down and launched again after 1 second, then 2, 4, and 8 after each
  further failure in a row. A launch that fails, short of the
  permanent failures below, counts the same way. The next launch, after
  a failure or a restart, waits until the old process is gone, or until
  shutting it down gives up (about 6 seconds; a process stuck in the
  kernel can outlive SIGKILL), which the panel's Logs then say. On
  Windows, children the plugin started are not shut down with it.
- The health check catches a plugin frozen as a whole. go-plugin
  answers it outside the plugin's own code, so a plugin hung inside
  that code still passes; the Watch heartbeat catches those, as does
  the deadline on a Render or an event.
- At the fifth failure in a row the host gives up and shows the panel
  as crashed, with the last error and, for a plugin that exited by
  itself, its exit status, until the user restarts it or its settings
  or size change. A change to them also cuts a wait between restarts
  short.
- A plugin that stays up for a minute starts its count again. It
  counts as up only until the last health check it passed, so one
  that freezes is not counted as up while frozen.
- Failures a restart cannot fix are not retried: a missing,
  unreadable, non-executable, or misnamed plugin, one the system
  cannot run (not a program for it, a script whose interpreter is
  missing, or a file on a `noexec` mount), a version the host does
  not speak, a `Describe` or `Configure` answered `UNIMPLEMENTED`
  (not a tessera plugin), `UNIMPLEMENTED` from both Watch and Render,
  and settings the plugin rejects with `INVALID_ARGUMENT`.

For the panel's Logs view, the host keeps each panel's last 500
lines, plugin output and lifecycle events (started, exited with
status, stopping with the reason, restart scheduled, gave up) in one
list, each line cut to 4 KiB. An event that spans lines, such as an
error a plugin returned, continues on lines that start with `| `.

Plugins are installed in `tessera/plugins` under the user data
directory: `$XDG_DATA_HOME` (or `~/.local/share`) on Linux,
`~/Library/Application Support` on macOS, and `%LOCALAPPDATA%` on
Windows. A plugin's `DescribeResponse.name` must equal its executable's
file name, which is how the config's `plugin:` field finds it: the name
itself on Unix, the name plus `.exe` on Windows. Other files in the
plugins directory, such as `clock.py` next to `clock`, are ignored.
Install or update a plugin by renaming it into place (write it as
`clock.new` in the plugins directory, then rename it to `clock`): a
panel that restarts while its plugin is missing or half-written fails
for good, as above, until the user restarts it or its settings or size
change. On Windows a launch
denied access, to the file or to running it, is retried, since an
antivirus scan of a new `.exe` can deny it for a moment.

The host refuses a plugin, shutting it down and recording the reason,
when its file is missing or not executable, the handshake or the
mutual TLS connection fails, it does not answer `Describe` within the
call deadline, its `Describe` gives a different name or protocol
version, or it picks a view schema version the host did not offer; a
rejected `Configure` is recorded the same way. Of these, a failed
handshake (other than one on a protocol version the host does not
speak) or connection and a `Describe` that does not answer, or
answers with an error other than `UNIMPLEMENTED`, are retried as
above; the rest are the permanent failures. The reason is logged and
shown in the panel's place, in its error tile.
Names are 1 to 64 lowercase letters, digits, and hyphens, starting with a
letter or digit, so they can never name a path outside the plugins
directory. Names Windows reserves for devices (`con`, `prn`, `aux`,
`nul`, `com0` to `com9`, `lpt0` to `lpt9`) are not allowed.

## Settings

`DescribeResponse.config_schema` is a JSON Schema (draft 2020-12) for
the panel's `settings` block in the config file. The host passes the
settings to `Configure` as a `google.protobuf.Struct`, which has the
JSON data model: mappings become structs, sequences lists, and every
number a double, so integers beyond ±2^53 lose precision (quote them as
strings). How the host reads YAML values into settings is in
`docs/config.md`. Checking settings against the schema, and building a
form from it, is #16; until then, the plugin validates them and answers
`INVALID_ARGUMENT` with a readable message.

## View schema v1

A `View` has a `title`, drawn by the host in the tile header, and a `body`
widget. A `Widget` has an optional `id`, a `loading` flag that draws a
placeholder in the widget's shape, and exactly one kind:

| Widget | Shows | Key fields |
|---|---|---|
| `Text` | A run of text | `value`, `size` (small, body, large, display), `tone`, `monospace` |
| `Stat` | A headline number | `label`, `value` (formatted), `unit`, `caption` |
| `Gauge` | A value on a 260° arc | `value`, `min` (default 0), `max` (default 100), `unit`, `warn_at`, `error_at` |
| `Sparkline` | Recent samples as a line | `samples` (oldest first), optional `min`/`max`, axis labels |
| `Table` | Rows and columns | `columns`, `rows` (each with an optional `action_id`), sort indicator |
| `List` | A vertical list | `items` with `leading`, `title`, `detail`, `action_id` |
| `Stack` | Children in a row or column | `axis`, `gap`, `dividers`, `items` with a `weight` |

`Stack` is the only layout widget; nest stacks to build a grid. The host
owns all styling: plugins choose a `Tone` (muted, accent, ok, warn, error)
or a step on the type and spacing scales, never a color, font, or pixel
size. Numbers are formatted by the plugin, which knows its units and
locale.

How the host draws a view:
- A view taller than its tile is cut off at the tile's edge, except
  tables and lists, which scroll within the space they get. Where text
  runs past the bottom, a widget gets no room in a stack, or a gauge's
  smallest dial does not fit, that widget, or the stack whose items run
  out of room, fades out into the tile where it is cut (at its bottom,
  or the right end of a horizontal stack), so the cut shows. A plugin
  avoids it with `min_body` in `DescribeResponse`: the host scales it
  with the user's text size, as sp but never below its size in dp,
  turns it into cells for the row height and column width at the
  time, grows a panel the user adds to at least that once its plugin
  has described itself, and stops edit mode's resizing there. A panel
  the config makes smaller is shown at its size, faded. A table or
  list with an `id` keeps its scroll position across views and sends a
  `ScrollEvent` whenever its visible range changes, including the
  first time a plugin's view draws it and when it is empty. Visible
  means inside the space the widget gets in its tile, even where the
  window hides that part of the tile. One with
  no `id`, or one whose `id` an earlier widget took, sends none and
  keeps its position by its place among the others like it. A scroll
  event still waiting for a busy plugin is replaced by the newer range
  for the same widget, so the plugin gets the latest one; one the queue
  has no room for is sent again shortly while its tile shows, or when
  it shows again. An activation the host cannot
  queue gives the highlight back to the last row or item activated
  before it that the host did queue; one the queue has no room for
  also shows a "Panel busy" toast. One queued that then fails at the
  plugin keeps its highlight.
- A loading widget is a static placeholder in its shape. A loading
  `Stack` keeps its layout and draws each widget in it as a
  placeholder.
- A gauge whose `max - min` is too large for a double is drawn as
  missing, and a sparkline whose range is too large draws its line at
  mid-height.
- A stat narrower than 200 dp uses a smaller value size. Stats in a
  row should have equal weights: each item without a weight gets only
  what the items without one before it left, so a row of them can mix
  sizes. A stat's or gauge's caption with no tone is muted.
- In a horizontal `Stack`, a gauge without a weight is as wide as its
  dial, but a sparkline, table or list without one takes all the
  width left, and the items after it get none. Give those a weight to
  put them beside other items.
- Views are laid out left to right in every locale: a horizontal
  `Stack` places its first child on the left, table columns run from
  the left, `ALIGN_UNSPECIFIED` is left-aligned and `ALIGN_END`
  right-aligned. Text within a widget still follows its own
  direction.
- Digits in stat and gauge values and in end-aligned table cells are
  equally wide, so a changing value keeps its width.
- The row or item last activated stays highlighted, by its
  `action_id`, until another one is activated; the plugin never sees
  this.

The vocabulary is deliberately small. `docs/design/widget-vocabulary.html`
shows each widget as the renderer should draw it. A new widget or field is
a schema change: add it to `view.proto` with a comment, and to that
mockup.

## Input events

| Event | Sent when | Fields |
|---|---|---|
| `ActivateEvent` | The user clicks, taps, or presses Enter on a table row or list item that has an `action_id` | `action_id` |
| `ScrollEvent` | The visible range of a table or list with an `id` changes | `widget_id`, `first_visible`, `visible_count` |

Rows and items without an `action_id` are not interactive. The event
carries only the `action_id`, so a view never reuses one; if it does,
the first row or item in depth-first order keeps it and later ones are
not interactive. Hover and
selection highlighting are drawn by the host and never reach the plugin.
