// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix && !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package pluginhost

import "os"

// canAwaitExit says awaitExit cannot wait without reaping here.
const canAwaitExit = false

func awaitExit(*os.Process) bool { return false }
