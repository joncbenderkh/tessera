// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package pluginhost

func pidfds() int { return -1 }
