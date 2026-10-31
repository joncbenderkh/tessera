// SPDX-License-Identifier: GPL-3.0-or-later

// Command tessera is the dashboard host: it owns the window, the grid
// layout, and the plugin lifecycle. It ships with no built-in panels.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"gioui.org/app"
	"gioui.org/unit"
	"github.com/spf13/cobra"

	"github.com/joncbenderkh/tessera"
	"github.com/joncbenderkh/tessera/internal/config"
	"github.com/joncbenderkh/tessera/internal/pluginhost"
)

func main() {
	// tessera is a window, so it may well be started from Explorer,
	// which cobra would refuse on Windows.
	cobra.MousetrapHelpText = ""
	var configPath, pluginsDir string
	cmd := &cobra.Command{
		Use:          "tessera",
		Short:        "A desktop dashboard whose every panel is a plugin",
		Version:      tessera.Version,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error {
			return start(configPath, pluginsDir)
		},
	}
	cmd.SetVersionTemplate("tessera {{.Version}}\n")
	cmd.Flags().StringVar(&configPath, "config", "", "config file (default: tessera/config.yaml in the user config directory)")
	cmd.Flags().StringVar(&pluginsDir, "plugins", pluginhost.DefaultDir(), "plugins directory")
	if cmd.Execute() != nil {
		os.Exit(1)
	}
}

// start runs the host, which exits from within.
func start(configPath, pluginsDir string) error {
	if pluginsDir == "" {
		// Abs would read it as the working directory.
		return errors.New("--plugins is empty")
	}
	if configPath == "" {
		path, err := config.DefaultPath()
		if err != nil {
			return err
		}
		configPath = path
	}
	cfg, read, err := config.Read(configPath)
	if err != nil {
		return err
	}

	// A plugin's path must not be a bare name, which exec looks up in
	// PATH, as --plugins . would make it.
	pluginDir, err := filepath.Abs(pluginsDir)
	if err != nil {
		return err
	}
	plugins := pluginhost.Start(cfg, pluginDir, log.Default())
	window := new(app.Window)
	saver := &themeSaver{path: configPath, done: make(chan error, 1), invalidate: window.Invalidate}
	layouts := &layoutSaver{path: configPath, done: make(chan error, 1), invalidate: window.Invalidate}
	signals := make(chan os.Signal, 1)
	// SIGHUP too: plugins run in their own process groups, so a closed
	// terminal reaches only the host.
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig := <-signals
		signal.Stop(signals) // a second signal ends a slow shutdown at once
		saver.wait()
		layouts.wait()
		plugins.Close()
		// The shell convention for a signalled exit, so a supervisor sees
		// why the host stopped.
		os.Exit(128 + int(sig.(syscall.Signal)))
	}()

	reloads := make(chan reload, 1)
	go func() {
		name := filepath.Base(configPath)
		err := config.Watch(context.Background(), configPath, read, layouts.readFile, func(c config.Config, err error) {
			reloaded(c, err, layouts, plugins.Apply, reloads)
			window.Invalidate()
		})
		if err != nil {
			log.Printf("not watching %s for changes: %v", configPath, err)
			layouts.unwatch()
			queue(reloads, config.Config{}, 0, fmt.Errorf("%s is not watched, so edits apply only after a restart: %w", name, err))
			window.Invalidate()
		}
	}()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				plugins.Close()
				panic(r)
			}
		}()
		window.Option(app.Title("tessera"), app.Size(unit.Dp(1440), unit.Dp(900)), app.MinSize(minWidth, minHeight))
		err := run(window, plugins, pluginDir, cfg, reloads, saver, layouts)
		plugins.Close()
		if err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
	return nil
}
