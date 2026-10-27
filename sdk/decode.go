// SPDX-License-Identifier: GPL-3.0-or-later

package sdk

import (
	"encoding"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// unknownKey returns the path of the first key in value, as the JSON
// data model holds it, that names no field of t exactly, or "".
// encoding/json alone would match such a key to a field whose name
// differs only in case. dst, if valid, is the value of type t the key
// would be decoded into: encoding/json decodes into what a non-nil
// pointer, or an interface holding one, already points to, so those are
// followed. Types that decode themselves are not looked into.
func unknownKey(value any, dst reflect.Value, t reflect.Type, path string) string {
	if t == nil {
		return ""
	}
	for {
		switch {
		case t.Kind() == reflect.Pointer:
			if dst.IsValid() && !dst.IsNil() {
				dst = dst.Elem()
			} else {
				dst = reflect.Value{}
			}
			t = t.Elem()
			continue
		case t.Kind() == reflect.Interface && dst.IsValid() && !dst.IsNil() &&
			dst.Elem().Kind() == reflect.Pointer && !dst.Elem().IsNil():
			dst = dst.Elem()
			t = dst.Type()
			continue
		}
		break
	}
	if p := reflect.PointerTo(t); p.Implements(jsonUnmarshaler) || p.Implements(textUnmarshaler) {
		return ""
	}
	switch v := value.(type) {
	case map[string]any:
		if t.Kind() != reflect.Struct {
			if t.Kind() == reflect.Map {
				for _, k := range slices.Sorted(maps.Keys(v)) {
					if p := unknownKey(v[k], reflect.Value{}, t.Elem(), join(path, k)); p != "" {
						return p
					}
				}
			}
			return ""
		}
		fields := jsonFields(t)
		for _, k := range slices.Sorted(maps.Keys(v)) {
			f, ok := fields[k]
			if !ok {
				return join(path, k)
			}
			var fv reflect.Value
			if dst.IsValid() {
				fv, _ = dst.FieldByIndexErr(f.index)
			}
			if p := unknownKey(v[k], fv, f.typ, join(path, k)); p != "" {
				return p
			}
		}
	case []any:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			for i, e := range v {
				var ev reflect.Value
				if dst.IsValid() && i < dst.Len() {
					ev = dst.Index(i)
				}
				if p := unknownKey(e, ev, t.Elem(), path); p != "" {
					return p
				}
			}
		}
	}
	return ""
}

var (
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
)

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// field is a struct field as encoding/json sees it.
type field struct {
	typ   reflect.Type
	index []int
}

// jsonFields maps the JSON names of struct t's fields to the fields,
// choosing among embedded structs' fields as encoding/json does: the
// shallowest wins, then the one with a json tag; a name still shared is
// left out, since encoding/json ignores it and rejects the setting
// itself. Each embedded struct type is looked into once.
func jsonFields(t reflect.Type) map[string]field {
	type candidate struct {
		field
		depth  int
		tagged bool
		count  int
	}
	found := map[string]*candidate{}
	visited := map[reflect.Type]bool{}
	for depth, level := 0, []field{{typ: t}}; len(level) > 0; depth++ {
		var next []field
		for _, sf := range level {
			st := sf.typ
			if visited[st] {
				continue
			}
			visited[st] = true
			for i := range st.NumField() {
				index := append(slices.Clip(sf.index), i)
				f := st.Field(i)
				tag := f.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, _, _ := strings.Cut(tag, ",")
				ft := f.Type
				for ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				embeddedStruct := f.Anonymous && ft.Kind() == reflect.Struct
				if embeddedStruct && name == "" {
					next = append(next, field{ft, index})
					continue
				}
				// encoding/json keeps an embedded struct, even unexported,
				// that a tag names.
				if !f.IsExported() && !embeddedStruct {
					continue
				}
				tagged := name != ""
				if !tagged {
					name = f.Name
				}
				c := found[name]
				switch {
				case c == nil:
					found[name] = &candidate{field{f.Type, index}, depth, tagged, 1}
				case c.depth < depth, c.tagged && !tagged:
				case tagged && !c.tagged:
					*c = candidate{field{f.Type, index}, depth, tagged, 1}
				default:
					c.count++
				}
			}
		}
		level = next
	}
	fields := map[string]field{}
	for name, c := range found {
		if c.count == 1 {
			fields[name] = c.field
		}
	}
	return fields
}
