# Configuration

tessera reads one YAML file: `tessera/config.yaml` in the user config
directory. That is `$XDG_CONFIG_HOME/tessera/config.yaml`, or
`~/.config/tessera/config.yaml`, on Linux. Pass `--config <path>` to use a
different file. On first run, tessera writes a default config there,
readable only by you (mode 0600). Later saves keep the file's existing
permissions.

tessera reads the file at startup; restart it after editing. Reloading
on change and Edit layout, which will rewrite the file and drop any
comments, arrive with the grid layout engine (#10).

## Example

```yaml
version: 1
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
| `grid.columns` | Number of grid columns, at least 1. |
| `grid.row_height` | Height of one grid row, at least 1. |
| `panels[].id` | Unique name for this panel, read as written (`017` stays `017`). One plugin can back several panels. |
| `panels[].plugin` | Name of the plugin that provides the panel: lowercase letters, digits, and hyphens, starting with a letter or digit, at most 64 characters, and not a Windows device name such as `con` or `com1`. Read as written, like `id`. |
| `panels[].enabled` | `false` hides the panel. Defaults to `true`. |
| `panels[].x`, `y` | Zero-based column and row of the panel's top-left cell. |
| `panels[].w`, `h` | Width and height in cells, each at least 1. |
| `panels[].settings` | Settings passed to the plugin. Their schema comes from the plugin; see [Settings](#settings). |

Unknown fields are errors, so a misspelt key is reported rather than
ignored. Aliases (`*name`) are not supported, nor are merge keys (`<<`)
outside settings. Every panel must fit within `grid.columns`. Enabled
panels must not overlap; disabled panels may overlap anything.

### Settings

A panel's settings reach its plugin as JSON-model values, so tessera
reads them strictly by YAML 1.2's core schema, whatever the rest of the
YAML tooling does: `017` is 17, `1e5` is a number, `1_000`, `0b101`,
`yes`, and dates are strings, and `<<` is an ordinary key. Tags other
than `!!str` (on keys too), `.inf` and `.nan`, and floats beyond a
double, too large or so small it would become 0, are errors, and so
are integers beyond ±2^53, which a plugin would receive rounded. Quote a value to pass it as a string. When
tessera rewrites the file, it quotes every key and string that would
otherwise read back as something else.
