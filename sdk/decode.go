// SPDX-License-Identifier: GPL-3.0-or-later

package sdk

import (
	"encoding"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// checkKeys returns why value, as the JSON data model holds it, cannot
// be decoded into t, or "": the first key that names no field of t
// exactly, which encoding/json alone would match to a field whose name
// differs only in case, or a bare number or boolean for a field tagged
// ",string". dst, if valid, is the value of type t the key would be
// decoded into: encoding/json decodes into what a non-nil pointer, or an
// interface holding one, already points to, so those are followed.
// Types that decode themselves are not looked into.
func checkKeys(value any, dst reflect.Value, t reflect.Type, path string) string {
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
					if want := mapKeyProblem(k, t.Key()); want != "" {
						if path == "" {
							return fmt.Sprintf("key %s: want %s", k, want)
						}
						return fmt.Sprintf("setting %q: key %s: want %s", path, k, want)
					}
					if p := checkKeys(v[k], reflect.Value{}, t.Elem(), join(path, k)); p != "" {
						return p
					}
				}
			}
			return ""
		}
		fields := jsonFields(t)
		for _, k := range slices.Sorted(maps.Keys(v)) {
			f, ok := fields[k]
			var fv reflect.Value
			var err error
			if dst.IsValid() {
				fv, err = dst.FieldByIndexErr(f.index)
			}
			// encoding/json cannot allocate an embedded pointer to an
			// unexported struct, only fill one that is already set.
			if !ok || f.viaUnexported && (!dst.IsValid() || err != nil) {
				return fmt.Sprintf("unknown setting %q", join(path, k))
			}
			if f.quoted && !quotedValue(v[k], f.typ) {
				got := describeValue(jsonKind(v[k]))
				if s, ok := v[k].(string); ok {
					got = s
				}
				return fmt.Sprintf("setting %q: want %s in quotes, not %s",
					join(path, k), describeType(f.typ), got)
			}
			if p := checkKeys(v[k], fv, f.typ, join(path, k)); p != "" {
				return p
			}
		}
	case []any:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			if t.Kind() == reflect.Array {
				// encoding/json drops the elements past the array's length
				// unread.
				v = v[:min(len(v), t.Len())]
			}
			for i, e := range v {
				var ev reflect.Value
				if dst.IsValid() && i < dst.Len() {
					ev = dst.Index(i)
				}
				if p := checkKeys(e, ev, t.Elem(), path+"["+strconv.Itoa(i)+"]"); p != "" {
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
	// quoted is set for a number or boolean tagged ",string".
	quoted bool
	// viaUnexported is set for a field promoted through an embedded
	// pointer to an unexported struct.
	viaUnexported bool
}

// jsonFields maps the JSON names of struct t's fields to the fields,
// choosing among embedded structs' fields as encoding/json does: the
// shallowest wins, then the one with a json tag; a name still shared,
// even by one type embedded twice at the same depth, is left out, since
// encoding/json ignores it and rejects the setting itself. Each embedded
// struct type is looked into once.
func jsonFields(t reflect.Type) map[string]field {
	type candidate struct {
		field
		depth  int
		tagged bool
		count  int
	}
	found := map[string]*candidate{}
	visited := map[reflect.Type]bool{}
	copies := map[reflect.Type]int{t: 1}
	for depth, level := 0, []field{{typ: t}}; len(level) > 0; depth++ {
		var next []field
		nextCopies := map[reflect.Type]int{}
		for _, sf := range level {
			st := sf.typ
			if visited[st] {
				continue
			}
			visited[st] = true
			count := min(copies[st], 2)
			for i := range st.NumField() {
				index := append(slices.Clip(sf.index), i)
				f := st.Field(i)
				tag := f.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, opts, _ := strings.Cut(tag, ",")
				ft := f.Type
				for ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				embeddedStruct := f.Anonymous && ft.Kind() == reflect.Struct
				if embeddedStruct && name == "" {
					if nextCopies[ft]++; nextCopies[ft] == 1 {
						next = append(next, field{typ: ft, index: index,
							viaUnexported: sf.viaUnexported || !f.IsExported() && f.Type.Kind() == reflect.Pointer})
					}
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
				fd := field{f.Type, index, quotable(quotedType(f.Type)) && slices.Contains(strings.Split(opts, ","), "string"), sf.viaUnexported}
				c := found[name]
				switch {
				case c == nil:
					found[name] = &candidate{fd, depth, tagged, count}
				case c.depth < depth, c.tagged && !tagged:
				case tagged && !c.tagged:
					*c = candidate{fd, depth, tagged, count}
				default:
					c.count += count
				}
			}
		}
		level, copies = next, nextCopies
	}
	fields := map[string]field{}
	for name, c := range found {
		if c.count == 1 {
			fields[name] = c.field
		}
	}
	return fields
}

// quotable reports whether encoding/json honours ",string" on a field
// of kind t's and expects a quoted value for it: numbers, booleans, and
// strings.
func quotable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// quotedType is the type whose kind decides how encoding/json reads a
// field of type t tagged ",string": t, or what t points to if it is an
// unnamed pointer, but no further.
func quotedType(t reflect.Type) reflect.Type {
	if t.Name() == "" && t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}

// quotedValue reports whether v suits a field of type t tagged
// ",string": null, or a string, which for a string field must itself
// hold a JSON string.
func quotedValue(v any, t reflect.Type) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	var inner string
	t = quotedType(t)
	return t.Kind() != reflect.String || json.Unmarshal([]byte(s), &inner) == nil
}

// jsonKind names v's JSON type as an UnmarshalTypeError does.
func jsonKind(v any) string {
	switch v := v.(type) {
	case float64:
		return "number " + strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		return "bool"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	}
	return "string"
}

// failingKey returns the path of the deepest setting in value, as the
// JSON data model holds it, that does not decode into a fresh value of
// type t, or path if none of its settings is to blame. A struct's
// settings are tried one at a time inside the struct, so its tags
// still apply.
func failingKey(value any, t reflect.Type, path string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if p := reflect.PointerTo(t); p.Implements(jsonUnmarshaler) || p.Implements(textUnmarshaler) {
		return path
	}
	switch v := value.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(v)) {
			switch t.Kind() {
			case reflect.Struct:
				f, ok := jsonFields(t)[k]
				if ok && !decodes(map[string]any{k: v[k]}, t) {
					return failingKey(v[k], f.typ, join(path, k))
				}
			case reflect.Map:
				if !decodes(v[k], t.Elem()) {
					return failingKey(v[k], t.Elem(), join(path, k))
				}
			}
		}
	case []any:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			for i, e := range v {
				if t.Kind() == reflect.Array && i >= t.Len() {
					break
				}
				if !decodes(e, t.Elem()) {
					return failingKey(e, t.Elem(), path+"["+strconv.Itoa(i)+"]")
				}
			}
		}
	}
	return path
}

// mapKeyProblem names what a key of type t must be when encoding/json
// cannot read k as one, or returns "".
func mapKeyProblem(k string, t reflect.Type) string {
	if reflect.PointerTo(t).Implements(textUnmarshaler) {
		return ""
	}
	var err error
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		_, err = strconv.ParseInt(k, 10, t.Bits())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		_, err = strconv.ParseUint(k, 10, t.Bits())
	}
	if err == nil {
		return ""
	}
	if r := numberRange(t, "number "+k); r != "" {
		return r
	}
	return describeType(t)
}

// decodes reports whether encoding/json decodes value into a fresh
// value of type t.
func decodes(value any, t reflect.Type) bool {
	data, err := json.Marshal(value)
	return err == nil && json.Unmarshal(data, reflect.New(t).Interface()) == nil
}

// invalidTag returns the first struct field reachable from t, as
// Type.Field, whose json tag names it with a name encoding/json before
// Go 1.27 ignores, or "". Types that decode themselves are not looked
// into.
func invalidTag(t reflect.Type, seen map[reflect.Type]bool) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if seen[t] {
		return ""
	}
	seen[t] = true
	if p := reflect.PointerTo(t); p.Implements(jsonUnmarshaler) || p.Implements(textUnmarshaler) {
		return ""
	}
	switch t.Kind() {
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" || !f.IsExported() && !f.Anonymous {
				continue
			}
			if name, _, _ := strings.Cut(tag, ","); name != "" && !validTag(name) {
				return t.String() + "." + f.Name
			}
			if p := invalidTag(f.Type, seen); p != "" {
				return p
			}
		}
	case reflect.Slice, reflect.Array, reflect.Map:
		return invalidTag(t.Elem(), seen)
	}
	return ""
}

// validTag reports whether encoding/json before Go 1.27 takes name, from
// a json tag, as a field's name.
func validTag(name string) bool {
	for _, c := range name {
		if !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c) &&
			!unicode.IsLetter(c) && !unicode.IsDigit(c) {
			return false
		}
	}
	return true
}
