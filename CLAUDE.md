# CLAUDE.md

Guidance for Claude Code (claude.ai/code) when working in this repository.

## What tessera is

`tessera` is a desktop dashboard written in Go. The window is a grid of
panels (tiles), and **every panel is a plugin**: the host ships with no
built-in panels and only handles layout, rendering, configuration, and
plugin lifecycle. Even the first-party panels (clock, system stats, and so
on) are plugins that use the same protocol as third-party ones.

**The content area holds nothing but plugin panels.** That includes
greetings, headings, captions, and explanatory text: if it appears in the
grid, a plugin provides it and the user can disable it. The host owns only
the window header (app name, layout selector, plugin status, edit-layout
toggle, theme toggle) and transient UI (edit-mode controls, toasts,
dialogs). Edit mode must be able to re-enable any disabled panel.

## Decisions

- **License:** GPL-3.0-or-later. `LICENSE` at repo root;
  `SPDX-License-Identifier: GPL-3.0-or-later` as the first line of every
  `.go` and `.proto` file.
- **Version:** starts at `0.1.0`. `./VERSION` is the single source of truth,
  embedded at build time (`//go:embed VERSION`); release tags `vX.Y.Z`
  mirror it.
- **Language / stack:** Go 1.25+, the minimum set by
  `google.golang.org/grpc` v1.84.0 (latest as of 2026-10-15, whose
  `go.mod` declares `go 1.25.0`; `gioui.org` v0.10.3 needs 1.24). The
  local toolchain is go1.27.2 (`/usr/local/go`).
- **GUI:** [Gio](https://gioui.org) (`gioui.org`), immediate mode. Gio needs
  cgo on Linux (Wayland/X11, EGL/Vulkan, xkbcommon), so the host binary is
  **not** `CGO_ENABLED=0`. CI needs the system dev packages, and
  cross-compiling the host is per-OS. Plugins have no GUI dependency and
  stay `CGO_ENABLED=0`.
- **Plugin model:** out-of-process RPC in the style of
  `github.com/hashicorp/go-plugin`. Each panel is its own executable, which
  the host launches and talks to over gRPC. Protocol definitions live in
  `.proto` files, so non-Go panels are possible. Consequences:
  - A panel crash or hang must never take down the host. Use per-call
    deadlines, restart with backoff, and show an error tile in the panel's
    place.
  - Plugins **cannot draw Gio directly**. They return a *declarative view*
    (a small, versioned widget vocabulary such as text, stat/KPI, gauge,
    sparkline, table, and list) and the host renders it. Designing this
    view schema is the central architectural task. Keep it small and
    version it explicitly in the handshake.
  - Data flow: the host subscribes and the plugin pushes view updates over a
    server-streaming RPC. Polling is the fallback. Input events (click,
    scroll) go back to the plugin as RPCs.
  - Plugins are discovered from a plugins directory
    (`$XDG_DATA_HOME/tessera/plugins`, found per OS with
    `github.com/adrg/xdg`), and each declares its name, version,
    protocol version, and config schema in the handshake. The host
    launches them with `github.com/hashicorp/go-plugin` itself.
- **Config:** one YAML file, `tessera/config.yaml` in the user config
  directory (`$XDG_CONFIG_HOME`, or `~/.config`, on Linux), holds the
  theme, the grid, panel placement and enabled state, and per-panel
  settings. YAML was the owner's choice over TOML and JSON: compact for
  a list of panels with nested settings, and it allows comments. It is
  parsed with `github.com/goccy/go-yaml` and decoded strictly so unknown
  fields are errors. goccy resolves some plain scalars by YAML 1.1 rules
  (`017` as octal, `<<` as a merge), so panel settings are taken out of
  its syntax tree and resolved by YAML 1.2's core schema in
  `internal/config/settings.go`, and the host writes the file with its
  own emitter. It is saved atomically (temp file and rename) and watched
  for changes through `fsnotify`. Rewrites by the host drop comments.
  The schema is versioned (`version: 1`); see `docs/config.md`.
- **Module path:** `github.com/joncbenderkh/tessera`.

## Layout

- `version.go`: package `tessera`, which embeds `./VERSION` as
  `tessera.Version`.
- `cmd/tessera`: the host (Gio window, layout, plugin manager).
- `internal/config`: loading, validating, saving, and watching the user
  config.
- `internal/ui`: the host-owned UI drawn with Gio: the window header,
  the grid and its tiles (placement in `Arrange`, the menu, and the
  starting and error bodies), the button, and the toast and dialog
  primitives. `internal/ui/theme`
  holds the design tokens (dark and light palettes, spacing, radius, and
  type scales) taken from `docs/design`.
- `internal/pluginhost`: finding, launching, describing, configuring,
  supervising, and shutting down plugins, one process per enabled
  panel: restarts with backoff, panel states, each panel's logs, its
  latest view (from Watch, or by polling Render), and input events.
  Its tests run the test binary itself as a plugin whose behaviour
  follows its file name (`plugin_test.go`).
- `proto/`: the plugin protocol. The `.proto` files and the Go code
  generated from them live in `proto/tessera/v1` (Go package
  `tesserav1`); `buf.yaml` and `buf.gen.yaml` sit in `proto/`. The
  protocol is described in `docs/protocol.md`.
- `sdk/`: the Go helper library plugin authors import, which hides the
  go-plugin/gRPC boilerplate.
- `plugins/<name>`: first-party panels, each its own `main` package.

## Commands

Use the local toolchain: `export PATH=/usr/local/go/bin:$PATH`.

- Build the host: `go build ./cmd/tessera`; `./tessera --version`.
- Test: `go test ./...`
- Vet: `go vet ./...`
- Format check: `gofmt -l .` (must print nothing).
- Lint: `go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...`
- Regenerate protocol code after editing a `.proto`: `go generate ./proto`.
  It runs `buf` and the protoc plugins through `go run`, so no `protoc`
  install is needed. Generated files are committed.
- Lint the protocol (from `proto/`):
  `go run github.com/bufbuild/buf/cmd/buf@v1.74.0 lint` and
  `go run github.com/bufbuild/buf/cmd/buf@v1.74.0 format -d --exit-code`.
- Check the protocol for breaking changes, with CI's rules, against the
  point where the branch left `origin/main` (from `proto/`, after
  `git fetch`):
  `go run github.com/bufbuild/buf/cmd/buf@v1.74.0 breaking --config '{"version":"v2","breaking":{"use":["FILE"]}}' --against "$(git rev-parse --path-format=absolute --git-common-dir)#ref=$(git merge-base HEAD origin/main),subdir=proto" --against-config '{"version":"v2","modules":[{"path":"proto"}]}'`.
- CI (`.github/workflows/ci.yml`) builds, tests, vets, and checks the
  `CGO_ENABLED=0` build on both the `go.mod` minimum and stable Go. On
  stable only, it also runs the format check, staticcheck, `buf lint`,
  `buf format`, `buf breaking` against the previous `main` (with the
  rules pinned in the workflow), a `go generate` drift check, and an
  SPDX header check. Pull requests must target `main`; CI fails on any
  other base. A separate job runs the `internal/pluginhost`,
  `internal/config`, and `internal/ui` tests on macOS and Windows, on
  both Go versions; the BSD variants of the plugin host's process code
  are only vetted (`GOOS=freebsd go vet` and so on, in the stable Linux
  job), never run. It does not run the host.
- Plugins and `sdk/` must also build with `CGO_ENABLED=0`.

The host needs Gio's system packages. On Fedora: `wayland-devel`,
`libxkbcommon-devel`, `libxkbcommon-x11-devel`, `libX11-devel`,
`libXcursor-devel`, `libXfixes-devel`, `libglvnd-devel`,
`vulkan-loader-devel`. Build with `-tags nox11` or `-tags nowayland` to drop a
backend.

## Releasing

1. Bump `./VERSION` in a PR and merge it.
2. Tag the merge commit on `main` and push the tag:
   `git tag v$(cat VERSION) && git push origin v$(cat VERSION)`.

`.github/workflows/release.yml` fails unless the tag equals `v` + `VERSION`.
It builds the host natively on linux/amd64, linux/arm64, darwin/arm64, and
windows/amd64, cross-builds every `plugins/*` main package with
`CGO_ENABLED=0` for the same targets, and publishes the archives with
`SHA256SUMS` and generated notes as a GitHub release. On PRs that touch
the workflow it runs the builds without publishing. The macOS host is a
bare binary, not a signed `.app` bundle.

## Status

The module and source skeleton exist (#4); `sdk/` and `plugins/` are
placeholders. The backlog is tracked as GitHub issues. CI (#5), the
release workflow (#6), the config file (#7), and plugin protocol v1
(#8) are in place. The host launches, describes, and configures each
enabled panel's plugin and shuts them all down on exit (#12), and
supervises them: crashes and hangs restart with backoff, and each
panel keeps its state and logs (#13). It reads each panel's view, from
the Watch stream with its heartbeat or by polling Render, shows a
polled panel whose Render fails as stale, and passes input events on
(#14). The window has its header (app name, a single "Default" layout,
plugin status, an Edit layout button whose edit mode is #15, and a
theme toggle), dark and light themes saved in the config, and toast
and dialog primitives (#9). The grid lays the enabled panels out,
compacted upward and flowed into fewer columns in narrow windows, as
tiles with a title, the plugin on hover, an options menu whose items
are stubs until #15 and #16, and a body for starting and failed
panels, with Restart and Logs; config changes apply live (#10). Tile
bodies stay empty until views are rendered (#11); the SDK is #18.

## Git workflow

Never commit on or push to `main`; it is branch-protected on GitHub, and
local `pre-commit`/`pre-push` hooks reject it. Each change goes on its own
branch, in a worktree that sits beside `main/`, and lands through a PR.
PRs land as squash or merge commits; rebase merging is off on GitHub,
so `HEAD~1` on `main` is always the previous `main` (CI relies on this).
