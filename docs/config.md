# Configuration

tessera reads one YAML file: `tessera/config.yaml` in the user config
directory. That is `$XDG_CONFIG_HOME/tessera/config.yaml`, or
`~/.config/tessera/config.yaml`. Pass `--config <path>` to use a
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
| `theme` | The theme shown: `dark`, `light`, or the name of a custom theme under `themes`. Defaults to `dark`. The header's theme toggle sets it. |
| `themes` | Colours for the built-in themes and custom themes, by name. See [Themes](#themes). |
| `grid.columns` | Number of grid columns, from 1 to 24. |
| `grid.row_height` | Height of one grid row in dp (logical pixels), from 150 to 2000. Large text makes rows taller where a panel's head and its buttons or menu need it, and the window keeps room for one such row. |
| `panels[].id` | Unique name for this panel, read as written (`017` stays `017`). One plugin can back several panels. |
| `panels[].plugin` | Name of the plugin that provides the panel: lowercase letters, digits, and hyphens, starting with a letter or digit, at most 64 characters. Read as written, like `id`. |
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
heights. Plugins are told both the size in the config and the size
they are shown at, and are configured again when a window's width
changes the second. Arranging panels freely is #36.

A plugin can ask for a least size: the room its view needs, which the
host turns into cells for the row height and column width at the time.
Edit mode keeps a panel at least that large (see below); a panel the
file makes smaller is shown as it is, its view fading out where it is
cut off. With the default row height of 180, the system, disk,
network, calendar, month, processes, and services panels need two
rows, as they have by default; at a row height of 150, the world clock
and weather panels do too, and the services panel needs three.

### Themes

`themes` replaces colours of the built-in `dark` and `light` themes and
adds custom themes. Each entry under it names a theme and gives any of
the colour tokens below; a token left out keeps the colour of the
theme's base. A custom theme's `base` is `dark` or `light` (default
`dark`), with any colours `themes` gives that theme, so `dusk` below
has the accent given to `dark`. The built-in themes are their own
base, so they take no `base`.

```yaml
theme: dusk
themes:
  dark:
    accent: oklch(0.7 0.15 300)
  dusk:
    base: dark
    bg: "#1e1e2e"
    surface: "#252536"
    fg: "#cdd6f4"
```

| Token | Used for |
|---|---|
| `bg` | The window's background. |
| `surface` | Tiles, dialogs, menus, and the header. |
| `fg` | Text. |
| `muted` | Secondary text, such as captions and units. |
| `border` | Borders, dividers, and gauge tracks. |
| `accent` | Focus rings, highlights, and accent-toned text. |
| `ok`, `warn`, `error` | Text and marks in those tones, such as a panel's status. |
| `hover` | The background of a button, or a table or list row, under the pointer. |
| `inset` | Sunken areas: the Logs text, the Configure dialog's groups, and a tile's plugin details. |

A colour is CSS hex, `#rgb` or `#rrggbb`, or `oklch(L C H)`: lightness
from 0 to 1 (or 0% to 100%), chroma from 0 to 1, and hue in degrees, as in
`oklch(0.72 0.14 150)`. Colours outside sRGB are clamped into it.
Quote hex colours: `#` starts a YAML comment, so `bg: #1e1e2e` leaves
`bg` empty, which is an error.

A custom theme's name is lowercase letters, digits, and hyphens,
starting with a letter, at most 32 characters. The header's theme
toggle steps through `dark`, `light`, and then the custom themes in
order of name; with the pointer over it, a tip names the theme it
switches to. Removing the theme shown from the file shows the one
`theme` names.

tessera warns, in the window and the log, of a theme whose text is hard
to read: `fg`, `muted`, `ok`, `warn`, and `error` with a contrast below
4.5:1 against `bg` or `surface`, and `accent` below 3:1 (WCAG AA's
ratios for text and for large text and marks). `accent` is held to
3:1 since it mostly draws marks, so text a plugin sets in the accent
tone can be below 4.5:1 without a warning. The theme is still
applied. The built-in themes meet these ratios.

Releases before `themes` was added refuse a config that uses it, or
whose `theme` names a custom theme, as a whole; go back to one only
after removing them.

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
- Neither a drag nor the keys make a panel smaller than its plugin
  asks for; one smaller already, as the file can make it, can grow
  but not shrink.
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
  and so on). Once its plugin has started, the panel grows to the
  plugin's default size and the least size it asks for, moving left
  if the grid's edge is in the way. It grows only while the window
  shows all the grid's columns, as other resizing needs. A growth
  that would push a panel past the last row waits until another change
  makes room. One still waiting when edit mode ends stays one cell.

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

**Configure…** in a panel's menu, in edit mode or not, opens a form
for the panel's settings, made from the config schema its plugin
declares: a text field, number field, toggle, or set of choices per
setting, a list with Add and Remove for one that holds several, and
YAML text for anything else. A setting stays out of the file, so the
plugin's default applies, until it is given a value; **Default**
beside a setting leaves it out again. An emptied text field holds
`""`, an emptied list `[]`, and a blank text item in a list `""`; a
blank number in a list must be filled in or removed. A number field
takes decimals (`3`, `-0.5`, `1e3`), and whole numbers only within
±2^53, however written. Settings in the
file the schema does not list are kept as they are, each with a
**Remove** to leave it out, as for a misspelt name. **Save** checks the settings
against the schema, and, if the panel is running, gives them to its
plugin first; if either refuses them, the form says why, at the
setting where it can, and nothing is saved. Nothing is saved either if
the file changed the panel's settings or plugin while the form was
open: reopen **Configure…** to start from the file. Otherwise the
settings are saved at once, like a layout change, and the panel shows
them. The
form needs the plugin to have started at least once; for a panel whose
plugin is missing, edit the file.

Settings in the file are checked against the plugin's schema too,
whenever the plugin starts or the file changes. A panel whose settings
fail it is not configured: it shows an error tile listing the problems
(**Logs** has them too), and starts again once its settings are fixed,
from the form or in the file. The settings of a plugin that declares
no schema are not checked by tessera, and the form edits them as YAML.
A setting left blank, such as `title:`, is null, which means
the plugin's default, as a missing one does, at any depth: a blank
`label:` in a clock of a list of clocks is left out too. A blank item
in a list stays, as null, since its place counts.

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
