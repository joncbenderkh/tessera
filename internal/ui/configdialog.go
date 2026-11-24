// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"image"
	"slices"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/ui/theme"
)

// ConfigDialog is the Configure… dialog: a form for a panel's settings,
// made from its plugin's config schema, with Save and Cancel. Each
// setting gets a control for its type; one the form cannot show, or
// whose value does not fit its type, is written in YAML. A setting is
// left out, for the plugin's default, until it is given a value, and
// again after its Default button; an emptied text field holds "", and
// an emptied list []. Save hands the
// settings to the caller (see Submitted), which checks them and either
// closes the dialog or shows what is wrong (see Failed). Cancel and
// Escape close it; a click outside does not, so edits are not lost.
type ConfigDialog struct {
	Panel, Plugin string
	// Original is the settings the dialog opened with, to tell whether
	// the file has changed them since.
	Original config.Settings

	open    bool
	busy    bool // Save was pressed; waiting for Failed or Close
	submit  bool
	fields  []*formField
	whole   *formField     // the settings as a whole, in YAML, for a schema the form cannot show
	extra   map[string]any // settings no field shows, kept as they are unless removed
	drop    map[string]*Button
	general []string // problems not at a field, and the plugin's answer
	list    layout.List
	save    Button
	cancel  Button
	focus   bool
}

// formField is one setting's control and what is wrong with it.
type formField struct {
	spec     *fieldSpec
	kind     fieldKind // spec.kind, or kindRaw for a value that does not fit it
	editor   widget.Editor
	set      bool // kindText, kindBool, kindList, kindObjects: given a value
	on       bool // kindBool: the value
	toggle   Button
	reset    Button // Default: leaves the setting out
	choice   int    // kindEnum: the index in spec.enum, or -1 for unset
	choices  []Button
	items    []*formItem // kindList, kindObjects
	add      Button
	problems []string
}

// formItem is one item of an array setting.
type formItem struct {
	editor   widget.Editor // kindList
	fields   []*formField  // kindObjects
	remove   Button
	problems []string
}

// Open shows the form for panel's settings, made from its plugin's
// config schema; one the host cannot compile, and so does not check,
// gives the settings in YAML.
func (d *ConfigDialog) Open(panel, plugin, schema string, settings config.Settings) {
	*d = ConfigDialog{Panel: panel, Plugin: plugin, Original: settings, open: true, focus: true}
	d.list.Axis = layout.Vertical
	specs, ok := parseForm(schema)
	if _, err := config.CompileSchema(schema); err != nil {
		specs, ok = nil, false
	}
	if !ok {
		v := mapOrNil(settings)
		d.whole = newField(&fieldSpec{kind: kindRaw, description: "The settings, in YAML."}, v, v != nil)
		return
	}
	for _, s := range specs {
		// A null setting, as a blank value in YAML is, means the
		// plugin's default, as a missing one does.
		v := settings[s.name]
		d.fields = append(d.fields, newField(s, v, v != nil))
	}
	d.extra = extraSettings(specs, settings)
	d.drop = map[string]*Button{}
	for k := range d.extra {
		d.drop[k] = &Button{Description: "Remove the setting " + k}
	}
}

func mapOrNil(s config.Settings) any {
	if len(s) == 0 {
		return nil
	}
	return map[string]any(s)
}

// newField is the control for spec showing v, if present.
func newField(spec *fieldSpec, v any, present bool) *formField {
	f := &formField{spec: spec, kind: spec.kind, choice: -1, set: present}
	if present && (v == nil || !valueFits(spec.kind, spec, v)) {
		f.kind = kindRaw
	}
	switch f.kind {
	case kindText, kindInteger, kindNumber:
		f.editor.SingleLine, f.editor.Submit = true, true // Return adds nothing to the value
		if present {
			f.editor.SetText(scalarText(v))
		}
	case kindRaw:
		if present {
			text, err := config.FormatValue(v)
			if err != nil {
				text = fmt.Sprint(v)
			}
			f.editor.SetText(text)
		}
	case kindBool:
		f.on, f.set = v == true, v != nil
		if !f.set && spec.hasDef {
			f.on = spec.def == true
		}
	case kindEnum:
		f.choice = slices.IndexFunc(spec.enum, func(e any) bool { return v != nil && sameValue(e, v) })
		f.choices = make([]Button, len(spec.enum))
	case kindList, kindObjects:
		items, _ := v.([]any)
		for _, e := range items {
			f.items = append(f.items, newItem(spec, e))
		}
	}
	return f
}

// newItem is an item of the array spec, showing v.
func newItem(spec *fieldSpec, v any) *formItem {
	it := &formItem{}
	if spec.kind == kindList {
		it.editor.SingleLine, it.editor.Submit = true, true // Return adds nothing to the value
		it.editor.SetText(scalarText(v))
		return it
	}
	m, _ := v.(map[string]any)
	for _, p := range spec.props {
		pv, present := m[p.name]
		it.fields = append(it.fields, newField(p, pv, present))
	}
	return it
}

// IsOpen reports whether the dialog is showing.
func (d *ConfigDialog) IsOpen() bool { return d.open }

// Close hides the dialog.
func (d *ConfigDialog) Close() { d.open, d.busy = false, false }

// Busy reports whether Save was pressed and no answer has come.
func (d *ConfigDialog) Busy() bool { return d.busy }

// Submitted returns the settings Save asked for, once per Save, if
// the form has no problems of its own. The dialog then waits, Busy,
// for Close or Failed.
func (d *ConfigDialog) Submitted() (config.Settings, bool) {
	if !d.submit {
		return nil, false
	}
	d.submit = false
	settings, problems := d.collect()
	if problems != nil {
		d.show(problems, "")
		return nil, false
	}
	d.show(nil, "")
	d.busy = true
	return settings, true
}

// Failed shows why the settings were not saved: problems at the
// settings they name, and message above the buttons.
func (d *ConfigDialog) Failed(problems []config.Problem, message string) {
	d.busy = false
	d.show(problems, message)
}

// collect reads the form: the settings it holds, with those no field
// shows, and the problems in what was typed.
func (d *ConfigDialog) collect() (config.Settings, []config.Problem) {
	var problems []config.Problem
	if d.whole != nil {
		v, set, err := d.whole.value(nil, &problems)
		if !set || v == nil || err {
			return nil, problems
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, []config.Problem{{Message: "must be a mapping of settings"}}
		}
		return config.Settings(m), problems
	}
	settings := config.Settings{}
	for _, f := range d.fields {
		if v, set, _ := f.value([]string{f.spec.name}, &problems); set {
			settings[f.spec.name] = v
		}
	}
	return withExtra(settings, d.extra), problems
}

// value is the field's value, whether it is set, and whether it has a
// problem, which it adds to problems with path.
func (f *formField) value(path []string, problems *[]config.Problem) (any, bool, bool) {
	report := func(at []string, msg string) {
		*problems = append(*problems, config.Problem{Path: slices.Clone(at), Message: msg})
	}
	switch f.kind {
	case kindText, kindInteger, kindNumber:
		v, set, msg := parseScalar(f.kind, f.editor.Text())
		if msg != "" {
			report(path, msg)
			return nil, set, true
		}
		if f.kind == kindText && !set && f.set {
			return "", true, false
		}
		return v, set, false
	case kindRaw:
		v, err := config.ParseValue(f.editor.Text())
		if err != nil {
			report(path, "not valid YAML: "+err.Error())
			return nil, true, true
		}
		return v, v != nil, false // null, blank, or a comment: the default
	case kindBool:
		return f.on, f.set, false
	case kindEnum:
		if f.choice < 0 {
			return nil, false, false
		}
		return f.spec.enum[f.choice], true, false
	}
	items := []any{}
	bad := false
	for i, it := range f.items {
		at := append(slices.Clip(path), strconv.Itoa(i))
		if f.kind == kindList {
			// Every item stays, so the schema's problems keep their
			// indexes: a blank text item is "", a blank number wrong.
			v, set, msg := parseScalar(f.spec.item.kind, it.editor.Text())
			switch {
			case msg != "":
				report(at, msg)
				bad = true
			case !set && f.spec.item.kind == kindText:
				items = append(items, "")
			case !set:
				report(at, "give a number, or remove the item")
				bad = true
			default:
				items = append(items, v)
			}
			continue
		}
		obj := map[string]any{}
		for _, sub := range it.fields {
			v, set, b := sub.value(append(slices.Clip(at), sub.spec.name), problems)
			bad = bad || b
			if set {
				obj[sub.spec.name] = v
			}
		}
		items = append(items, obj)
	}
	return items, f.set || len(items) > 0, bad
}

// isSet reports whether the field gives its setting a value, which
// its Default button takes away.
func (f *formField) isSet() bool {
	switch f.kind {
	case kindInteger, kindNumber:
		return strings.TrimSpace(f.editor.Text()) != ""
	case kindRaw:
		v, err := config.ParseValue(f.editor.Text())
		return err != nil || v != nil
	case kindText:
		return f.set || f.editor.Text() != ""
	case kindEnum:
		return f.choice >= 0
	}
	return f.set || len(f.items) > 0
}

// unset leaves the setting out, for the plugin's default.
func (f *formField) unset(gtx layout.Context) {
	f.set, f.choice, f.items = false, -1, nil
	f.on = f.spec.hasDef && f.spec.def == true
	f.editor.SetText("")
	for {
		if _, ok := f.editor.Update(gtx); !ok {
			break // SetText's change is not an edit
		}
	}
}

// show places each problem at the field, item, or item's setting its
// path names, and the rest, and message, above the buttons.
func (d *ConfigDialog) show(problems []config.Problem, message string) {
	all := append(slices.Clone(d.fields), d.whole)
	for _, f := range all {
		if f == nil {
			continue
		}
		f.problems = nil
		for _, it := range f.items {
			it.problems = nil
			for _, sub := range it.fields {
				sub.problems = nil
			}
		}
	}
	d.general = nil
	for _, p := range problems {
		if !d.place(p) {
			d.general = append(d.general, p.String())
		}
	}
	if message != "" {
		d.general = append(d.general, message)
	}
}

// place shows p at the control its path names, if there is one.
func (d *ConfigDialog) place(p config.Problem) bool {
	if d.whole != nil {
		d.whole.problems = append(d.whole.problems, p.String())
		return true
	}
	if len(p.Path) == 0 {
		return false
	}
	i := slices.IndexFunc(d.fields, func(f *formField) bool { return f.spec.name == p.Path[0] })
	if i < 0 {
		return false
	}
	f := d.fields[i]
	rest := p.Path[1:]
	if len(rest) > 0 && (f.kind == kindList || f.kind == kindObjects) {
		if n, err := strconv.Atoi(rest[0]); err == nil && n >= 0 && n < len(f.items) {
			it := f.items[n]
			if len(rest) > 1 && f.kind == kindObjects {
				j := slices.IndexFunc(it.fields, func(s *formField) bool { return s.spec.name == rest[1] })
				if j >= 0 {
					it.fields[j].problems = append(it.fields[j].problems, problemText(rest[2:], p.Message))
					return true
				}
			}
			it.problems = append(it.problems, problemText(rest[1:], p.Message))
			return true
		}
	}
	f.problems = append(f.problems, problemText(rest, p.Message))
	return true
}

// problemText is msg, led by the rest of its path, if any.
func problemText(rest []string, msg string) string {
	if len(rest) == 0 {
		return msg
	}
	return strings.TrimPrefix(config.PathString(rest), "settings") + ": " + msg
}

// Layout draws the dialog, if open, over gtx's constraints.
func (d *ConfigDialog) Layout(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	if !d.open {
		return layout.Dimensions{}
	}
	// While Save is under way, the plugin may be running the settings
	// tried, so the dialog waits for the answer.
	if d.cancel.Clicked(gtx) && !d.busy {
		d.Close()
	}
	if d.save.Clicked(gtx) && !d.busy {
		d.submit = true
	}
	for {
		e, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := e.(key.Event); ok && e.State == key.Press && !d.busy {
			d.Close()
		}
	}
	if d.busy {
		// The settings being tried are the ones saved: no edits
		// meanwhile, which would be lost.
		gtx = gtx.Disabled()
	}
	d.update(gtx)
	if !d.open {
		gtx.Execute(op.InvalidateCmd{})
		return layout.Dimensions{}
	}
	if d.submit {
		gtx.Execute(op.InvalidateCmd{}) // a frame to show Saving… while the caller tries them
	}
	if d.focus {
		d.focus = false
		gtx.Execute(key.FocusCmd{Tag: d.firstFocus()})
	}

	area := gtx.Constraints.Max
	fillRect(gtx, image.Rectangle{Max: area}, theme.Scrim)
	// The backdrop takes the clicks outside the card.
	backdrop := clip.Rect{Max: area}.Push(gtx.Ops)
	event.Op(gtx.Ops, &d.list)
	backdrop.Pop()
	modalLayer(gtx, image.Rectangle{Max: area})
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &d.list, Kinds: pointer.Press | pointer.Release}); !ok {
			break
		}
	}

	pad := gtx.Dp(theme.Space24)
	width := min(gtx.Dp(600), area.X*9/10)
	inner := gtx
	inner.Constraints = layout.Constraints{Max: image.Pt(width-2*pad, area.Y*9/10-2*pad)}
	content := record(inner, func(gtx layout.Context) layout.Dimensions { return d.content(gtx, th) })
	size := image.Pt(width, content.dims.Size.Y+2*pad)
	defer op.Offset(area.Sub(size).Div(2)).Push(gtx.Ops).Pop()
	card := image.Rectangle{Max: size}
	rr := gtx.Dp(theme.RadiusLarge)
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(card, rr).Op(gtx.Ops))
	strokeRRect(gtx, card, rr, gtx.Dp(1), th.Border)
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: d, Kinds: pointer.Press | pointer.Release}); !ok {
			break
		}
	}
	cardArea := clip.Rect(card).Push(gtx.Ops)
	event.Op(gtx.Ops, d)
	cardArea.Pop()
	content.at(gtx, image.Pt(pad, pad))
	return layout.Dimensions{Size: area}
}

// firstFocus is what has the focus when the dialog opens: the first
// text field, or Save.
func (d *ConfigDialog) firstFocus() event.Tag {
	if d.whole != nil {
		return &d.whole.editor
	}
	for _, f := range d.fields {
		switch f.kind {
		case kindText, kindInteger, kindNumber, kindRaw:
			return &f.editor
		}
	}
	return &d.save.Clickable
}

// update applies the clicks on the form's buttons.
func (d *ConfigDialog) update(gtx layout.Context) {
	var fields []*formField
	fields = append(fields, d.fields...)
	for _, f := range d.fields {
		for _, it := range f.items {
			fields = append(fields, it.fields...)
		}
	}
	for _, f := range fields {
		for {
			e, ok := f.editor.Update(gtx)
			if !ok {
				break
			}
			if _, ok := e.(widget.ChangeEvent); ok {
				f.set = true // even emptied: "" is a value
			}
		}
		if f.reset.Clicked(gtx) {
			f.unset(gtx)
			gtx.Execute(op.InvalidateCmd{})
		}
		if f.toggle.Clicked(gtx) {
			f.on, f.set = !f.on, true
		}
		for i := range f.choices {
			if f.choices[i].Clicked(gtx) {
				if f.choice == i {
					f.choice = -1 // unset: the plugin's default applies
				} else {
					f.choice = i
				}
			}
		}
	}
	for k, b := range d.drop {
		if b.Clicked(gtx) {
			delete(d.extra, k)
			delete(d.drop, k)
			gtx.Execute(op.InvalidateCmd{})
		}
	}
	for _, f := range d.fields {
		if f.add.Clicked(gtx) && (f.spec.maxItems < 0 || len(f.items) < f.spec.maxItems) {
			f.items, f.set = append(f.items, newItem(f.spec, nil)), true
			gtx.Execute(op.InvalidateCmd{})
		}
		for i := 0; i < len(f.items); i++ {
			if f.items[i].remove.Clicked(gtx) {
				f.items = slices.Delete(f.items, i, i+1)
				i--
			}
		}
	}
}

// content is the headings, the fields, which scroll, and the problems
// and buttons at the bottom, always shown.
func (d *ConfigDialog) content(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	width := gtx.Constraints.Max.X
	foot := record(gtx, func(gtx layout.Context) layout.Dimensions { return d.foot(gtx, th) })
	head := record(gtx, func(gtx layout.Context) layout.Dimensions {
		title := label(th, "Configure "+d.Panel, theme.TextDialog, th.Fg, font.Font{Typeface: theme.Body.Typeface, Weight: font.Bold})
		title.MaxLines = 2
		sub := label(th, "Plugin "+d.Plugin, theme.TextSmall, th.Muted, theme.Body)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: 10}.Layout(gtx, title.Layout)
			}),
			layout.Rigid(sub.Layout),
		)
	})
	rows := d.rows(th)
	body := gtx
	body.Constraints = layout.Constraints{Max: image.Pt(width, max(gtx.Constraints.Max.Y-head.dims.Size.Y-foot.dims.Size.Y, 0))}
	list := record(body, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		defer viewport(gtx, image.Rectangle{Max: gtx.Constraints.Max})()
		return d.list.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
			return layout.Inset{Top: 18}.Layout(gtx, rows[i])
		})
	})
	head.at(gtx, image.Point{})
	y := head.dims.Size.Y
	clipped := clip.Rect{Min: image.Pt(0, y), Max: image.Pt(width, y+list.dims.Size.Y)}.Push(gtx.Ops)
	list.at(gtx, image.Pt(0, y))
	clipped.Pop()
	y += list.dims.Size.Y
	foot.at(gtx, image.Pt(0, y))
	return layout.Dimensions{Size: image.Pt(width, y+foot.dims.Size.Y)}
}

// rows is a widget per field, or a note when the panel takes no
// settings.
func (d *ConfigDialog) rows(th *theme.Theme) []layout.Widget {
	if d.whole != nil {
		return []layout.Widget{func(gtx layout.Context) layout.Dimensions { return d.whole.layout(gtx, th, "Settings") }}
	}
	if len(d.fields) == 0 && len(d.extra) == 0 {
		return []layout.Widget{label(th, "This panel has no settings.", theme.TextBase, th.Muted, theme.Body).Layout}
	}
	var rows []layout.Widget
	for _, f := range d.fields {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions { return f.layout(gtx, th, "") })
	}
	if len(d.extra) > 0 {
		rows = append(rows, label(th, "Not in the plugin's schema, kept as they are unless removed:", theme.TextSmall, th.Muted, theme.Body).Layout)
		for _, k := range slicesSorted(d.extra) {
			text, err := config.FormatValue(d.extra[k])
			if err != nil {
				text = fmt.Sprint(d.extra[k])
			}
			text = k + ": " + strings.ReplaceAll(text, "\n", " ")
			b := d.drop[k]
			rows = append(rows, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, label(th, text, theme.TextBase, th.Fg, theme.Mono).Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return b.Layout(gtx, th, "Remove") }),
				)
			})
		}
	}
	return rows
}

func slicesSorted(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// foot is the problems not at a field, and Cancel and Save.
func (d *ConfigDialog) foot(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	var children []layout.FlexChild
	for _, msg := range d.general {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 8}.Layout(gtx, label(th, msg, theme.TextSmall, th.Error, theme.Body).Layout)
		}))
	}
	children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 18}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Spacing: layout.SpaceStart}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if d.busy {
							gtx = gtx.Disabled()
						}
						return d.cancel.Layout(gtx, th, "Cancel")
					}),
					layout.Rigid(layout.Spacer{Width: theme.Space8}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						text := "Save"
						if d.busy {
							text = "Saving…"
							gtx = gtx.Disabled()
						}
						return d.save.Layout(gtx, th, text)
					}),
				)
			})
		})
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// layout draws the field: its name, description, control, and
// problems. name, if set, replaces the setting's name.
func (f *formField) layout(gtx layout.Context, th *theme.Theme, name string) layout.Dimensions {
	if name == "" {
		name = f.spec.name
		if f.spec.title != "" {
			name = f.spec.title
		}
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	var children []layout.FlexChild
	heading := name
	if f.spec.required {
		heading += " (required)"
	}
	title := label(th, heading, theme.TextBase, th.Fg, font.Font{Typeface: theme.Body.Typeface, Weight: font.Bold})
	children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		if f.spec.name == "" || !f.isSet() {
			return title.Layout(gtx)
		}
		f.reset.Description = "Use the default for " + name
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, title.Layout),
			layout.Rigid(layout.Spacer{Width: theme.Space8}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = 0
				return f.reset.Layout(gtx, th, "Default")
			}),
		)
	}))
	if note := f.note(); note != "" {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 2}.Layout(gtx, label(th, note, theme.TextSmall, th.Muted, theme.Body).Layout)
		}))
	}
	children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return f.control(gtx, th) })
	}))
	for _, msg := range f.problems {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 4}.Layout(gtx, label(th, msg, theme.TextSmall, th.Error, theme.Body).Layout)
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// note is the field's description, with its default and, for one
// written in YAML, why.
func (f *formField) note() string {
	parts := []string{}
	if f.spec.description != "" {
		parts = append(parts, f.spec.description)
	}
	if f.spec.hasDef {
		text, err := scalarText(f.spec.def), error(nil)
		if !isScalar(f.spec.def) {
			text, err = config.FormatValue(f.spec.def)
		}
		if err == nil && text != "" && !strings.Contains(text, "\n") {
			parts = append(parts, "Default: "+text+".")
		}
	}
	if f.kind == kindRaw && f.spec.kind != kindRaw {
		parts = append(parts, "Its value does not fit the form, so it is written in YAML.")
	} else if f.kind == kindRaw && f.spec.name != "" {
		parts = append(parts, "Written in YAML.")
	}
	return strings.Join(parts, " ")
}

// control draws the field's input.
func (f *formField) control(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	switch f.kind {
	case kindText, kindInteger, kindNumber:
		hint, name := "", f.spec.label()
		switch {
		case f.kind == kindText && f.set:
			hint = `""` // emptied, it holds "", not the default
		case f.spec.hasDef:
			hint = scalarText(f.spec.def)
			name += ", default " + hint
		}
		return editorBox(gtx, th, &f.editor, hint, name, false)
	case kindRaw:
		return editorBox(gtx, th, &f.editor, "", f.spec.label(), true)
	case kindBool:
		gtx.Constraints.Min.X = 0
		f.toggle.Toggle, f.toggle.Pressed = true, f.on
		text := "Off"
		if f.on {
			text = "On"
		}
		return f.toggle.Layout(gtx, th, text)
	case kindEnum:
		var items []layout.Widget
		labels := enumLabels(f.spec.enum)
		for i := range f.spec.enum {
			b := &f.choices[i]
			b.Toggle, b.Pressed = true, f.choice == i
			text := labels[i]
			items = append(items, func(gtx layout.Context) layout.Dimensions { return b.Layout(gtx, th, text) })
		}
		return flow(gtx, gtx.Dp(theme.Space8), items)
	}
	return f.itemsLayout(gtx, th)
}

// itemsLayout draws an array's items, each with Remove, and Add.
func (f *formField) itemsLayout(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	var children []layout.FlexChild
	for i, it := range f.items {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return it.layout(gtx, th, f, i)
			})
		}))
	}
	if f.spec.maxItems < 0 || len(f.items) < f.spec.maxItems {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = 0
			return f.add.Layout(gtx, th, "Add")
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// layout draws item i of f.
func (it *formItem) layout(gtx layout.Context, th *theme.Theme, f *formField, i int) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	it.remove.Description = fmt.Sprintf("Remove %s item %d", f.spec.label(), i+1)
	var children []layout.FlexChild
	if f.kind == kindList {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return editorBox(gtx, th, &it.editor, "", fmt.Sprintf("%s item %d", f.spec.label(), i+1), false)
				}),
				layout.Rigid(layout.Spacer{Width: theme.Space8}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = 0
					return it.remove.Layout(gtx, th, "Remove")
				}),
			)
		}))
	} else {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return group(gtx, th, func(gtx layout.Context) layout.Dimensions {
				var rows []layout.FlexChild
				for j, sub := range it.fields {
					rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						top := unit.Dp(0)
						if j > 0 {
							top = 12
						}
						return layout.Inset{Top: top}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return sub.layout(gtx, th, "")
						})
					}))
				}
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						gtx.Constraints.Min.X = 0
						return it.remove.Layout(gtx, th, "Remove")
					})
				}))
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
			})
		}))
	}
	for _, msg := range it.problems {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 4}.Layout(gtx, label(th, msg, theme.TextSmall, th.Error, theme.Body).Layout)
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// group draws w on an inset panel, as an array's object items are.
func group(gtx layout.Context, th *theme.Theme, w layout.Widget) layout.Dimensions {
	pad := gtx.Dp(theme.Space12)
	inner := gtx
	inner.Constraints = layout.Constraints{Max: gtx.Constraints.Max.Sub(image.Pt(2*pad, 2*pad))}
	inner.Constraints.Min.X = inner.Constraints.Max.X
	content := record(inner, w)
	size := content.dims.Size.Add(image.Pt(2*pad, 2*pad))
	r := image.Rectangle{Max: size}
	paint.FillShape(gtx.Ops, th.Inset, clip.UniformRRect(r, gtx.Dp(theme.RadiusSmall)).Op(gtx.Ops))
	content.at(gtx, image.Pt(pad, pad))
	return layout.Dimensions{Size: size}
}

// editorBox draws e in a bordered box as wide as gtx allows, with hint
// while it is empty, in the monospaced face for YAML.
func editorBox(gtx layout.Context, th *theme.Theme, e *widget.Editor, hint, name string, mono bool) layout.Dimensions {
	pad := gtx.Dp(theme.Space8)
	inner := gtx
	inner.Constraints = layout.Constraints{Max: image.Pt(max(gtx.Constraints.Max.X-2*pad, 0), gtx.Constraints.Max.Y)}
	inner.Constraints.Min.X = inner.Constraints.Max.X
	face := theme.Body
	if mono {
		face = theme.Mono
	}
	mt := &material.Theme{Shaper: th.Shaper}
	style := material.Editor(mt, e, hint)
	style.Font, style.TextSize = face, theme.TextBase
	style.Color, style.HintColor = th.Fg, th.Muted
	style.SelectionColor = mix(th.Accent, th.Surface, 0.35)
	content := record(inner, style.Layout)
	size := image.Pt(gtx.Constraints.Max.X, content.dims.Size.Y+2*pad)
	r := image.Rectangle{Max: size}
	rr := gtx.Dp(theme.RadiusSmall)
	paint.FillShape(gtx.Ops, th.Bg, clip.UniformRRect(r, rr).Op(gtx.Ops))
	border := th.Border
	if gtx.Focused(e) {
		border = th.Accent
	}
	strokeRRect(gtx, r, rr, gtx.Dp(1), border)
	content.at(gtx, image.Pt(pad, pad))
	// The editor's own semantics sit with its input handlers, which
	// uidebug's router does not see (see semanticArea).
	semanticArea(gtx, r, func() {
		semantic.Editor.Add(gtx.Ops)
		semantic.LabelOp(e.Text()).Add(gtx.Ops)
		// Named by its setting, so even an empty one can be found.
		semantic.DescriptionOp(name).Add(gtx.Ops)
	})
	return layout.Dimensions{Size: size}
}

// flow lays items out left to right, starting a new row where the next
// does not fit, gap apart both ways.
func flow(gtx layout.Context, gap int, items []layout.Widget) layout.Dimensions {
	free := gtx
	free.Constraints.Min = image.Point{}
	var x, y, rowH, width int
	for _, w := range items {
		r := record(free, w)
		if x > 0 && x+r.dims.Size.X > gtx.Constraints.Max.X {
			x, y = 0, y+rowH+gap
			rowH = 0
		}
		r.at(gtx, image.Pt(x, y))
		x += r.dims.Size.X + gap
		width = max(width, x-gap)
		rowH = max(rowH, r.dims.Size.Y)
	}
	return layout.Dimensions{Size: image.Pt(width, y+rowH)}
}
