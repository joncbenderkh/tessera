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
   plugin still running 2 seconds later is killed. go-plugin's
   `GRPCBroker` and `GRPCStdio` services are optional. The plugin's
   stderr goes to the host's log for that panel.

Mutual TLS is what go-plugin calls AutoMTLS, and the host always enables
it: without it, any local process could connect to a plugin, read its
view, and trigger its actions. That matters most for TCP, which every
local user can reach, but it covers unix sockets as well.

The host-side launcher is #12; the Go SDK that hides all of this is #18.

## Versions

Two versions are negotiated, and they change independently:

| Version | Covers | Negotiated in |
|---|---|---|
| Protocol (`ProtocolVersion`, now 1) | The `PanelService` RPCs and their messages | The transport handshake; repeated in `DescribeResponse.protocol_version` |
| View schema (`ViewVersion`, now 1) | The messages in `view.proto` | `DescribeRequest.view_versions` (host offers) and `DescribeResponse.view_version` (plugin picks) |

Within a version, changes are additive only: new fields and enum values
may appear, nothing is renamed, renumbered, or repurposed. Hosts render
unknown enum values as their default and ignore unknown fields. Anything
else needs a new version. `buf breaking` (`FILE` rules) is configured in
`proto/buf.yaml` for checking this; CI does not run it yet.

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
 │◀──────────────────────────────────────│  config_schema, default_size
 │── Configure(panel id, settings, size)▶│  INVALID_ARGUMENT if rejected
 │── Watch(heartbeat interval) ─────────▶│
 │◀──────── view ── view ── heartbeat ───│  first view right away
 │── HandleEvent(activate "pid:4821") ──▶│  effect arrives as a new view
```

- **Configure** is called again whenever the panel's settings or size
  change. A watching plugin then sends a fresh view on its open stream,
  and a polled plugin is rendered again right away. If the plugin
  answers `INVALID_ARGUMENT`, it keeps its previous settings, and the
  host shows the message in an error tile and cancels any open Watch or
  stops polling; until a Configure succeeds, it makes no Watch or Render
  call.
- **Watch** is the normal path: the plugin pushes a complete view each
  time its content changes, starting with the current one. The first
  response always carries a view: a plugin still fetching data sends
  widgets marked `loading`. When nothing
  changes for `heartbeat_interval`, it sends a `WatchResponse` without a
  view, so a silent stream means a hung plugin. The host reads one
  stream per plugin and cancels it before opening another, but the
  cancelled handler may still be returning when the next starts, so keep
  per-stream state inside the handler.
- **Render** is the fallback for plugins that cannot push. If Watch
  returns `UNIMPLEMENTED` before its first response, the host polls
  Render, waiting `RenderResponse.poll_interval` (or its default)
  between calls.
- **HandleEvent** delivers clicks and scrolls. A polled plugin is rendered
  again right after the event.

Configure and HandleEvent run concurrently with the open Watch, so a
plugin guards the state they share. The host makes these unary calls one
at a time and in order, so events never overtake each other. A call that
misses its deadline counts as a hang: the host restarts the plugin
before making another call, so a timed-out handler never overlaps the
next one. A Watch stream that ends without the host cancelling it, other
than with `UNIMPLEMENTED` before its first response or a view over a
limit, is a protocol failure.

Every view is complete: it replaces the previous one rather than patching
it. Views are small, and this keeps plugins stateless about what the host
has drawn. A response is at most 4 MiB encoded, gRPC's default receive
limit, and widgets nest at most 16 deep (`View.body` is depth 1, and
each `StackItem.widget` adds 1). A view over either limit fails
its stream or Render call; the host shows an error tile naming the limit,
does not restart the plugin, and tries again after the next Configure.

The host puts a deadline on every unary call, treats a Watch stream that
stays silent for twice the heartbeat interval as hung, and restarts a
plugin that crashes, hangs, or breaks the protocol, with backoff, showing
an error tile meanwhile (#13).

A plugin's `DescribeResponse.name` must equal its executable's file name,
which is how the config's `plugin:` field finds it: the name itself on
Unix, the name plus `.exe` on Windows. Other files in the plugins
directory, such as `clock.py` next to `clock`, are ignored.
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
strings). The host resolves settings by YAML 1.2's core schema: `017` is
17, `1e5` is a number, `1_000`, `0b101`, `yes`, and dates are strings,
and `<<` is an ordinary key. When it loads the config, it rejects tags
other than `!!str`, aliases anywhere in the file, `.inf` and `.nan`, and
integers beyond 64 bits or floats beyond a double. Checking settings
against the schema, and building a form from it, is #16; until then, the
plugin validates them and answers `INVALID_ARGUMENT` with a readable
message.

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
