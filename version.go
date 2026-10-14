// SPDX-License-Identifier: GPL-3.0-or-later

// Package tessera holds build metadata shared by the host, the SDK, and
// first-party plugins.
package tessera

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var version string

// Version is the release version from ./VERSION, the single source of truth.
var Version = strings.TrimSpace(version)
