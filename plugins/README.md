# First-party plugins

Each subdirectory is one panel plugin: its own `main` package, built as a
separate executable with `CGO_ENABLED=0`. They use the same protocol and SDK
as third-party plugins; the host treats them no differently.
