// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"sync"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// First-party plugins' tests use TileBodySize, ViewSize and ViewFits to check that
// their views fit a tile, at one pixel per dp with text scaled by
// textScale.

// measure guards the theme every measurement shares, so its shaper
// caches the text it has laid out; a shaper is not safe for concurrent
// use.
var measure struct {
	sync.Mutex
	th *theme.Theme
}

// measuring locks measure and returns a context for measuring at most
// max, and the theme. The caller unlocks measure.
func measuring(max image.Point, textScale float32) (layout.Context, *theme.Theme) {
	measure.Lock()
	if measure.th == nil {
		measure.th = theme.New(true, theme.NewShaper(false))
	}
	var r input.Router
	return layout.Context{
		Ops:         new(op.Ops),
		Source:      r.Source(),
		Now:         time.Now(),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: textScale},
		Constraints: layout.Constraints{Max: max},
	}, measure.th
}

// TileBodySize is the space a tile of size, titled title, leaves its
// view: what its head and padding do not take, as Layout lays it out
// in a window wide enough for its normal head.
func TileBodySize(size image.Point, title string, textScale float32) image.Point {
	gtx, th := measuring(size, textScale)
	defer measure.Unlock()
	pad := gtx.Dp(theme.Space16)
	inner := gtx
	inner.Constraints.Max.X = max(size.X-2*pad, 0)
	menu := Button{Icon: true, Bare: true}
	menuH := record(inner, func(gtx layout.Context) layout.Dimensions { return menu.Layout(gtx, th, "⋯") }).dims.Size.Y
	nameH := record(inner, titleLabel(th, title).Layout).dims.Size.Y
	return bodySize(gtx, size, headHeight(gtx, nameH, menuH, false))
}

// ViewSize is the size view's body takes laid out at most width wide.
func ViewSize(view *tesserav1.View, width int, textScale float32) image.Point {
	gtx, th := measuring(image.Pt(width, 1<<20), textScale)
	defer measure.Unlock()
	var v viewState
	v.Layout(gtx, th, view) // sets up the state widget reads
	gtx.Ops = new(op.Ops)
	return v.widget(gtx, th, view.GetBody()).Size
}

// ViewFits reports whether view's body fits in body: laid out there, no
// text is cut short and no widget is left out for lack of room, so the
// tile shows it without a fade. Tables and lists scroll, so any height
// that shows them at all holds them.
func ViewFits(view *tesserav1.View, body image.Point, textScale float32) bool {
	gtx, th := measuring(body, textScale)
	defer measure.Unlock()
	var v viewState
	v.Layout(gtx, th, view)
	return !v.cut
}
