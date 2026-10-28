// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"image"

	"gioui.org/gesture"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// Hints the edit bar gives: while panels can move, while the window is
// too narrow for that, and for a layout wider than any window shows.
const (
	HintMove   = "Drag a panel by its head to move it in view, or by its corner to resize it. Arrow keys move the focused panel any distance; with Shift they resize it."
	HintNarrow = "Widen the window to move and resize panels: it shows fewer columns than the layout has."
	HintWide   = "Panels cannot be moved or resized here: the layout has more columns than any window shows. Lower grid.columns in the config file to edit it here."
)

// EditBar is the bar above the grid in edit mode: a hint, an Enable
// button for each disabled panel, or for the first few and an Enable…
// menu of the rest, and Add panel, whose menu lists the installed
// plugins. The buttons keep to one line, so the bar is never more than
// that line and the hint high.
type EditBar struct {
	enable map[string]*Button
	more   popover           // Enable…, for the disabled panels past the first few
	add    popover           // Add panel…
	boxes  []image.Rectangle // where each button was drawn, Add panel last; for tests
	// refocus is 1 + the place, among the buttons, of one that went
	// with the panel it enabled, whose focus the button there now
	// takes; 0 if none went.
	refocus int
	// refocusMore moves the focus back to Enable… after a choice from
	// its menu, or to Add panel… if Enable… went with it.
	refocusMore bool
	opened      bool // the last Layout opened a menu
}

// inlineEnable is how many disabled panels get an Enable button of
// their own: all of them up to this many, and one fewer, with Enable…
// for the rest, past it; fewer if their buttons do not fit on a line.
const inlineEnable = 4

// shortWindow is the height below which the bar's hint keeps to one
// line, so the grid keeps room in a short window.
const shortWindow unit.Dp = 360

// Layout draws the bar across the top of gtx's constraints, aligned with
// the grid, for cfg's disabled panels and the plugins installed, and
// returns its height and what the user chose. hintText says how panels
// move, or why they cannot.
func (b *EditBar) Layout(gtx layout.Context, th *theme.Theme, cfg config.Config, plugins []string, hintText string) (layout.Dimensions, []Action) {
	var actions []Action
	if !gtx.Enabled() {
		b.Close()
	}
	var disabled []string
	for _, p := range cfg.Panels {
		if !p.IsEnabled() {
			disabled = append(disabled, p.ID)
		}
	}

	area := gtx.Constraints.Max
	width := widthDp(gtx)
	pad := gtx.Dp(contentPadding(width))
	content := max(min(area.X-2*pad, gtx.Dp(maxContent)), 0)
	padX, padY := gtx.Dp(14), gtx.Dp(11)
	inner := max(content-2*padX, 0)
	space := gtx.Dp(theme.Space8)
	// Each button at most a line wide: a long panel id is cut short.
	rgtx := gtx
	rgtx.Constraints = layout.Constraints{Max: image.Pt(inner, area.Y)}
	measure := func(label string) int {
		var m Button
		return record(rgtx, func(gtx layout.Context) layout.Dimensions { return m.Layout(gtx, th, label) }).dims.Size.X
	}
	// As many Enable buttons as fit on the line with those after them.
	n := len(disabled)
	if n > inlineEnable {
		n = inlineEnable - 1
	}
	for addW, moreW := measure("Add panel…"), measure("Enable…"); n > 0; n-- {
		w := addW
		if n < len(disabled) {
			w += space + moreW
		}
		for _, id := range disabled[:n] {
			w += space + measure("Enable "+id)
		}
		if w <= inner {
			break
		}
	}
	inline, rest := disabled[:n], disabled[n:]
	buttons := make(map[string]*Button, len(inline))
	order := make([]*Button, 0, len(inline)+2) // the buttons, as they are drawn
	for _, id := range inline {
		btn := b.enable[id]
		if btn == nil {
			btn = new(Button)
		}
		buttons[id] = btn
		order = append(order, btn)
	}
	b.enable = buttons
	if len(rest) > 0 {
		order = append(order, &b.more.button)
	}
	order = append(order, &b.add.button)
	if b.refocus > 0 {
		gtx.Execute(key.FocusCmd{Tag: &order[min(b.refocus, len(order))-1].Clickable})
		b.refocus = 0
	}
	if b.refocusMore {
		tag := &b.add.button.Clickable
		if len(rest) > 0 {
			tag = &b.more.button.Clickable
		}
		gtx.Execute(key.FocusCmd{Tag: tag})
		b.refocusMore = false
	}
	// Focus moves on only from a button the keyboard pressed: a click
	// leaves it alone.
	for k, id := range inline {
		if buttons[id].Clicked(gtx) {
			actions = append(actions, Action{Kind: ActionEnable, Panel: id})
			if gtx.Focused(&buttons[id].Clickable) {
				b.refocus = k + 1
			}
		}
	}
	if len(rest) == 0 {
		b.more.open = false
	}
	was := b.more.open || b.add.open
	if id, keyed, ok := b.more.update(gtx, &b.add); ok {
		actions = append(actions, Action{Kind: ActionEnable, Panel: id})
		b.refocusMore = keyed
	}
	if plugin, keyed, ok := b.add.update(gtx, &b.more); ok {
		actions = append(actions, Action{Kind: ActionAdd, Plugin: plugin})
		if keyed {
			gtx.Execute(key.FocusCmd{Tag: &b.add.button.Clickable})
		}
	}
	b.opened = !was && (b.more.open || b.add.open)

	var row []recorded
	for _, id := range inline {
		btn := b.enable[id]
		row = append(row, record(rgtx, func(gtx layout.Context) layout.Dimensions { return btn.Layout(gtx, th, "Enable "+id) }))
	}
	if len(rest) > 0 {
		row = append(row, record(rgtx, func(gtx layout.Context) layout.Dimensions { return b.more.button.Layout(gtx, th, "Enable…") }))
	}
	row = append(row, record(rgtx, func(gtx layout.Context) layout.Dimensions { return b.add.button.Layout(gtx, th, "Add panel…") }))
	rowW := -space
	for _, r := range row {
		rowW += r.dims.Size.X + space
	}
	// The hint and the buttons share a line if the hint fits beside
	// them in two lines at most; otherwise the buttons go below it. In
	// a short window, the hint keeps to one line, cut short.
	room := inner - rowW - gtx.Dp(theme.Space16)
	hintStyle := material.LabelStyle{Text: hintText, TextSize: theme.TextSmall, Color: th.Muted, Font: theme.Body, Shaper: th.Shaper}
	lines := 2
	if area.Y < gtx.Dp(shortWindow) {
		hintStyle.MaxLines, lines = 1, 1
	}
	hgtx := gtx
	hgtx.Constraints = layout.Constraints{Max: image.Pt(max(room, 0), area.Y)}
	hint := record(hgtx, hintStyle.Layout)
	beside := room > gtx.Dp(minColumn)/2 && hint.dims.Size.Y <= lines*gtx.Sp(theme.TextSmall)*3/2
	if !beside {
		hgtx.Constraints.Max.X = inner
		hint = record(hgtx, hintStyle.Layout)
	}

	// The buttons, flowed into lines inner wide: one, unless even Add
	// panel… alone is wider.
	type placed struct {
		r  recorded
		at image.Point
	}
	var flow []placed
	x, y, lineH := 0, 0, 0
	for _, r := range row {
		if x > 0 && x+r.dims.Size.X > inner {
			x, y, lineH = 0, y+lineH+space, 0
		}
		flow = append(flow, placed{r, image.Pt(x, y)})
		x += r.dims.Size.X + space
		lineH = max(lineH, r.dims.Size.Y)
	}
	flowH := y + lineH
	flowW := 0
	for _, f := range flow {
		flowW = max(flowW, f.at.X+f.r.dims.Size.X)
	}

	var height int
	var hintAt, flowAt image.Point
	if beside {
		height = max(hint.dims.Size.Y, flowH)
		hintAt = image.Pt(0, (height-hint.dims.Size.Y)/2)
		flowAt = image.Pt(inner-flowW, (height-flowH)/2)
	} else {
		height = hint.dims.Size.Y + space + flowH
		flowAt = image.Pt(0, hint.dims.Size.Y+space)
	}
	box := image.Rectangle{Max: image.Pt(content, height+2*padY)}
	origin := image.Pt((area.X-content)/2, pad)
	st := op.Offset(origin).Push(gtx.Ops)
	rr := gtx.Dp(theme.RadiusSmall)
	strokeRRect(gtx, box, rr, gtx.Dp(1), th.Border)
	hint.at(gtx, hintAt.Add(image.Pt(padX, padY)))
	b.boxes = b.boxes[:0]
	for _, f := range flow {
		at := flowAt.Add(f.at).Add(image.Pt(padX, padY))
		f.r.at(gtx, at)
		b.boxes = append(b.boxes, image.Rectangle{Min: at, Max: at.Add(f.r.dims.Size)}.Add(origin))
	}
	st.Pop()
	b.add.anchor = b.boxes[len(b.boxes)-1]
	if len(rest) > 0 {
		b.more.anchor = b.boxes[len(b.boxes)-2]
	}
	b.more.layout(gtx, th, rest)
	b.add.layout(gtx, th, plugins)
	return layout.Dimensions{Size: image.Pt(area.X, origin.Y+box.Max.Y)}, actions
}

// MenuOpened reports whether the last Layout opened one of its menus.
func (b *EditBar) MenuOpened() bool { return b.opened }

// Adding reports whether Add panel…'s menu is open.
func (b *EditBar) Adding() bool { return b.add.open }

// MenuShown reports whether one of its menus is open.
func (b *EditBar) MenuShown() bool { return b.more.open || b.add.open }

// Close closes the bar's menus, as when edit mode ends.
func (b *EditBar) Close() { b.more.open, b.add.open = false, false }

// popover is a button with a menu of items below it, which a click
// outside it or Escape closes, and which scrolls if the window is too
// short for it.
type popover struct {
	button  Button
	items   []Button
	labels  []string // the items' labels, as last drawn
	open    bool     // the menu is showing
	focus   bool     // move the focus into the menu on the next frame
	outside gesture.Click
	inside  gesture.Click
	list    layout.List
	anchor  image.Rectangle // where the button was drawn
	box     image.Rectangle // where the menu was drawn; for tests
}

// update reads the button and the menu, closing other when the menu
// opens, and reports the label of the item chosen, if one was: the item
// as drawn, though the list has changed since; and whether the
// keyboard chose it.
func (p *popover) update(gtx layout.Context, other *popover) (string, bool, bool) {
	if p.button.Clicked(gtx) {
		p.open = !p.open
		// Its first item takes the focus only if a key opened it, as a
		// click does not focus the button.
		p.focus = p.open && gtx.Focused(&p.button.Clickable)
		if p.open {
			other.open = false
		}
	}
	chosen, keyed, ok := "", false, false
	for i := range p.items {
		if p.items[i].Clicked(gtx) && i < len(p.labels) {
			chosen, keyed, ok = p.labels[i], gtx.Focused(&p.items[i].Clickable), true
			p.open = false
		}
	}
	if !p.open {
		return chosen, keyed, ok
	}
	for {
		if _, ok := p.inside.Update(gtx.Source); !ok {
			break
		}
	}
	for {
		e, ok := p.outside.Update(gtx.Source)
		if !ok {
			break
		}
		if e.Kind == gesture.KindClick {
			p.open = false
		}
	}
	for {
		e, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := e.(key.Event); ok && e.State == key.Press {
			p.open = false
			gtx.Execute(key.FocusCmd{Tag: &p.button.Clickable})
		}
	}
	return chosen, keyed, ok
}

// layout draws the menu of labels, if it is open, below the button and
// over everything else, on a backdrop that closes it on a click
// anywhere else. With no labels it says there are no plugins installed,
// the one menu that can be empty.
func (p *popover) layout(gtx layout.Context, th *theme.Theme, labels []string) {
	p.box = image.Rectangle{}
	if !p.open {
		return
	}
	p.labels = labels
	if len(p.items) != len(labels) {
		// The list changed while open: the focus goes to the item in
		// the place of the one that had it.
		focused := -1
		for i := range p.items {
			if gtx.Focused(&p.items[i].Clickable) {
				focused = i
			}
		}
		p.items = make([]Button, len(labels))
		if focused >= 0 && len(p.items) > 0 {
			gtx.Execute(key.FocusCmd{Tag: &p.items[min(focused, len(p.items)-1)].Clickable})
		}
	}
	pop := op.Record(gtx.Ops)
	defer func() { op.Defer(gtx.Ops, pop.Stop()) }()
	far := image.Rectangle{Min: image.Pt(-1<<20, -1<<20), Max: image.Pt(1<<20, 1<<20)}
	backdrop := clip.Rect(far).Push(gtx.Ops)
	p.outside.Add(gtx.Ops)
	backdrop.Pop()

	if p.focus && len(p.items) > 0 {
		gtx.Execute(key.FocusCmd{Tag: &p.items[0].Clickable})
	}
	p.focus = false
	pad := gtx.Dp(menuPad)
	area := gtx.Constraints.Max
	igtx := gtx
	igtx.Constraints = layout.Constraints{Max: image.Pt(max(min(gtx.Dp(280), area.X-2*pad), 0), area.Y)}
	var empty recorded
	widest, height := 0, 0
	if len(labels) == 0 {
		empty = record(igtx, func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(theme.Space8).Layout(gtx, label(th, "No plugins installed", theme.TextSmall, th.Muted, theme.Body).Layout)
		})
		widest, height = empty.dims.Size.X, empty.dims.Size.Y
	}
	for i := range p.items {
		p.items[i].Bare = true
		dims := record(igtx, func(gtx layout.Context) layout.Dimensions {
			return p.items[i].Layout(gtx, th, labels[i])
		}).dims
		widest = max(widest, dims.Size.X)
		height += dims.Size.Y
	}
	// As tall as its items, or the window less its padding.
	box := image.Rectangle{Max: image.Pt(widest+2*pad, min(height+2*pad, area.Y))}
	at := image.Pt(p.anchor.Max.X-box.Dx(), p.anchor.Max.Y+gtx.Dp(4))
	// Kept inside the window, moved up past its bottom edge.
	at.X = max(min(at.X, area.X-box.Dx()), 0)
	at.Y = max(min(at.Y, area.Y-box.Dy()), 0)
	p.box = box.Add(at)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rr := gtx.Dp(theme.RadiusSmall)
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(box, rr).Op(gtx.Ops))
	strokeRRect(gtx, box, rr, gtx.Dp(1), th.Border)
	card := clip.Rect(box).Push(gtx.Ops)
	p.inside.Add(gtx.Ops)
	card.Pop()
	if len(labels) == 0 {
		empty.at(gtx, image.Pt(pad, pad))
		return
	}
	defer op.Offset(image.Pt(pad, pad)).Push(gtx.Ops).Pop()
	lgtx := gtx
	lgtx.Constraints = layout.Constraints{Min: image.Pt(widest, 0), Max: image.Pt(widest, max(box.Dy()-2*pad, 0))}
	p.list.Axis = layout.Vertical
	p.list.Layout(lgtx, len(p.items), func(gtx layout.Context, i int) layout.Dimensions {
		gtx.Constraints.Min.X = widest
		return p.items[i].Layout(gtx, th, labels[i])
	})
}
