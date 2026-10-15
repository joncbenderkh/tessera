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
   the value in `proto/tessera/v1/version.go`, and
   `PLUGIN_PROTOCOL_VERSIONS` listing the protocol versions it supports.
   A plugin started without the cookie was run by hand: it prints a short
   message and exits.
2. The plugin listens on a local address and prints one line to stdout:
   `1|<protocol version>|tcp|127.0.0.1:<port>|grpc` (or `unix|<path>`).
3. The host connects and makes `PanelService` calls. As go-plugin
   requires, the plugin also serves the standard gRPC health service
   (`grpc.health.v1.Health`) and reports the service name `plugin` as
   serving. The plugin's stderr goes to the host's log for that panel.

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
package (`tessera.v2.View`, say) carried in a new field next to `view` in
`WatchResponse` and `RenderResponse`. The plugin sets only the field for
the version it picked in `Describe`.

## Lifecycle

The host launches **one plugin process per panel** in the config, so two
clocks with different time zones are two processes.

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
  change. A watching plugin then sends a fresh view on its open stream.
- **Watch** is the normal path: the plugin pushes a complete view each
  time its content changes, starting with the current one. When nothing
  changes for `heartbeat_interval`, it sends a `WatchResponse` without a
  view, so a silent stream means a hung plugin. The host holds one stream
  per plugin; a new Watch supersedes the open one, which the plugin
  should end.
- **Render** is the fallback for plugins that cannot push. If Watch
  returns `UNIMPLEMENTED`, the host polls Render, waiting
  `RenderResponse.poll_interval` (or its default) between calls.
- **HandleEvent** delivers clicks and scrolls. A polled plugin is rendered
  again right after the event.

Every view is complete: it replaces the previous one rather than patching
it. Views are small, and this keeps plugins stateless about what the host
has drawn.

The host puts a deadline on every unary call, treats a Watch stream that
stays silent for twice the heartbeat interval as hung, and restarts a
plugin that crashes, hangs, or breaks the protocol, with backoff, showing
an error tile meanwhile (#13).

A plugin's `DescribeResponse.name` must equal its executable's file name
without extension, which is how the config's `plugin:` field finds it.

## Settings

`DescribeResponse.config_schema` is a JSON Schema (draft 2020-12) for the
panel's `settings` block in the config file. The host passes the settings
to `Configure` as a `google.protobuf.Struct`, which has the JSON data
model: mappings become structs, sequences lists, and every number a
double, so integers beyond ±2^53 lose precision (quote them as strings).
Dates arrive as strings as written, and `!!binary` values as base64
strings. Checking settings against the schema, and building a form from
it, is #16; until then, the plugin validates them and answers
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

The vocabulary is deliberately small. `docs/design/widget-vocabulary.html`
shows each widget as the renderer should draw it. A new widget or field is
a schema change: add it to `view.proto` with a comment, and to that
mockup.

## Input events

| Event | Sent when | Fields |
|---|---|---|
| `ActivateEvent` | The user clicks, taps, or presses Enter on a table row or list item that has an `action_id` | `action_id` |
| `ScrollEvent` | The visible range of a table or list with an `id` changes | `widget_id`, `first_visible`, `visible_count` |

Rows and items without an `action_id` are not interactive. Hover and
selection highlighting are drawn by the host and never reach the plugin.
