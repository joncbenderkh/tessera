// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// X keysyms uidrive presses by name.
const (
	keyShift   xproto.Keysym = 0xffe1
	keyControl xproto.Keysym = 0xffe3
	keyAlt     xproto.Keysym = 0xffe9
	keySuper   xproto.Keysym = 0xffeb
)

var namedKeys = map[string]xproto.Keysym{
	"backspace": 0xff08, "tab": 0xff09, "return": 0xff0d, "enter": 0xff0d,
	"escape": 0xff1b, "esc": 0xff1b, "delete": 0xffff, "home": 0xff50,
	"left": 0xff51, "up": 0xff52, "right": 0xff53, "down": 0xff54,
	"pageup": 0xff55, "pagedown": 0xff56, "end": 0xff57, "space": 0x20,
	"f1": 0xffbe, "f2": 0xffbf, "f3": 0xffc0, "f4": 0xffc1, "f5": 0xffc2,
	"f6": 0xffc3, "f7": 0xffc4, "f8": 0xffc5, "f9": 0xffc6, "f10": 0xffc7,
	"f11": 0xffc8, "f12": 0xffc9,
}

var modifiers = map[string]xproto.Keysym{
	"shift": keyShift, "ctrl": keyControl, "control": keyControl,
	"alt": keyAlt, "super": keySuper,
}

// combo is a key with the modifiers held while it is pressed.
type combo struct {
	name string
	mods []xproto.Keysym
	key  xproto.Keysym
}

// parseCombo reads a combo such as "ctrl+a", "shift+Tab", or "Return":
// modifiers joined to a key by "+", where the key is a name or one
// character.
func parseCombo(s string) (combo, error) {
	parts := strings.Split(s, "+")
	if strings.HasSuffix(s, "++") || s == "+" { // the plus key itself
		parts = append(parts[:len(parts)-2], "+")
	}
	c := combo{name: s}
	for _, m := range parts[:len(parts)-1] {
		sym, ok := modifiers[strings.ToLower(m)]
		if !ok {
			return combo{}, fmt.Errorf("%q: unknown modifier %q", s, m)
		}
		c.mods = append(c.mods, sym)
	}
	key := parts[len(parts)-1]
	if sym, ok := namedKeys[strings.ToLower(key)]; ok {
		c.key = sym
		return c, nil
	}
	if utf8.RuneCountInString(key) != 1 {
		return combo{}, fmt.Errorf("%q: unknown key %q", s, key)
	}
	r, _ := utf8.DecodeRuneInString(key)
	sym, ok := runeKeysym(r)
	if !ok {
		return combo{}, fmt.Errorf("%q: cannot type %q", s, r)
	}
	c.key = sym
	return c, nil
}

// typing is the combos that type text, one a character.
func typing(text string) ([]combo, error) {
	var combos []combo
	for _, r := range text {
		var sym xproto.Keysym
		switch r {
		case '\n':
			sym = namedKeys["return"]
		case '\t':
			sym = namedKeys["tab"]
		default:
			var ok bool
			if sym, ok = runeKeysym(r); !ok {
				return nil, fmt.Errorf("cannot type %q", r)
			}
		}
		combos = append(combos, combo{name: string(r), key: sym})
	}
	return combos, nil
}

// runeKeysym is the keysym of a printable Latin-1 character, which is
// its code point.
func runeKeysym(r rune) (xproto.Keysym, bool) {
	if r < 0x20 || r == 0x7f || r > 0xff || r >= 0x80 && r < 0xa0 {
		return 0, false
	}
	return xproto.Keysym(r), true
}

// keymap is the keyboard's keysyms by keycode: per keycode, the first
// is the unshifted symbol and the second the shifted one.
type keymap struct {
	min  xproto.Keycode
	per  int
	syms []xproto.Keysym
}

func readKeymap(conn *xgb.Conn, minCode, maxCode xproto.Keycode) (keymap, error) {
	r, err := xproto.GetKeyboardMapping(conn, minCode, byte(maxCode-minCode+1)).Reply()
	if err != nil {
		return keymap{}, err
	}
	return keymap{min: minCode, per: int(r.KeysymsPerKeycode), syms: r.Keysyms}, nil
}

// code is the keycode that types sym, and whether Shift must be held.
func (k keymap) code(sym xproto.Keysym) (xproto.Keycode, bool, bool) {
	for level, shift := range []bool{false, true} {
		if level >= k.per {
			break
		}
		for i := 0; i+level < len(k.syms); i += k.per {
			if k.syms[i+level] == sym {
				return k.min + xproto.Keycode(i/k.per), shift, true
			}
		}
	}
	return 0, false, false
}
