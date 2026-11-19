// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
)

// semanticArea adds an element's semantics, which add gives, in an area
// of their own, bounds large, with no input handlers. A router keeps
// the semantics of an area with handlers only if they ask it for
// pointer events, which those of the window's router do not ask of
// another, such as the one uidebug reads the window's elements with.
func semanticArea(gtx layout.Context, bounds image.Rectangle, add func()) {
	defer clip.Rect(bounds).Push(gtx.Ops).Pop()
	add()
}
