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
- **Language / stack:** Go 1.24+, the minimum set by `gioui.org` v0.10.3
  (latest as of 2026-10-14, whose `go.mod` declares `go 1.24.0`). The
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
    (`$XDG_DATA_HOME/tessera/plugins`), and each declares its name, version,
    protocol version, and config schema in the handshake.
- **Config:** a single user config file under `$XDG_CONFIG_HOME/tessera/`
  holds grid layout, panel placement, and per-panel settings. Its format is
  undecided; pick it at scaffold time.
- **Module path:** `github.com/joncbenderkh/tessera`.

## Layout

- `version.go`: package `tessera`, which embeds `./VERSION` as
  `tessera.Version`.
- `cmd/tessera`: the host (Gio window, layout, plugin manager).
- `proto/`: the plugin protocol (`.proto`) and generated Go code.
- `sdk/`: the Go helper library plugin authors import, which hides the
  go-plugin/gRPC boilerplate.
- `plugins/<name>`: first-party panels, each its own `main` package.

## Commands

Use the local toolchain: `export PATH=/usr/local/go/bin:$PATH`.

- Build the host: `go build ./cmd/tessera`; `./tessera --version`.
- Test: `go test ./...`
- Vet: `go vet ./...`
- Format check: `gofmt -l .` (must print nothing).
- Plugins and `sdk/` must also build with `CGO_ENABLED=0`.

The host needs Gio's system packages. On Fedora: `wayland-devel`,
`libxkbcommon-devel`, `libxkbcommon-x11-devel`, `libX11-devel`,
`libXcursor-devel`, `libXfixes-devel`, `libglvnd-devel`,
`vulkan-loader-devel`. Build with `-tags nox11` or `-tags nowayland` to drop a
backend.

## Status

The module and source skeleton exist (#4): the host opens an empty Gio
window, and `proto/`, `sdk/`, and `plugins/` are placeholders. The backlog
is tracked as GitHub issues, starting with CI (#5), the release workflow
(#6), the config format (#7), and the plugin protocol (#8).

## Git workflow

Never commit on or push to `main`; it is branch-protected on GitHub, and
local `pre-commit`/`pre-push` hooks reject it. Each change goes on its own
branch, in a worktree that sits beside `main/`, and lands through a PR.
