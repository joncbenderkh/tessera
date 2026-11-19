// SPDX-License-Identifier: GPL-3.0-or-later

// Command uidrive drives the host's window for tests: it runs the host
// on a private Xvfb display with a throwaway copy of a config, finds
// buttons, labels, and editors by name through the host's semantic
// tree, and clicks, types, and takes screenshots there. A session
// lasts from start to stop, across uidrive's runs.
//
//	go run ./cmd/build --dest /tmp/plugins
//	go run ./cmd/uidrive start --plugins /tmp/plugins --config my.yaml
//	go run ./cmd/uidrive click "Options for World clock"
//	go run ./cmd/uidrive click Configure…
//	go run ./cmd/uidrive shot
//	go run ./cmd/uidrive stop
package main

import (
	"cmp"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
	"github.com/joncbenderkh/tessera/internal/uidebug"
)

// settle is how long an action is given to take effect before uidrive
// returns.
const settle = 250 * time.Millisecond

func main() {
	var name string
	root := &cobra.Command{
		Use:           "uidrive",
		Short:         "Drive the tessera host's window by name, on a private X display",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&name, "session", "default", "the session's name, for several at once")

	var start startOptions
	var size string
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the host on a private X display, with a copy of a config",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if _, err := fmt.Sscanf(size, "%dx%d", &start.size.X, &start.size.Y); err != nil {
				return fmt.Errorf("--size %q: want WIDTHxHEIGHT", size)
			}
			s, err := startSession(name, start)
			if err != nil {
				if s.Dir != "" {
					s.stop()
				}
				return err
			}
			fmt.Printf("session %q on display %s\nconfig: %s\nlog: %s\n", name, s.Display, s.configPath(), s.hostLog())
			return nil
		},
	}
	startCmd.Flags().StringVar(&start.host, "host", "./tessera", "the host binary")
	startCmd.Flags().StringVar(&start.plugins, "plugins", pluginhost.DefaultDir(), "plugins directory")
	startCmd.Flags().StringVar(&start.config, "config", "", "config file to copy (default: an empty grid)")
	startCmd.Flags().StringVar(&size, "size", "1280x800", "the screen's size in pixels, which the window fills")

	var clean bool
	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the host and its X display",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			s, err := loadSession(name)
			if s.Dir == "" {
				return err
			}
			if err := s.stop(); err != nil {
				return err
			}
			if clean {
				return os.RemoveAll(s.Dir)
			}
			fmt.Println("stopped; files kept in", s.Dir)
			return nil
		},
	}
	stopCmd.Flags().BoolVar(&clean, "clean", false, "remove the session's files too")

	var all bool
	treeCmd := &cobra.Command{
		Use:   "tree",
		Short: "List the window's named elements and their bounds",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			s, err := loadSession(name)
			if err != nil {
				return err
			}
			t, err := s.tree(0)
			if err != nil {
				return err
			}
			fmt.Printf("window %dx%d\n", t.Size.X, t.Size.Y)
			for _, n := range t.Nodes {
				if all || n.Class != "" || n.Label != "" || n.Description != "" || n.Clickable {
					fmt.Println(describe(n))
				}
			}
			return nil
		},
	}
	treeCmd.Flags().BoolVar(&all, "all", false, "list unnamed elements too")

	var nth, count int
	var class string
	var right bool
	clickCmd := &cobra.Command{
		Use:   "click NAME",
		Short: "Click the element NAME names (see tree), or a point given as X,Y",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return act(name, args[0], class, nth, func(d *display, p image.Point) error {
				button := byte(1)
				if right {
					button = 3
				}
				return d.click(p, button, count)
			})
		},
	}
	clickCmd.Flags().IntVar(&nth, "nth", 0, "of several elements that NAME names, the nth, from 1")
	clickCmd.Flags().StringVar(&class, "class", "", "only elements of this class, such as Button or Editor; with it, NAME may be empty for all of them")
	clickCmd.Flags().IntVar(&count, "count", 1, "clicks: 2 for a double click")
	clickCmd.Flags().BoolVar(&right, "right", false, "click the right button")

	hoverCmd := &cobra.Command{
		Use:   "hover NAME",
		Short: "Put the pointer over the element NAME names, or at X,Y",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return act(name, args[0], class, nth, (*display).move)
		},
	}
	hoverCmd.Flags().IntVar(&nth, "nth", 0, "of several elements that NAME names, the nth, from 1")
	hoverCmd.Flags().StringVar(&class, "class", "", "only elements of this class, such as Button or Editor; with it, NAME may be empty for all of them")

	var lines int
	scrollCmd := &cobra.Command{
		Use:   "scroll NAME",
		Short: "Scroll the wheel over the element NAME names, or at X,Y",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return act(name, args[0], class, nth, func(d *display, p image.Point) error {
				button := byte(5) // down
				if lines < 0 {
					button, lines = 4, -lines
				}
				return d.click(p, button, lines)
			})
		},
	}
	scrollCmd.Flags().IntVar(&nth, "nth", 0, "of several elements that NAME names, the nth, from 1")
	scrollCmd.Flags().StringVar(&class, "class", "", "only elements of this class, such as Button or Editor; with it, NAME may be empty for all of them")
	scrollCmd.Flags().IntVar(&lines, "lines", 3, "wheel steps, down; negative scrolls up")

	typeCmd := &cobra.Command{
		Use:   "type TEXT",
		Short: "Type TEXT into the element with the keyboard focus",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			combos, err := typing(args[0])
			if err != nil {
				return err
			}
			return keys(name, combos)
		},
	}

	keyCmd := &cobra.Command{
		Use:   "key COMBO...",
		Short: "Press keys, such as Return, Escape, Tab, ctrl+a, or shift+Tab",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var combos []combo
			for _, a := range args {
				c, err := parseCombo(a)
				if err != nil {
					return err
				}
				combos = append(combos, c)
			}
			return keys(name, combos)
		},
	}

	var out string
	shotCmd := &cobra.Command{
		Use:   "shot",
		Short: "Save a screenshot of the window as PNG and print its path",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			s, err := loadSession(name)
			if err != nil {
				return err
			}
			if _, err := s.tree(0); err != nil { // a frame drawn since the last action
				return err
			}
			d, err := openDisplay(s.Display, 0)
			if err != nil {
				return err
			}
			defer d.Close()
			img, err := d.capture()
			if err != nil {
				return err
			}
			if out == "" {
				out = filepath.Join(s.Dir, time.Now().Format("shot-150405.000.png"))
			}
			f, err := os.Create(out)
			if err != nil {
				return err
			}
			if err := png.Encode(f, img); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			fmt.Println(out)
			return nil
		},
	}
	shotCmd.Flags().StringVarP(&out, "output", "o", "", "the PNG file (default: a new one in the session's directory)")

	var gone bool
	var timeout time.Duration
	waitCmd := &cobra.Command{
		Use:   "wait NAME",
		Short: "Wait until an element NAME names appears, or with --gone, until none does",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			s, err := loadSession(name)
			if err != nil {
				return err
			}
			deadline := time.Now().Add(timeout)
			for {
				t, err := s.tree(0)
				if err != nil {
					return err
				}
				found := t.Find(args[0])
				if len(found) > 0 != gone {
					if !gone {
						fmt.Println(describe(found[0]))
					}
					return nil
				}
				if time.Now().After(deadline) {
					if gone {
						return fmt.Errorf("%q still shown after %v: %s", args[0], timeout, describe(found[0]))
					}
					return fmt.Errorf("nothing named %q after %v", args[0], timeout)
				}
				time.Sleep(100 * time.Millisecond)
			}
		},
	}
	waitCmd.Flags().BoolVar(&gone, "gone", false, "wait until nothing NAME names is shown")
	waitCmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "how long to wait")

	fileCmd := func(use, short string, path func(session) string) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				s, err := loadSession(name)
				if s.Dir == "" {
					return err
				}
				b, err := os.ReadFile(path(s))
				if err != nil {
					return err
				}
				_, err = os.Stdout.Write(b)
				return err
			},
		}
	}

	root.AddCommand(startCmd, stopCmd, treeCmd, clickCmd, hoverCmd, scrollCmd, typeCmd, keyCmd, shotCmd, waitCmd,
		fileCmd("config", "Print the session's config file, as the host has saved it", session.configPath),
		fileCmd("log", "Print the host's log", session.hostLog))
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "uidrive:", err)
		os.Exit(1)
	}
}

// act finds the element target names, or the point target gives as
// X,Y in window pixels, and does do at its center.
func act(name, target, class string, nth int, do func(*display, image.Point) error) error {
	s, err := loadSession(name)
	if err != nil {
		return err
	}
	var at image.Point
	if _, err := fmt.Sscanf(target, "%d,%d", &at.X, &at.Y); err != nil || strings.Count(target, ",") != 1 {
		t, err := s.tree(0)
		if err != nil {
			return err
		}
		n, err := pick(t, target, class, nth)
		if err != nil {
			return err
		}
		at = n.Bounds.Min.Add(n.Bounds.Max).Div(2)
	}
	d, err := openDisplay(s.Display, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := do(d, at); err != nil {
		return err
	}
	time.Sleep(settle)
	return nil
}

// pick is the element query names, of class if it is set: the nth if
// nth is set, and otherwise the one best match, as Tree.Find ranks
// them. An empty query with a class picks among all of that class, top
// to bottom.
func pick(t uidebug.Tree, query, class string, nth int) (uidebug.Node, error) {
	var found []uidebug.Node
	if query == "" && class != "" {
		for _, n := range t.Nodes {
			if !n.Bounds.Empty() {
				found = append(found, n)
			}
		}
		slices.SortStableFunc(found, func(a, b uidebug.Node) int {
			return cmp.Or(cmp.Compare(a.Bounds.Min.Y, b.Bounds.Min.Y), cmp.Compare(a.Bounds.Min.X, b.Bounds.Min.X))
		})
	} else {
		found = t.Find(query)
	}
	if class != "" {
		found = slices.DeleteFunc(found, func(n uidebug.Node) bool { return !strings.EqualFold(n.Class, class) })
	}
	switch {
	case len(found) == 0 && class != "":
		return uidebug.Node{}, fmt.Errorf("no %s named %q; see uidrive tree", class, query)
	case len(found) == 0:
		return uidebug.Node{}, fmt.Errorf("nothing named %q; see uidrive tree", query)
	case nth > 0 && nth <= len(found):
		return found[nth-1], nil
	case nth > 0:
		return uidebug.Node{}, fmt.Errorf("only %d elements named %q", len(found), query)
	case len(found) > 1 && found[0].Clickable == found[1].Clickable:
		var b strings.Builder
		fmt.Fprintf(&b, "%d elements named %q; pick one with --nth:", len(found), query)
		for i, n := range found {
			fmt.Fprintf(&b, "\n  %d: %s", i+1, describe(n))
		}
		return uidebug.Node{}, errors.New(b.String())
	}
	return found[0], nil
}

// keys presses combos in turn.
func keys(name string, combos []combo) error {
	s, err := loadSession(name)
	if err != nil {
		return err
	}
	d, err := openDisplay(s.Display, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	for _, c := range combos {
		if err := d.press(c); err != nil {
			return err
		}
	}
	time.Sleep(settle)
	return nil
}

// describe is one line about n for people.
func describe(n uidebug.Node) string {
	var b strings.Builder
	class := n.Class
	if class == "" {
		class = "-"
	}
	fmt.Fprintf(&b, "%-8s", class)
	text := n.Label
	if text == "" {
		text = n.Text
	}
	if text != "" {
		fmt.Fprintf(&b, " %q", cut(text, 60))
	}
	if n.Description != "" && n.Description != text {
		fmt.Fprintf(&b, " (%s)", cut(n.Description, 60))
	}
	// Text's bounds are the room it was laid out in, which can be far
	// larger than the text, so only its position means much.
	r := n.Bounds
	fmt.Fprintf(&b, " at %d,%d", r.Min.X, r.Min.Y)
	if n.Clickable || n.Class != "" {
		fmt.Fprintf(&b, " %dx%d", r.Dx(), r.Dy())
	}
	for _, f := range []struct {
		on   bool
		name string
	}{{n.Clickable, "click"}, {n.Scrollable, "scroll"}, {n.Selected, "selected"}, {n.Disabled, "disabled"}} {
		if f.on {
			b.WriteString(" " + f.name)
		}
	}
	return b.String()
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
