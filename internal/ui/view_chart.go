// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
	"strconv"
	"strings"

	"gioui.org/f32"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera/internal/ui/theme"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// The gauge's dial, in the units of the mockup's 120 by 104 SVG: a 260°
// arc of radius 48 around (60, 54), starting at 140° (screen angles,
// clockwise from the right), stroked 9 wide.
const (
	dialW, dialH        = 120, 104
	dialCX, dialCY      = 60, 54
	dialR, dialStroke   = 48, 9
	dialStart, dialSpan = 140.0, 260.0
	dialValueY          = 54  // the value's baseline
	dialLimitY          = 100 // the min and max labels' baseline
	dialLimitX          = 23  // the min label's centre; max mirrors it
	// tickIn and tickOut are a threshold tick's ends, from the centre.
	tickIn, tickOut = 44.25, 51.75
	// dialMaxScale caps how far a dial grows past its 96 dp diameter.
	dialMaxScale = 1.5
)

// gaugeTone is the state the gauge's value puts it in.
type gaugeTone int

const (
	gaugeNormal gaugeTone = iota
	gaugeWarn
	gaugeError
	gaugeMissing
)

// gaugeReading is what a gauge shows, worked out from its fields.
type gaugeReading struct {
	tone          gaugeTone
	min, max      float64
	fraction      float64   // of the arc filled
	ticks         []float64 // threshold positions on the arc, 0 to 1, warn first
	tickTones     []gaugeTone
	value, limits [2]string // the value and unit; the min and max labels
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// readGauge works out g's tone, fill and labels. A non-finite value or
// bound, or max not above min, is missing: no fill and an em dash.
func readGauge(g *tesserav1.Gauge) gaugeReading {
	r := gaugeReading{min: g.GetMin(), max: 100}
	if g.Max != nil {
		r.max = g.GetMax()
	}
	r.value[1] = g.GetUnit()
	// A range too wide for a float64 counts as missing too.
	if !finite(g.GetValue()) || !finite(r.min) || !finite(r.max) || r.max <= r.min || !finite(r.max-r.min) {
		r.tone = gaugeMissing
		r.value[0] = "—"
		return r
	}
	r.limits = [2]string{formatNumber(r.min), formatNumber(r.max)}
	v := math.Min(math.Max(g.GetValue(), r.min), r.max)
	r.fraction = (v - r.min) / (r.max - r.min)
	r.value[0] = g.GetDisplay()
	if r.value[0] == "" {
		r.value[0] = formatNumber(v)
	}
	at := func(t *float64) (float64, bool) {
		if t == nil || math.IsNaN(*t) {
			return 0, false
		}
		return *t, true
	}
	// A threshold below min always applies, one above max never: the
	// clamped value is compared with it either way.
	if t, ok := at(g.WarnAt); ok {
		if v >= t {
			r.tone = gaugeWarn
		}
		if r.min <= t && t <= r.max {
			r.ticks, r.tickTones = append(r.ticks, (t-r.min)/(r.max-r.min)), append(r.tickTones, gaugeWarn)
		}
	}
	if t, ok := at(g.ErrorAt); ok {
		if v >= t {
			r.tone = gaugeError
		}
		if r.min <= t && t <= r.max {
			r.ticks, r.tickTones = append(r.ticks, (t-r.min)/(r.max-r.min)), append(r.tickTones, gaugeError)
		}
	}
	return r
}

// formatNumber is x as the host shows a number it formats itself: at
// most one decimal, none if it would be zero; two significant digits
// below 1, with an exponent below 0.0001; and four significant digits
// with an exponent for a huge one.
func formatNumber(x float64) string {
	if math.Abs(x) >= 1e15 {
		return strconv.FormatFloat(x, 'g', 4, 64)
	}
	if x != 0 && math.Abs(x) < 1e-4 {
		return strconv.FormatFloat(x, 'g', 2, 64)
	}
	if math.Abs(x) < 1 {
		// Small values keep their first digits: 0.05 is not 0.1.
		x, _ = strconv.ParseFloat(strconv.FormatFloat(x, 'g', 2, 64), 64)
	} else {
		x = math.Round(x*10) / 10
	}
	if x == 0 {
		x = 0 // not "-0"
	}
	return strconv.FormatFloat(x, 'f', -1, 64)
}

func (r gaugeReading) color(th *theme.Theme, t gaugeTone) color.NRGBA {
	switch t {
	case gaugeWarn:
		return th.Warn
	case gaugeError:
		return th.Error
	}
	return th.Muted
}

// describe is what a screen reader says for the gauge.
func (r gaugeReading) describe(g *tesserav1.Gauge) string {
	parts := []string{strings.TrimSpace(r.value[0] + " " + r.value[1])}
	if g.GetLabel() != "" {
		parts = append([]string{g.GetLabel()}, parts...)
	}
	for i, t := range r.ticks {
		name := "warning"
		if r.tickTones[i] == gaugeError {
			name = "error"
		}
		parts = append(parts, fmt.Sprintf("%s at %s", name, formatNumber(r.min+t*(r.max-r.min))))
	}
	return strings.Join(parts, "; ")
}

// dialScale is the size of the dial's unit, in px, for the space gtx
// gives it with room left for rest px of text below: the largest that
// fits, from 1 dp (a 96 dp diameter, the floor) up to dialMaxScale dp.
func dialScale(gtx layout.Context, rest int) float32 {
	dp := gtx.Metric.PxPerDp
	fit := min(float32(gtx.Constraints.Max.X)/dialW, float32(max(gtx.Constraints.Max.Y-rest, 0))/dialH)
	return max(min(fit, dialMaxScale*dp), dp)
}

// arc is the path of the dial's arc from fraction a to b of its span.
func arc(ops *op.Ops, u float32, a, b float64) clip.PathSpec {
	var p clip.Path
	p.Begin(ops)
	p.MoveTo(dialPoint(u, dialR, a))
	c := f32.Pt(u*dialCX, u*dialCY)
	// Screen angles grow clockwise, as y points down.
	p.ArcTo(c, c, float32((b-a)*dialSpan*math.Pi/180))
	return p.End()
}

// dialPoint is the point at radius r (dial units) and fraction t of the
// span, in px for a unit of u px.
func dialPoint(u float32, r, t float64) f32.Point {
	a := (dialStart + dialSpan*t) * math.Pi / 180
	return f32.Pt(u*float32(dialCX+r*math.Cos(a)), u*float32(dialCY+r*math.Sin(a)))
}

// strokeArc strokes the arc from a to b with round ends, as the
// mockup's stroke-linecap: round.
func strokeArc(gtx layout.Context, u float32, a, b float64, c color.NRGBA) {
	w := u * dialStroke
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: arc(gtx.Ops, u, a, b), Width: w}.Op())
	for _, t := range []float64{a, b} {
		dot(gtx, dialPoint(u, dialR, t), w/2, c)
	}
}

// dot fills a circle of radius r around p.
func dot(gtx layout.Context, p f32.Point, r float32, c color.NRGBA) {
	box := image.Rect(int(p.X-r), int(p.Y-r), int(math.Ceil(float64(p.X+r))), int(math.Ceil(float64(p.Y+r))))
	paint.FillShape(gtx.Ops, c, clip.Ellipse(box).Op(gtx.Ops))
}

// dialLimits draws the min and max labels under the arc's ends.
func dialLimits(gtx layout.Context, th *theme.Theme, u float32, limits [2]string) {
	gtx.Constraints.Min = image.Point{}
	for i, l := range limits {
		if l == "" {
			continue
		}
		lbl := recordLabel(gtx, th, l, theme.TextMin, th.Muted, theme.Mono)
		cx := u * dialLimitX
		if i == 1 {
			cx = u * (dialW - dialLimitX)
		}
		lbl.at(gtx, image.Pt(int(cx)-lbl.dims.Size.X/2, int(u*dialLimitY)-(lbl.dims.Size.Y-lbl.dims.Baseline)))
	}
}

// gaugeTrack is a loading gauge: the bare track, with two short bars
// where the value goes, and its limits, as the mockup's.
func (v *viewState) gaugeTrack(gtx layout.Context, th *theme.Theme, g *tesserav1.Gauge) layout.Dimensions {
	u := dialScale(gtx, 0)
	width := gtx.Constraints.Max.X
	if v.narrow {
		width = min(int(u*dialW), width)
	}
	x := (width - int(u*dialW)) / 2
	if x < 0 { // the least dial is wider than the room
		v.cut = true
		defer fadeOut(gtx, th, image.Rect(0, 0, width, min(int(u*dialH), gtx.Constraints.Max.Y)), true)
	}
	defer op.Offset(image.Pt(max(x, 0), 0)).Push(gtx.Ops).Pop()
	strokeArc(gtx, u, 0, 1, th.Border)
	dialLimits(gtx, th, u, readGauge(g).limits)
	for _, b := range [][3]float32{{43.5, 76.5, 49.5}, {50.25, 69.75, 62.25}} {
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(f32.Pt(u*b[0], u*b[2]))
		p.LineTo(f32.Pt(u*b[1], u*b[2]))
		paint.FillShape(gtx.Ops, th.Border, clip.Stroke{Path: p.End(), Width: u * dialStroke}.Op())
		dot(gtx, f32.Pt(u*b[0], u*b[2]), u*dialStroke/2, th.Border)
		dot(gtx, f32.Pt(u*b[1], u*b[2]), u*dialStroke/2, th.Border)
	}
	return layout.Dimensions{Size: image.Pt(width, int(u*dialH))}
}

// gauge draws the dial, centred, with the label and caption under it.
func (v *viewState) gauge(gtx layout.Context, th *theme.Theme, g *tesserav1.Gauge) layout.Dimensions {
	r := readGauge(g)
	width := gtx.Constraints.Max.X
	tgtx := gtx
	tgtx.Constraints.Min = image.Point{}
	var below []recorded
	if g.GetLabel() != "" {
		below = append(below, record(tgtx, wrapped(th, g.GetLabel(), theme.TextMin, th.Muted, theme.Body).Layout))
	}
	if g.GetCaption() != "" {
		below = append(below, record(tgtx, wrapped(th, g.GetCaption(), theme.TextMin, th.Muted, theme.Mono).Layout))
	}
	gap := gtx.Dp(theme.Space4)
	rest := 0
	for _, b := range below {
		rest += b.dims.Size.Y + gap
	}
	u := dialScale(gtx, rest)
	dw, dh := int(u*dialW), int(u*dialH)
	if v.narrow {
		natural := dw
		for _, b := range below {
			natural = max(natural, b.dims.Size.X)
		}
		width = min(natural, width)
	}
	area := image.Rectangle{Max: image.Pt(width, dh)}
	sem := clip.Rect(area).Push(gtx.Ops)
	semantic.DescriptionOp(r.describe(g)).Add(gtx.Ops)
	sem.Pop()

	off := op.Offset(image.Pt(max((width-dw)/2, 0), 0)).Push(gtx.Ops)
	strokeArc(gtx, u, 0, 1, th.Border)
	if r.tone != gaugeMissing && r.fraction > 0 {
		strokeArc(gtx, u, 0, r.fraction, r.color(th, r.tone))
	}
	for i, t := range r.ticks {
		c := r.color(th, r.tickTones[i])
		c.A = uint8(int(c.A) * 6 / 10)
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(dialPoint(u, tickIn, t))
		p.LineTo(dialPoint(u, tickOut, t))
		paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: 2 * u}.Op())
	}
	// The value and unit, centred on their baseline; the limits under
	// the arc's ends.
	// It has the part of the dial that shows, so one too wide is cut
	// with an ellipsis rather than at the edges.
	vw := min(width, dw)
	vgtx := tgtx
	vgtx.Constraints.Max.X = vw
	value := record(vgtx, func(gtx layout.Context) layout.Dimensions {
		return v.valueWithUnit(gtx, th, r.value[0], theme.TextStat, r.value[1], theme.TextSmall)
	})
	value.at(gtx, image.Pt((vw-value.dims.Size.X)/2, int(u*dialValueY)-(value.dims.Size.Y-value.dims.Baseline)))
	dialLimits(tgtx, th, u, r.limits)
	off.Pop()

	y := dh
	for _, b := range below {
		y += gap
		b.at(gtx, image.Pt(max((width-b.dims.Size.X)/2, 0), y))
		y += b.dims.Size.Y
	}
	if y > gtx.Constraints.Max.Y { // even at the dial's least size
		v.cut = true
		fadeOut(gtx, th, image.Rect(0, 0, width, gtx.Constraints.Max.Y), false)
	}
	if dw > width { // as wide
		v.cut = true
		fadeOut(gtx, th, image.Rect(0, 0, width, min(y, gtx.Constraints.Max.Y)), true)
	}
	return layout.Dimensions{Size: image.Pt(width, y)}
}

// sparkHeight is the chart's height, as the mockup's.
const sparkHeight unit.Dp = 39

// sparkPoints maps samples onto a w by h chart: x evenly spaced, y from
// lo at the bottom to hi at the top, clamped. A non-finite sample ends
// a run; each run is a separate line. An empty range draws the line at
// mid-height, as does one too wide for a float64. With more than two
// samples a pixel column, each column of a run keeps only its highest
// and lowest point, in order: the line looks the same, and costs what
// the width does rather than what the samples do.
func sparkPoints(samples []float64, lo, hi float64, w, h float32) [][]f32.Point {
	thin := float32(len(samples)) > 2*w
	type extreme struct {
		p f32.Point
		i int
	}
	var runs [][]f32.Point
	var run []f32.Point
	col := -1 // the column being thinned
	var top, bottom extreme
	flush := func() {
		if col < 0 {
			return
		}
		first, second := top, bottom
		if bottom.i < top.i {
			first, second = bottom, top
		}
		run = append(run, first.p)
		if second.i != first.i {
			run = append(run, second.p)
		}
		col = -1
	}
	for i, s := range samples {
		if !finite(s) {
			flush()
			if len(run) > 0 {
				runs, run = append(runs, run), nil
			}
			continue
		}
		x := w / 2
		if len(samples) > 1 {
			x = w * float32(i) / float32(len(samples)-1)
		}
		y := h / 2
		if hi > lo && finite(hi-lo) {
			f := (math.Min(math.Max(s, lo), hi) - lo) / (hi - lo)
			y = h * float32(1-f)
		}
		p := extreme{f32.Pt(x, y), i}
		switch {
		case !thin:
			run = append(run, p.p)
		case int(x) != col:
			flush()
			col, top, bottom = int(x), p, p
		case y < top.p.Y:
			top = p
		case y > bottom.p.Y:
			bottom = p
		}
	}
	flush()
	if len(run) > 0 {
		runs = append(runs, run)
	}
	return runs
}

// sparkRange is the y axis: min and max if set and finite, else the
// smallest and largest finite sample. ok is false with no finite sample.
func sparkRange(s *tesserav1.Sparkline) (lo, hi float64, ok bool) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, x := range s.GetSamples() {
		if finite(x) {
			lo, hi, ok = math.Min(lo, x), math.Max(hi, x), true
		}
	}
	if s.Min != nil && finite(*s.Min) {
		lo = *s.Min
	}
	if s.Max != nil && finite(*s.Max) {
		hi = *s.Max
	}
	return lo, hi, ok
}

// sparkline draws the label, the chart and its axis captions. With no
// finite samples it says so in place of the chart.
func (v *viewState) sparkline(gtx layout.Context, th *theme.Theme, s *tesserav1.Sparkline) layout.Dimensions {
	width := gtx.Constraints.Max.X
	tgtx := gtx
	tgtx.Constraints.Min = image.Point{}
	var parts []recorded
	var gaps []int
	if s.GetLabel() != "" {
		parts = append(parts, record(tgtx, wrapped(th, s.GetLabel(), theme.TextMin, th.Muted, theme.Body).Layout))
		gaps = append(gaps, gtx.Dp(6))
	}
	lo, hi, ok := sparkRange(s)
	if !ok {
		parts = append(parts, recordLabel(tgtx, th, "No samples yet", theme.TextMin, th.Muted, theme.Body))
		return column(gtx, parts, gaps)
	}
	line, area := th.Muted, th.Fg
	area.A = 18 // 7%
	if s.GetTone() != tesserav1.Tone_TONE_UNSPECIFIED {
		line = viewTone(th, s.GetTone(), th.Muted)
		area = line
		area.A = 26 // 10%
	}
	h := gtx.Dp(sparkHeight)
	parts = append(parts, record(tgtx, func(gtx layout.Context) layout.Dimensions {
		sem := clip.Rect{Max: image.Pt(width, h)}.Push(gtx.Ops)
		semantic.DescriptionOp(describeSpark(s)).Add(gtx.Ops)
		sem.Pop()
		stroke := float32(gtx.Metric.PxPerDp) * 1.5
		// The plot is inset by a dot's radius, so a dot or line at an
		// edge is not cut off there.
		in := 2 * stroke
		defer op.Affine(f32.AffineId().Offset(f32.Pt(in, in))).Push(gtx.Ops).Pop()
		fw, fh := float32(width)-2*in, float32(h)-2*in
		for _, run := range sparkPoints(s.GetSamples(), lo, hi, fw, fh) {
			if len(run) == 1 {
				dot(gtx, run[0], 2*stroke, line)
				continue
			}
			var fill clip.Path
			fill.Begin(gtx.Ops)
			fill.MoveTo(f32.Pt(run[0].X, fh))
			for _, p := range run {
				fill.LineTo(p)
			}
			fill.LineTo(f32.Pt(run[len(run)-1].X, fh))
			fill.Close()
			paint.FillShape(gtx.Ops, area, clip.Outline{Path: fill.End()}.Op())
			var p clip.Path
			p.Begin(gtx.Ops)
			p.MoveTo(run[0])
			for _, q := range run[1:] {
				p.LineTo(q)
			}
			paint.FillShape(gtx.Ops, line, clip.Stroke{Path: p.End(), Width: stroke}.Op())
		}
		return layout.Dimensions{Size: image.Pt(width, h)}
	}))
	if s.GetStartLabel() != "" || s.GetEndLabel() != "" {
		gaps = append(gaps, gtx.Dp(theme.Space4))
		parts = append(parts, record(tgtx, func(gtx layout.Context) layout.Dimensions {
			captions := func(gtx layout.Context) (recorded, recorded) {
				return recordLabel(gtx, th, s.GetStartLabel(), theme.TextMin, th.Muted, theme.Mono),
					recordLabel(gtx, th, s.GetEndLabel(), theme.TextMin, th.Muted, theme.Mono)
			}
			start, end := captions(gtx)
			// Captions that do not both fit get half the width each,
			// cut off there, so the end one stays at the right edge.
			gap := gtx.Dp(theme.Space8)
			if start.dims.Size.X+gap+end.dims.Size.X > width {
				half := gtx
				half.Constraints.Max.X = max((width-gap)/2, 0)
				start, end = captions(half)
			}
			start.at(gtx, image.Point{})
			end.at(gtx, image.Pt(width-end.dims.Size.X, 0))
			return layout.Dimensions{Size: image.Pt(width, max(start.dims.Size.Y, end.dims.Size.Y))}
		}))
	}
	return column(gtx, parts, gaps)
}

// describeSpark is what a screen reader says for a sparkline.
func describeSpark(s *tesserav1.Sparkline) string {
	var latest string
	n := 0
	for _, x := range slices.Backward(s.GetSamples()) {
		if finite(x) {
			if n == 0 {
				latest = formatNumber(x)
			}
			n++
		}
	}
	d := fmt.Sprintf("%d samples", n)
	if s.GetStartLabel() != "" && s.GetEndLabel() != "" {
		d += fmt.Sprintf(" from %s to %s", s.GetStartLabel(), s.GetEndLabel())
	}
	if latest != "" {
		d += ", latest " + strings.TrimSpace(latest+" "+s.GetUnit())
	}
	if s.GetLabel() != "" {
		d = s.GetLabel() + ": " + d
	}
	return d
}
