# Configuration

tessera reads one YAML file: `tessera/config.yaml` in the user config
directory. That is `$XDG_CONFIG_HOME/tessera/config.yaml`, or
`~/.config/tessera/config.yaml`, on Linux. Pass `--config <path>` to use a
different file. On first run, tessera writes a default config there,
readable only by you (mode 0600). Later saves keep the file's existing
permissions.

tessera reads the file at startup and again whenever it changes, and
applies the change at once: the grid and theme are redrawn, panels
added or enabled start, panels removed or disabled stop, and a panel
whose settings or size changed is configured again. An edit that does
not parse or validate is reported in the window, by its first problem
(the log has them all), and ignored, so the last good config stays in
use until the file is fixed, when the report goes away.
tessera rewrites the file when you switch the theme from the window
header or change the layout (see [Editing the layout](#editing-the-layout)),
which drops any comments (a change that leaves the config as it was
does not rewrite it); it rereads the file first, so other edits are
kept, and leaves the file alone if it cannot read it or it changes
during the save. If `config.yaml` is a symlink to a file, as with a
dotfiles manager, tessera writes the file it points to and keeps the
link; if that file's directory is read-only, as in the Nix store, the
change is reported as not saved, and a theme lasts only until tessera
restarts. A layout change that is not saved, there or because the file
changed during the save, lasts only until the file changes again, as
it does when tessera saves the next layout change; if the file is not
watched, until tessera restarts.
Changes are noticed through such links too, including a linked
`tessera` directory, and when a link is pointed elsewhere. An edit
made to the file a link points to, and a link pointed elsewhere, can
take about a second to be noticed. A link, to the file or to the
`tessera` directory, whose target does not exist yet stops tessera at
startup with an error, rather than being replaced by a default config.

## Example

```yaml
version: 1
theme: dark
grid:
  columns: 4
  row_height: 180
panels:
  - id: greeting
    plugin: greeting
    enabled: false
    x: 0
    y: 0
    w: 4
    h: 1
  - id: clock
    plugin: clock
    x: 0
    y: 0
    w: 1
    h: 1
    settings:
      timezone: Local
```

## Fields

| Field | Meaning |
|---|---|
| `version` | Schema version. Must be `1`. |
| `theme` | `dark` or `light`. Defaults to `dark`. The header's theme toggle sets it. |
| `grid.columns` | Number of grid columns, from 1 to 24. |
| `grid.row_height` | Height of one grid row in dp (logical pixels), from 150 to 2000. Large text makes rows taller where a panel's head and its buttons or menu need it, and the window keeps room for one such row. |
| `panels[].id` | Unique name for this panel, read as written (`017` stays `017`). One plugin can back several panels. |
| `panels[].plugin` | Name of the plugin that provides the panel: lowercase letters, digits, and hyphens, starting with a letter or digit, at most 64 characters, and not a Windows device name such as `con` or `com1`. Read as written, like `id`. |
| `panels[].enabled` | `false` hides the panel. Defaults to `true`. |
| `panels[].x`, `y` | Zero-based column and row of the panel's top-left cell. See [Grid](#grid). |
| `panels[].w`, `h` | Width and height in cells, each at least 1. A panel ends by row 1000: `y` + `h` is at most 1000. |
| `panels[].settings` | Settings passed to the plugin. Their schema comes from the plugin; see [Settings](#settings). |

Unknown fields are errors, so a misspelt key is reported rather than
ignored. Aliases (`*name`) are not supported, nor are merge keys (`<<`)
outside settings. Every panel must fit within `grid.columns`. Enabled
panels must not overlap; disabled panels may overlap anything.

### Grid

Each enabled panel keeps its column, `x`, but rises into any rows left
free above it, by disabled panels, removed ones, or rows left empty, so
the grid has no gaps for them. Panels are placed in the order of their
`y`, then their `x`, so a panel never passes one above it.

Narrow windows show fewer columns: at most two at 1000 dp (logical
pixels) or less, and one at 560 dp or less. No window shows a column
narrower than 200 dp, so many columns need a wide window: 1440 dp shows
at most six. Large text widens that minimum to what a failed panel's
Restart and Logs buttons need side by side. With fewer columns than
the config, the panels flow left to right and top to bottom in that
order, each at most as wide as the window's columns, and keep their
heights. Plugins are told the size in
the config, not the size they get in a narrow window. Arranging panels
freely is #36.

### Editing the layout

**Edit layout** in the window header switches edit mode on, and
**Done editing** switches it off. Each change is saved at once.

- Drag a panel by its head to move it, or by the handle in its bottom
  right corner to resize it; the grid shows where it will go, in the
  error colour if it cannot go there, as past the last row. The
  panels in its way move below it, and a panel dragged down onto
  another trades places with it. The grid is then compacted, as above,
  and saved with the rows it shows, so `y` matches the window until a
  panel is disabled: the ones below it keep their `y` in the file.
  A drag reaches only the rows in view: the grid does not scroll by
  itself, though the mouse wheel scrolls it during a drag, and a drag
  ends with no change if its panel scrolls out of view, or on Escape.
  The arrow keys move a panel further.
- With a panel's head focused (click it, or Tab to it), the arrow keys
  move it a column sideways or past the next panel up or down, and
  Shift with the arrow keys makes it wider, narrower, taller, or
  shorter. Moving up or down trades places with that panel; moving
  sideways or growing wider, like dragging, moves the panels in its
  way below it, and the panel itself below one that starts above it
  in its new columns.
- **Disable panel** in a panel's menu, in edit mode or not, sets
  `enabled: false`, with an Undo in the message that follows, for the
  panel disabled last. In edit mode, the bar above the grid has an
  **Enable** button for each disabled panel, which puts it back where
  it was; past four of them, the first three keep theirs and
  **Enable…** lists the rest. The buttons keep to one line, so in a
  narrow window fewer get a button of their own.
- **Add panel…** in that bar lists the plugins installed in the plugins
  directory; the one chosen gets a one-cell panel in the first free
  cell, with the plugin's name as its `id` (`clock`, then `clock-2`,
  and so on).

Moving and resizing need the window to show all the grid's columns;
in a narrower window, edit mode can still disable, enable, and add
panels. No window shows more than 7 columns, so a grid with more can
be moved and resized only in the file.

A change is saved at once. One that could not be saved stays in the
window, as [above](#configuration) says, and repeating it does not
save it, since the window already shows it; while the file is
watched, the next change that is saved, or the next edit to the file,
drops it. If the file was edited while the change was being saved,
the window takes the file's layout when it reads the edit.

### Settings

A panel's settings reach its plugin as JSON-model values, so tessera
reads them strictly by YAML 1.2's core schema, whatever the rest of the
YAML tooling does: `017` is 17, `1e5` is a number, `1_000`, `0b101`,
`yes`, and dates are strings, and `<<` is an ordinary key. Tags other
than `!!str` (on keys too), `.inf` and `.nan`, and floats beyond a
double, too large or so small it would become 0, are errors, and so
are integers beyond ±2^53, which a plugin would receive rounded.
Quote a value to pass it as a string. When
tessera rewrites the file, it quotes every key and string that would
otherwise read back as something else.
