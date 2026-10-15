// SPDX-License-Identifier: GPL-3.0-or-later

// Command tessera is the dashboard host: it owns the window, the grid
// layout, and the plugin lifecycle. It ships with no built-in panels.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/internal/config"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	configPath := flag.String("config", "", "config file (default: tessera/config.yaml in the user config directory)")
	flag.Parse()
	if *showVersion {
		fmt.Println("tessera", tessera.Version)
		return
	}

	if *configPath == "" {
		path, err := config.DefaultPath()
		if err != nil {
			log.Fatal(err)
		}
		*configPath = path
	}
	if _, err := config.Load(*configPath); err != nil {
		log.Fatal(err)
	}

	go func() {
		window := new(app.Window)
		window.Option(app.Title("tessera"), app.Size(unit.Dp(1440), unit.Dp(900)))
		if err := run(window); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(window *app.Window) error {
	var ops op.Ops
	for {
		switch e := window.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			e.Frame(gtx.Ops)
		}
	}
}
