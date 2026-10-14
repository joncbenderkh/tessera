# Design mockups

Static HTML prototypes of the tessera window, generated with OpenDesign on
2026-10-14. Open them directly in a browser; they need no build step and
load nothing from the network. The data in them is mocked.

- `tessera-dashboard.html`: the main window. It shows the window header,
  then a grid of first-party plugin panels in their live, starting, stale,
  and crashed states. Greeting and Notes can be disabled and re-enabled from
  Edit layout.
- `widget-vocabulary.html`: the reference for the host renderer. It shows
  each widget primitive (text, stat, gauge, sparkline, table, list) on its
  own, the spacing, radius, and type scales, and one example tile per panel
  state.

These files are design targets, not code: the Gio renderer reproduces them,
it does not embed them. Where they disagree with `CLAUDE.md`, `CLAUDE.md`
wins.
