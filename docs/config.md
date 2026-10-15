# Configuration

tessera reads one YAML file: `tessera/config.yaml` in the user config
directory. That is `$XDG_CONFIG_HOME/tessera/config.yaml`, or
`~/.config/tessera/config.yaml`, on Linux. Pass `--config <path>` to use a
different file. On first run, tessera writes a default config there,
readable only by you (mode 0600). Later saves keep the file's existing
permissions.

tessera reloads the file when it changes. Edit layout rewrites it, which
drops any comments.

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
| `panels[].id` | Unique name for this panel. One plugin can back several panels. |
| `panels[].plugin` | Name of the plugin that provides the panel. |
| `panels[].enabled` | `false` hides the panel. Defaults to `true`. |
| `panels[].x`, `y` | Zero-based column and row of the panel's top-left cell. |
| `panels[].w`, `h` | Width and height in cells, each at least 1. |
| `panels[].settings` | Settings passed to the plugin. Their schema comes from the plugin. |

Unknown fields are errors, so a misspelt key is reported rather than
ignored. Every panel must fit within `grid.columns`. Enabled panels must
not overlap; disabled panels may overlap anything.
