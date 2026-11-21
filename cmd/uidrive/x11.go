// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// display is a connection to the session's X server, with the host's
// window on it.
type display struct {
	conn   *xgb.Conn
	root   xproto.Window
	screen image.Point // the screen's size
	window xproto.Window
	keys   keymap
}

// openDisplay connects to the X display name and finds the host's
// window, waiting up to wait for it to appear.
func openDisplay(name string, wait time.Duration) (*display, error) {
	// It logs to stderr on its own; a refused connection's error says why.
	xgb.Logger = log.New(io.Discard, "", 0)
	conn, err := xgb.NewConnDisplay(name)
	if err != nil {
		return nil, fmt.Errorf("display %s (cookie from XAUTHORITY=%q): %w", name, os.Getenv("XAUTHORITY"), err)
	}
	if err := xtest.Init(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("display %s has no XTEST: %w", name, err)
	}
	setup := xproto.Setup(conn)
	s := setup.DefaultScreen(conn)
	d := &display{conn: conn, root: s.Root, screen: image.Pt(int(s.WidthInPixels), int(s.HeightInPixels))}
	if d.keys, err = readKeymap(conn, setup.MinKeycode, setup.MaxKeycode); err != nil {
		conn.Close()
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		if d.window, err = d.findWindow(d.root, 2); err == nil {
			return d, nil
		}
		if time.Now().After(deadline) {
			conn.Close()
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (d *display) Close() { d.conn.Close() }

// findWindow finds the mapped window titled "tessera" among parent's
// descendants, depth levels down.
func (d *display) findWindow(parent xproto.Window, depth int) (xproto.Window, error) {
	tree, err := xproto.QueryTree(d.conn, parent).Reply()
	if err != nil {
		return 0, err
	}
	for _, w := range tree.Children {
		attrs, err := xproto.GetWindowAttributes(d.conn, w).Reply()
		if err != nil || attrs.MapState != xproto.MapStateViewable {
			continue
		}
		name, err := xproto.GetProperty(d.conn, false, w, xproto.AtomWmName, xproto.GetPropertyTypeAny, 0, 256).Reply()
		if err == nil && string(name.Value) == "tessera" {
			return w, nil
		}
		if depth > 1 {
			if found, err := d.findWindow(w, depth-1); err == nil {
				return found, nil
			}
		}
	}
	return 0, errors.New("no tessera window on the display yet")
}

// fill moves the window to the screen's corner and gives it the whole
// screen, as no window manager does on a bare X server, and gives it
// the keyboard.
func (d *display) fill() error {
	err := xproto.ConfigureWindowChecked(d.conn, d.window,
		xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
		[]uint32{0, 0, uint32(d.screen.X), uint32(d.screen.Y)}).Check()
	if err != nil {
		return err
	}
	return d.focus()
}

func (d *display) focus() error {
	return xproto.SetInputFocusChecked(d.conn, xproto.InputFocusPointerRoot, d.window, xproto.TimeCurrentTime).Check()
}

// origin is the window's top left corner on the screen.
func (d *display) origin() (image.Point, error) {
	r, err := xproto.TranslateCoordinates(d.conn, d.window, d.root, 0, 0).Reply()
	if err != nil {
		return image.Point{}, err
	}
	return image.Pt(int(r.DstX), int(r.DstY)), nil
}

// fake sends one XTEST event and waits for the server to take it.
func (d *display) fake(kind, detail byte, at image.Point) error {
	if err := xtest.FakeInputChecked(d.conn, kind, detail, 0, d.root, int16(at.X), int16(at.Y), 0).Check(); err != nil {
		return err
	}
	_, err := xproto.GetInputFocus(d.conn).Reply() // a round trip: the event is in
	return err
}

// move puts the pointer at p, in window pixels.
func (d *display) move(p image.Point) error {
	o, err := d.origin()
	if err != nil {
		return err
	}
	return d.fake(xproto.MotionNotify, 0, p.Add(o))
}

// click moves the pointer to p and clicks button there, count times.
func (d *display) click(p image.Point, button byte, count int) error {
	if err := d.move(p); err != nil {
		return err
	}
	for range count {
		if err := d.fake(xproto.ButtonPress, button, image.Point{}); err != nil {
			return err
		}
		if err := d.fake(xproto.ButtonRelease, button, image.Point{}); err != nil {
			return err
		}
	}
	return nil
}

// press presses the keys of a combo, modifiers first, and releases them
// in reverse.
func (d *display) press(c combo) error {
	if err := d.focus(); err != nil {
		return err
	}
	var codes []xproto.Keycode
	for _, sym := range c.mods {
		code, _, ok := d.keys.code(sym)
		if !ok {
			return fmt.Errorf("no key for modifier %#x", sym)
		}
		codes = append(codes, code)
	}
	code, shift, ok := d.keys.code(c.key)
	if !ok {
		return fmt.Errorf("no key on the keyboard types %s", c.name)
	}
	if shift {
		shiftCode, _, _ := d.keys.code(keyShift)
		codes = append(codes, shiftCode)
	}
	codes = append(codes, code)
	for _, k := range codes {
		if err := d.fake(xproto.KeyPress, byte(k), image.Point{}); err != nil {
			return err
		}
	}
	for i := len(codes) - 1; i >= 0; i-- {
		if err := d.fake(xproto.KeyRelease, byte(codes[i]), image.Point{}); err != nil {
			return err
		}
	}
	return nil
}

// capture reads the window's pixels.
func (d *display) capture() (*image.RGBA, error) {
	geo, err := xproto.GetGeometry(d.conn, xproto.Drawable(d.window)).Reply()
	if err != nil {
		return nil, err
	}
	w, h := int(geo.Width), int(geo.Height)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// In bands, as one reply for a large window can pass the server's
	// request size limit.
	const band = 64
	for y := 0; y < h; y += band {
		bh := min(band, h-y)
		r, err := xproto.GetImage(d.conn, xproto.ImageFormatZPixmap, xproto.Drawable(d.window),
			0, int16(y), uint16(w), uint16(bh), 0xffffffff).Reply()
		if err != nil {
			return nil, err
		}
		if r.Depth != 24 && r.Depth != 32 || len(r.Data) < w*bh*4 {
			return nil, fmt.Errorf("window pixels in an unexpected format: depth %d, %d bytes", r.Depth, len(r.Data))
		}
		// 32 bits a pixel, little-endian BGRX.
		for i := 0; i < w*bh; i++ {
			s, t := r.Data[i*4:], img.Pix[(y*w+i)*4:]
			t[0], t[1], t[2], t[3] = s[2], s[1], s[0], 0xff
		}
	}
	return img, nil
}
