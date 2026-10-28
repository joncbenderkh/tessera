# First-party plugins

Each subdirectory is one panel plugin: its own `main` package, built as a
separate executable with `CGO_ENABLED=0`. They use the same protocol and SDK
as third-party plugins; the host treats them no differently.

Each one's version is tessera's, from `./VERSION`. Their tests run the
test binary as the plugin through `internal/plugintest`.

| Plugin | Shows | Settings |
|---|---|---|
| `clock` | The time to the second, the date, and the time zone | `timezone`: an IANA name such as `Europe/Berlin`, in any case, or `Local` (the default) |
| `greeting` | A heading, a line below it, and the computer's name | `text` and `secondary`, each hidden when set to `""` (left out or empty, as in `text:`, it keeps its default); `host_info` (default `true`) |
| `notes` | A note | `title` and `body`; a body keeps its line breaks |

Text settings are strings: quote one that would read as a number or
boolean, such as `title: "2026"`, or the panel rejects it.

```yaml
panels:
  - id: notes
    plugin: notes
    x: 0
    y: 1
    w: 2
    h: 1
    settings:
      title: Before Friday
      body: |
        Renew the certificate.
        Book the review.
```
