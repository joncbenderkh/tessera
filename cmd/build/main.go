// SPDX-License-Identifier: GPL-3.0-or-later

// Command build builds the tessera host and installs the first-party
// plugins into the plugins directory the host loads them from.
//
//	go run ./cmd/build                  # the host, as ./tessera, and every plugin
//	go run ./cmd/build --no-host        # the plugins only
//	go run ./cmd/build --run [-- flags] # both, then start the host with flags
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/joncbenderkh/tessera/internal/pluginhost"
)

func main() {
	var noHost, run bool
	var dest string
	cmd := &cobra.Command{
		Use:   "build [flags] [-- host flags]",
		Short: "Build the tessera host and install the first-party plugins",
		Args: func(cmd *cobra.Command, args []string) error {
			return checkArgs(run, cmd.ArgsLenAtDash(), args)
		},
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if dest == "" {
				// Abs would read it as the working directory.
				return errors.New("--dest is empty")
			}
			if noHost && run {
				return errors.New("--run needs the host, which --no-host leaves out")
			}
			build(noHost, run, dest, args)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noHost, "no-host", false, "build and install the plugins only, not the host")
	cmd.Flags().BoolVar(&run, "run", false, "start the host, with --dest as its plugins directory, once everything is built; arguments after -- go to it")
	cmd.Flags().StringVar(&dest, "dest", pluginhost.DefaultDir(), "the plugins directory to install into")
	if cmd.Execute() != nil {
		os.Exit(1)
	}
}

// checkArgs accepts arguments only for the host, after --, which is at
// dash in them (-1 if absent).
func checkArgs(run bool, dash int, args []string) error {
	if dash != 0 && len(args) > 0 {
		if dash > 0 {
			args = args[:dash]
		}
		return fmt.Errorf("unexpected arguments %q: those for the host go after -- (--dest takes the plugins directory)", args)
	}
	if len(args) > 0 && !run {
		return fmt.Errorf("unexpected arguments %q: only --run passes arguments on, to the host", args)
	}
	return nil
}

// build builds the host unless noHost, installs the plugins into
// dest, and with run starts the host on them with extra arguments.
func build(noHost, run bool, dest string, extra []string) {
	log.SetFlags(0)
	log.SetPrefix("build: ")
	// One channel takes every stop signal from the start. During the
	// build the first stops go build and leaves this to remove what it
	// staged; one from the terminal reaches go build as well. Unlike
	// Ignore, this does not pass on to it. Later ones go to the host.
	signals := make(chan os.Signal, len(stopSignals))
	signal.Notify(signals, stopSignals...)
	ctx, stopped := watch(signals)
	root, err := moduleRoot()
	if err != nil {
		log.Fatal(err)
	}
	host := filepath.Join(root, "tessera"+exeSuffix())
	if !noHost {
		if err := goBuild(ctx, root, nil, "-o", host, "./cmd/tessera"); err != nil {
			fatalUnlessStopped(ctx, "building the host", err)
		}
		log.Printf("host: %s", host)
	}
	names, err := installPlugins(ctx, root, dest)
	if err != nil {
		fatalUnlessStopped(ctx, "installing the plugins", err)
	}
	log.Printf("plugins in %s: %s", dest, strings.Join(names, ", "))
	// A signal during the renames, which it does not stop, stops this
	// now; any later one goes to the host.
	if stopped() {
		log.Fatal("stopped by a signal")
	}
	if run {
		args, err := hostArgs(dest, extra)
		if err != nil {
			log.Fatal(err)
		}
		os.Exit(runHost(host, args, signals))
	}
}

// fatalUnlessStopped ends this after err in doing what, or says that a
// signal stopped it, if one did.
func fatalUnlessStopped(ctx context.Context, what string, err error) {
	if ctx.Err() != nil {
		log.Fatal("stopped by a signal")
	}
	log.Fatalf("%s: %v", what, err)
}

// watch returns a context cancelled by the first signal from signals,
// and a function that ends the watch and reports whether one came,
// taking any that waits in signals, so later ones stay there.
func watch(signals chan os.Signal) (context.Context, func() bool) {
	ctx, cancel := context.WithCancel(context.Background())
	done, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-signals:
			cancel()
		case <-done:
		}
	}()
	return ctx, func() bool {
		close(done)
		<-watched
		select {
		case <-signals:
			cancel()
		default:
		}
		return ctx.Err() != nil
	}
}

// module is tessera's module path.
const module = "github.com/joncbenderkh/tessera"

// moduleRoot is the nearest directory at or above the working directory
// that holds a go.mod, which must be tessera's: the plugins of another
// module must not replace tessera's.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if modulePath(data) != module {
				return "", fmt.Errorf("%s is not tessera's module: run this inside the tessera module", dir)
			}
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod here or above: run this inside the tessera module")
		}
		dir = parent
	}
}

// modulePath is the path the module line of a go.mod gives.
func modulePath(goMod []byte) string {
	for line := range strings.Lines(string(goMod)) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module"); ok && rest != strings.TrimLeft(rest, " \t") {
			path, _, _ := strings.Cut(strings.TrimSpace(rest), "//")
			return strings.Trim(strings.TrimSpace(path), `"`)
		}
	}
	return ""
}

// goBuild runs go build in root with env added to the environment,
// stopping it when ctx is done. There is no library for compiling Go,
// so it runs the go command.
func goBuild(ctx context.Context, root string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "go", append([]string{"build"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if runtime.GOOS != "windows" {
		// Interrupted, go build finishes writing its output, which a
		// kill could leave half copied; it is killed if it lingers.
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = 10 * time.Second
	}
	return cmd.Run()
}

// installPlugins builds every plugin under root's plugins directory,
// without cgo, and moves each into dest. It returns their names.
func installPlugins(ctx context.Context, root, dest string) ([]string, error) {
	// go build runs in root, so it must not read a relative dest itself.
	dest, err := filepath.Abs(dest)
	if err != nil {
		return nil, err
	}
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
	if err := goBuild(ctx, root, []string{"CGO_ENABLED=0"}, "-o", staging+string(filepath.Separator), "./plugins/..."); err != nil {
		return nil, err
	}
	names, err := install(staging, dest)
	if err == nil && len(names) == 0 {
		err = errors.New("go build wrote no plugins")
	}
	return names, err
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

// hostArgs is how the host is started on the plugins in dest, then with
// the arguments given after --, which can override it.
func hostArgs(dest string, extra []string) ([]string, error) {
	abs, err := filepath.Abs(dest)
	if err != nil {
		return nil, err
	}
	return append([]string{"--plugins", abs}, extra...), nil
}

// stopSignals are the signals that stop the build, and the host.
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}

// runHost runs the host with args until it exits, passing on each
// signal from signals, and returns its exit code.
func runHost(host string, args []string, signals <-chan os.Signal) int {
	cmd := exec.Command(host, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Signals reach the host from this alone, each once: one sent to the
	// whole process group as well would end its shutdown at once, as a
	// second one does.
	ownGroup(cmd)
	err := cmd.Start()
	if err == nil {
		done := make(chan struct{})
		go func() {
			for {
				select {
				case sig := <-signals:
					cmd.Process.Signal(sig)
				case <-done:
					return
				}
			}
		}()
		err = cmd.Wait()
		close(done)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// The shell's code for a signalled exit, as the host gives its own.
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
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
