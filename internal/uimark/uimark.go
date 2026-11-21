// SPDX-License-Identifier: GPL-3.0-or-later

// Package uimark names the semantic markers internal/ui gives, as the
// descriptions of areas that hold nothing else, for internal/uidebug.
package uimark

const (
	// Viewport marks the part of a scrolling list or grid that shows:
	// what is in it shows only within it.
	Viewport = "tessera:viewport"
	// Modal marks a dialog's or popover's backdrop: what was drawn
	// before it is covered, and takes no clicks.
	Modal = "tessera:modal"
)
