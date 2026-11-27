// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"
	"image/color"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// Window widths, in dp, at or below which the chrome tightens: Compact
// shows the plugin status as its dot alone, Narrow drops the layout
// selector.
const (
	Compact unit.Dp = 1000
	Narrow  unit.Dp = 560
)

// HeaderHeight is the window header's height at normal text sizes.
const HeaderHeight unit.Dp = 58

// LayoutName is the layout selector's one entry until the config has
// layout profiles.
const LayoutName = "Default"

// Header is the window header: the app name, the layout selector, the
// plugin status, and the Edit layout and theme toggles. The caller reads
// the toggles' clicks, and makes Edit a toggle and sets Edit.Pressed,
// and opens its StatusList when Status is clicked.
type Header struct {
	Edit   Button
	Theme  Button
	Status Button

	// NextTheme names the theme Theme switches to, and NextDark says
	// whether it is dark, which picks Theme's icon. Without a name, it
	// switches between the built-in dark and light themes.
	NextTheme string
	NextDark  bool

	placed   []image.Rectangle // the items' bounds in the last Layout
	statusAt image.Rectangle   // where Status was drawn
	themeAt  image.Rectangle   // where Theme was drawn
	// tipBlocked keeps the tip down after a disabled frame until the
	// pointer is seen off the toggle: a disabled frame hears no leave,
	// so Theme may still read as hovered once the pointer has gone.
	tipBlocked bool
	tipSince   time.Time       // when the tip went up
	tipText    string          // what the tip said then
	tipAt      image.Rectangle // where LayoutTip last drew; for tests
}

// StatusAnchor is where the status was drawn in the last Layout, which
// its list opens below.
func (h *Header) StatusAnchor() image.Rectangle { return h.statusAt }

// headerFit is what the header shows, and its spacing, at a window
// width.
type headerFit struct {
	// status is the status in full, with its text; otherwise its dot
	// alone shows.
	status, selector bool
	pad, gap, endGap unit.Dp
}

func fitHeader(width unit.Dp) headerFit {
	f := headerFit{status: true, selector: true, pad: 28, gap: 24, endGap: 14}
	if width <= Compact {
		f.status, f.endGap = false, 7
	}
	if width <= Narrow {
		f.selector, f.pad, f.gap = false, 12, 8
	}
	return f
}

// Layout draws the header across the top of gtx's constraints. It is
// HeaderHeight tall, or taller if a large text scale needs it. When its
// items do not fit across, it drops, in turn, the layout selector, the
// plugin status's text (keeping its dot), the app name (keeping the
// mark), and Edit's label (keeping an icon).
func (h *Header) Layout(gtx layout.Context, th *theme.Theme, status Status) layout.Dimensions {
	fit := fitHeader(widthDp(gtx))
	pad, gap, endGap := gtx.Dp(fit.pad), gtx.Dp(fit.gap), gtx.Dp(fit.endGap)
	width := gtx.Constraints.Max.X
	gtx.Constraints = layout.Constraints{Max: image.Pt(width, gtx.Constraints.Max.Y)}

	h.Theme.Icon = true
	editLabel := "Edit layout"
	if h.Edit.Pressed {
		editLabel = "Done editing"
	}
	// The Go fonts have ☼ but no ◐, so the half disc is drawn.
	next, nextDark := h.NextTheme, h.NextDark
	if next == "" {
		next, nextDark = "dark", true
		if th.Dark {
			next, nextDark = "light", false
		}
	}
	glyph := ""
	h.Theme.Draw = halfDisc
	if !nextDark {
		glyph, h.Theme.Draw = "☼", nil
	}
	h.Theme.Description = "Switch to " + next + " theme"

	// Each variant is laid out; only the ones placed are drawn.
	brandFull := record(gtx, func(gtx layout.Context) layout.Dimensions { return brand(gtx, th, true) })
	brandMark := record(gtx, func(gtx layout.Context) layout.Dimensions { return brand(gtx, th, false) })
	sel := record(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.UniformInset(theme.Space8).Layout(gtx, label(th, LayoutName, theme.TextSmall, th.Fg, theme.Body).Layout)
	})
	h.Status.Bare, h.Status.Description = true, "Panels needing attention"
	h.Status.Content = func(gtx layout.Context, _ color.NRGBA) layout.Dimensions { return statusLine(gtx, th, status) }
	stat := record(gtx, func(gtx layout.Context) layout.Dimensions { return h.Status.Layout(gtx, th, status.Text) })
	h.Status.Content = func(gtx layout.Context, _ color.NRGBA) layout.Dimensions { return statusDot(gtx, th, status) }
	statDot := record(gtx, func(gtx layout.Context) layout.Dimensions { return h.Status.Layout(gtx, th, status.Text) })
	h.Edit.Icon, h.Edit.Draw, h.Edit.Description = false, nil, ""
	editText := record(gtx, func(gtx layout.Context) layout.Dimensions { return h.Edit.Layout(gtx, th, editLabel) })
	h.Edit.Icon, h.Edit.Draw, h.Edit.Description = true, gridIcon, editLabel
	editIcon := record(gtx, func(gtx layout.Context) layout.Dimensions { return h.Edit.Layout(gtx, th, "") })
	themeBtn := record(gtx, func(gtx layout.Context) layout.Dimensions { return h.Theme.Layout(gtx, th, glyph) })

	showSel, showStatus, showName, showEditText := fit.selector, fit.status, true, true
	var left, end []recorded
	divider := gtx.Dp(1)
	for {
		left = []recorded{brandMark}
		if showName {
			left[0] = brandFull
		}
		if showSel {
			left = append(left, sel)
		}
		end = []recorded{statDot}
		if showStatus {
			end[0] = stat
		}
		if showEditText {
			end = append(end, editText)
		} else {
			end = append(end, editIcon)
		}
		end = append(end, themeBtn)
		need := 2*pad + gap + left[0].dims.Size.X
		if showSel {
			need += 2*gap + divider + sel.dims.Size.X
		}
		for i, r := range end {
			need += r.dims.Size.X
			if i > 0 {
				need += endGap
			}
		}
		switch {
		case need <= width:
		case showSel:
			showSel = false
			continue
		case showStatus:
			showStatus = false
			continue
		case showName:
			showName = false
			continue
		case showEditText:
			showEditText = false
			continue
		}
		break
	}

	height := gtx.Dp(HeaderHeight)
	for _, r := range append(left, end...) {
		height = max(height, r.dims.Size.Y+2*gtx.Dp(theme.Space12))
	}
	size := image.Pt(width, height)
	fillRect(gtx, image.Rectangle{Max: size}, th.Surface)
	fillRect(gtx, image.Rect(0, size.Y-gtx.Dp(1), size.X, size.Y), th.Border)
	middle := func(r recorded) int { return (size.Y - r.dims.Size.Y) / 2 }

	h.placed = h.placed[:0]
	place := func(r recorded, x int) {
		p := image.Pt(x, middle(r))
		r.at(gtx, p)
		h.placed = append(h.placed, image.Rectangle{Min: p, Max: p.Add(r.dims.Size)})
	}
	x := pad
	place(left[0], x)
	x += left[0].dims.Size.X + gap
	if showSel {
		d := gtx.Dp(19)
		fillRect(gtx, image.Rect(x, (size.Y-d)/2, x+divider, (size.Y+d)/2), th.Border)
		x += divider + gap
		place(sel, x)
	}
	ex := size.X - pad
	for i := len(end) - 1; i >= 0; i-- {
		ex -= end[i].dims.Size.X
		place(end[i], ex)
		ex -= endGap
	}
	// The theme toggle is the last of the end's items, placed first.
	h.themeAt = h.placed[len(h.placed)-len(end)]
	// The status is the first of the end's items.
	h.statusAt = h.placed[len(h.placed)-1]
	return layout.Dimensions{Size: size}
}

// TipFor is how long the theme toggle's tip stays up.
const TipFor = 5 * time.Second

// LayoutTip shows, below the theme toggle while the pointer is over
// it, what the toggle does: the theme it switches to. It is deferred,
// so it shows over the grid and the tiles' popovers drawn before it,
// and takes no input, as CSS pointer-events: none. Call it after
// Layout, in the same frame.
func (h *Header) LayoutTip(gtx layout.Context, th *theme.Theme) {
	h.tipAt = image.Rectangle{}
	switch {
	case !gtx.Enabled():
		h.tipBlocked = true
		return
	case !h.Theme.Hovered():
		h.tipBlocked, h.tipSince = false, time.Time{}
		return
	case h.tipBlocked || h.Theme.Description == "":
		return
	}
	// It goes after TipFor, since a pointer that leaves the window from
	// the toggle may never be heard leaving it.
	// A click that changes the next theme puts it up again.
	if h.tipSince.IsZero() || h.tipText != h.Theme.Description {
		h.tipSince, h.tipText = gtx.Now, h.Theme.Description
	}
	if end := h.tipSince.Add(TipFor); gtx.Now.Before(end) {
		gtx.Execute(op.InvalidateCmd{At: end})
	} else {
		return
	}
	padX, padY := gtx.Dp(10), gtx.Dp(6)
	lgtx := gtx
	lgtx.Constraints = layout.Constraints{Max: image.Pt(max(gtx.Constraints.Max.X-2*padX, 0), gtx.Constraints.Max.Y)}
	text := recordLabel(lgtx, th, h.Theme.Description, theme.TextSmall, th.Fg, theme.Body)
	box := image.Rectangle{Max: text.dims.Size.Add(image.Pt(2*padX, 2*padY))}
	// Its end lines up with the toggle's, inside the window.
	at := image.Pt(max(h.themeAt.Max.X-box.Dx(), 0), h.themeAt.Max.Y+gtx.Dp(theme.Space8))
	h.tipAt = box.Add(at)
	m := op.Record(gtx.Ops)
	off := op.Offset(at).Push(gtx.Ops)
	rr := gtx.Dp(theme.RadiusSmall)
	paint.FillShape(gtx.Ops, th.Inset, clip.UniformRRect(box, rr).Op(gtx.Ops))
	strokeRRect(gtx, box, rr, gtx.Dp(1), th.Border)
	text.at(gtx, image.Pt(padX, padY))
	off.Pop()
	op.Defer(gtx.Ops, m.Stop())
}

// brand is the tessera mark, a 2×2 grid of squares, and, if name is
// set, the app name.
func brand(gtx layout.Context, th *theme.Theme, name bool) layout.Dimensions {
	text := recordLabel(gtx, th, "tessera", theme.TextBrand, th.Fg, font.Font{Typeface: theme.Body.Typeface, Weight: 250})
	cell, gap := gtx.Dp(5), gtx.Dp(3)
	mark := 2*cell + gap
	height := max(text.dims.Size.Y, mark)
	top := (height - mark) / 2
	for i := range 4 {
		p := image.Pt(i%2*(cell+gap), top+i/2*(cell+gap))
		paint.FillShape(gtx.Ops, th.Fg, clip.UniformRRect(image.Rectangle{Min: p, Max: p.Add(image.Pt(cell, cell))}, gtx.Dp(1)).Op(gtx.Ops))
	}
	if !name {
		return layout.Dimensions{Size: image.Pt(mark, height)}
	}
	text.at(gtx, image.Pt(mark+gtx.Dp(9), 0))
	return layout.Dimensions{Size: image.Pt(mark+gtx.Dp(9)+text.dims.Size.X, height)}
}

// gridIcon is Edit's compact icon: a 2×2 grid of outlined squares, as
// panels on a grid.
func gridIcon(gtx layout.Context, side int, c color.NRGBA) {
	cell := side * 3 / 8
	gap := side - 2*cell
	line := max(gtx.Dp(1), 1)
	for i := range 4 {
		p := image.Pt(i%2*(cell+gap), i/2*(cell+gap))
		strokeRRect(gtx, image.Rectangle{Min: p, Max: p.Add(image.Pt(cell, cell))}, gtx.Dp(1), line, c)
	}
}

// halfDisc is ◐: a circle whose left half is filled.
func halfDisc(gtx layout.Context, side int, c color.NRGBA) {
	d := side * 3 / 4
	r := image.Rectangle{Max: image.Pt(d, d)}.Add(image.Pt((side-d)/2, (side-d)/2))
	strokeRRect(gtx, r, d/2, max(gtx.Dp(1), 1), c)
	left := clip.Rect(image.Rect(r.Min.X, r.Min.Y, r.Min.X+d/2, r.Max.Y)).Push(gtx.Ops)
	paint.FillShape(gtx.Ops, c, clip.Ellipse(r).Op(gtx.Ops))
	left.Pop()
}

// statusLine is a tone dot and the status text.
func statusLine(gtx layout.Context, th *theme.Theme, s Status) layout.Dimensions {
	text := recordLabel(gtx, th, s.Text, theme.TextMin, th.Muted, theme.Body)
	dot := gtx.Dp(6)
	top := (text.dims.Size.Y - dot) / 2
	r := image.Rect(0, top, dot, top+dot)
	paint.FillShape(gtx.Ops, toneColor(th, s.Tone), clip.Ellipse(r).Op(gtx.Ops))
	text.at(gtx, image.Pt(dot+gtx.Dp(7), 0))
	return layout.Dimensions{Size: image.Pt(dot+gtx.Dp(7)+text.dims.Size.X, text.dims.Size.Y)}
}

// statusDot is the status's tone dot alone, for a narrow window; the
// status's text names its button.
func statusDot(gtx layout.Context, th *theme.Theme, s Status) layout.Dimensions {
	d := gtx.Dp(8)
	paint.FillShape(gtx.Ops, toneColor(th, s.Tone), clip.Ellipse(image.Rect(0, 0, d, d)).Op(gtx.Ops))
	return layout.Dimensions{Size: image.Pt(d, d)}
}

func toneColor(th *theme.Theme, t Tone) color.NRGBA {
	switch t {
	case ToneWarn:
		return th.Warn
	case ToneError:
		return th.Error
	}
	return th.Ok
}
