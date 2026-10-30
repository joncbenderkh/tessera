// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !unix

package main

import "os/exec"

// ownGroup does nothing where there are no process groups to leave.
func ownGroup(*exec.Cmd) {}
