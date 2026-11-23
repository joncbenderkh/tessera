// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"slices"
	"testing"

	"github.com/jezek/xgb/xproto"
)

func TestParseCombo(t *testing.T) {
	for _, c := range []struct {
		in   string
		mods []xproto.Keysym
		key  xproto.Keysym
	}{
		{"Return", nil, 0xff0d},
		{"ctrl+a", []xproto.Keysym{keyControl}, 'a'},
		{"ctrl+A", []xproto.Keysym{keyControl}, 'a'},
		{"A", nil, 'A'},
		{"Ctrl+Shift+Tab", []xproto.Keysym{keyControl, keyShift}, 0xff09},
		{"ctrl++", []xproto.Keysym{keyControl}, '+'},
		{"+", nil, '+'},
		{"é", nil, 0xe9},
	} {
		got, err := parseCombo(c.in)
		if err != nil || got.key != c.key || !slices.Equal(got.mods, c.mods) {
			t.Errorf("parseCombo(%q) = %+v, %v; want mods %v key %#x", c.in, got, err, c.mods, c.key)
		}
	}
	for _, in := range []string{"hyper+a", "Retrun", "ctrl+…", ""} {
		if _, err := parseCombo(in); err == nil {
			t.Errorf("parseCombo(%q) took it", in)
		}
	}
}

func TestTyping(t *testing.T) {
	combos, err := typing("A b\n")
	if err != nil {
		t.Fatal(err)
	}
	var keys []xproto.Keysym
	for _, c := range combos {
		keys = append(keys, c.key)
	}
	if want := []xproto.Keysym{'A', ' ', 'b', 0xff0d}; !slices.Equal(keys, want) {
		t.Errorf("typing = %#x, want %#x", keys, want)
	}
	if _, err := typing("UTC−3"); err == nil {
		t.Error("typing took U+2212, which no Latin-1 key types")
	}
}

func TestKeymapCode(t *testing.T) {
	// Keycodes 10 and 11, two keysyms each: a A, Return.
	k := keymap{min: 10, per: 2, syms: []xproto.Keysym{'a', 'A', 0xff0d, 0}}
	for _, c := range []struct {
		sym   xproto.Keysym
		code  xproto.Keycode
		shift bool
		ok    bool
	}{
		{'a', 10, false, true},
		{'A', 10, true, true},
		{0xff0d, 11, false, true},
		{'b', 0, false, false},
	} {
		code, shift, ok := k.code(c.sym)
		if code != c.code || shift != c.shift || ok != c.ok {
			t.Errorf("code(%#x) = %d, %v, %v; want %d, %v, %v", c.sym, code, shift, ok, c.code, c.shift, c.ok)
		}
	}
}
