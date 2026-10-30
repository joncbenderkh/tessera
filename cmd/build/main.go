// SPDX-License-Identifier: GPL-3.0-or-later

// Command build builds the tessera host and installs the first-party
// plugins into the plugins directory the host loads them from.
//
//	go run ./cmd/build                 # the host, as ./tessera, and every plugin
//	go run ./cmd/build -plugins        # the plugins only
//	go run ./cmd/build -run [-- flags] # both, then start the host with flags
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
)

func main() {
	pluginsOnly := flag.Bool("plugins", false, "build and install the plugins only, not the host")
	run := flag.Bool("run", false, "start the host once everything is built; arguments after -- go to it")
	dest := flag.String("dest", pluginhost.DefaultDir(), "the plugins directory to install into")
	flag.Parse()
	log.SetFlags(0)
	log.SetPrefix("build: ")
	if *pluginsOnly && *run {
		log.Fatal("-run needs the host, which -plugins leaves out")
	}
	if flag.NArg() > 0 && !*run {
		log.Fatalf("unexpected arguments %q: only -run passes arguments on, to the host", flag.Args())
	}
	root, err := moduleRoot()
	if err != nil {
		log.Fatal(err)
	}
	host := filepath.Join(root, "tessera"+exeSuffix())
	if !*pluginsOnly {
		if err := goBuild(root, nil, "-o", host, "./cmd/tessera"); err != nil {
			log.Fatalf("building the host: %v", err)
		}
		log.Printf("host: %s", host)
	}
	names, err := installPlugins(root, *dest)
	if err != nil {
		log.Fatalf("installing the plugins: %v", err)
	}
	log.Printf("plugins in %s: %s", *dest, strings.Join(names, ", "))
	if *run {
		os.Exit(runHost(host, flag.Args()))
	}
}

// moduleRoot is the nearest directory at or above the working directory
// that holds a go.mod.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod here or above: run this inside the tessera module")
		}
		dir = parent
	}
}

// goBuild runs go build in root with env added to the environment. There
// is no library for compiling Go, so it runs the go command.
func goBuild(root string, env []string, args ...string) error {
	cmd := exec.Command("go", append([]string{"build"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// installPlugins builds every plugin under root's plugins directory,
// without cgo, and moves each into dest. It returns their names.
func installPlugins(root, dest string) ([]string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	// The plugins are built beside their destination, so each is renamed
	// into place whole: a panel that starts meanwhile never runs a
	// half-written file.
	staging, err := os.MkdirTemp(dest, ".build-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	if err := goBuild(root, []string{"CGO_ENABLED=0"}, "-o", staging+string(filepath.Separator), "./plugins/..."); err != nil {
		return nil, err
	}
	return install(staging, dest)
}

// install renames every file in staging into dest and returns the
// plugin names they give.
func install(staging, dest string) ([]string, error) {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if err := os.Rename(filepath.Join(staging, e.Name()), filepath.Join(dest, e.Name())); err != nil {
			if runtime.GOOS == "windows" {
				// Windows does not replace a running executable.
				err = fmt.Errorf("%w (quit tessera, whose panels run it, and try again)", err)
			}
			return names, err
		}
		names = append(names, strings.TrimSuffix(e.Name(), exeSuffix()))
	}
	return names, nil
}

// runHost runs the host with args until it exits, and returns its exit
// code.
func runHost(host string, args []string) int {
	// An interrupt from the terminal reaches the host as well; this waits
	// for it to shut down. Notify, unlike Ignore, does not pass on to the
	// host.
	signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	cmd := exec.Command(host, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		log.Print(err)
		return 1
	}
	return 0
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
