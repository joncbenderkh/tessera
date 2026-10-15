// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"os"
	"os/exec"
)

// ownProcessGroup does nothing on Windows: killGroup kills only the
// plugin, not its children, and a plugin outlives a host that dies
// without shutting it down.
func ownProcessGroup(*exec.Cmd) {}

// killGroup kills the plugin.
func killGroup(p *os.Process) { _ = p.Kill() }
