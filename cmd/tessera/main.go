// SPDX-License-Identifier: GPL-3.0-or-later

// Command tessera is the dashboard host: it owns the window, the grid
// layout, and the plugin lifecycle. It ships with no built-in panels.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gioui.org/app"
	"gioui.org/unit"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
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
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatal(err)
	}

	plugins := pluginhost.Start(cfg, pluginhost.DefaultDir(), log.Default())
	window := new(app.Window)
	saver := &themeSaver{path: *configPath, done: make(chan error, 1), invalidate: window.Invalidate}
	signals := make(chan os.Signal, 1)
	// SIGHUP too: plugins run in their own process groups, so a closed
	// terminal reaches only the host.
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig := <-signals
		signal.Stop(signals) // a second signal ends a slow shutdown at once
		saver.wait()
		plugins.Close()
		// The shell convention for a signalled exit, so a supervisor sees
		// why the host stopped.
		os.Exit(128 + int(sig.(syscall.Signal)))
	}()

	reloads := make(chan reload, 1)
	go func() {
		err := config.Watch(context.Background(), *configPath, func(c config.Config, err error) {
			if err == nil {
				plugins.Apply(c)
			}
			select { // only the latest matters
			case <-reloads:
			default:
			}
			reloads <- reload{c, err}
			window.Invalidate()
		})
		if err != nil {
			log.Printf("not watching %s for changes: %v", *configPath, err)
		}
	}()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				plugins.Close()
				panic(r)
			}
		}()
		window.Option(app.Title("tessera"), app.Size(unit.Dp(1440), unit.Dp(900)), app.MinSize(unit.Dp(360), unit.Dp(240)))
		err := run(window, plugins, cfg, reloads, saver)
		plugins.Close()
		if err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}
