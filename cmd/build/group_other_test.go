// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !unix

package main

import "testing"

func checkOwnGroup(*testing.T, int) {}

func backgroundSafe() bool { return true }
