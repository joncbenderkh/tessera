// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || android

package pluginhost

// tessera runs on Linux alone (#49). A build for another system stops
// here with that reason, before the names only Linux defines.
var _ = "tessera runs on Linux only" + 1
