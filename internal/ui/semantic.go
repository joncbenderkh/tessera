// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op/clip"

	"github.com/joncbenderkh/tessera/internal/uimark"
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

// viewport clips what is drawn until pop is called to bounds, the part
// of a scrolling list or grid that shows, in an area marked as a
// viewport, to whose bounds uidebug cuts those of the elements in it.
func viewport(gtx layout.Context, bounds image.Rectangle) (pop func()) {
	area := clip.Rect(bounds).Push(gtx.Ops)
	semantic.DescriptionOp(uimark.Viewport).Add(gtx.Ops)
	return area.Pop
}

// modalLayer marks a dialog's or popover's backdrop, bounds large, after
// which uidebug takes what was drawn before for covered.
func modalLayer(gtx layout.Context, bounds image.Rectangle) {
	semanticArea(gtx, bounds, func() { semantic.DescriptionOp(uimark.Modal).Add(gtx.Ops) })
}
