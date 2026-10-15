// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/joncbenderkh/tessera"
	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// Timeouts for bringing a plugin up and taking it down.
const (
	startTimeout = 10 * time.Second
	callTimeout  = 5 * time.Second
	// killTimeout bounds go-plugin's Kill, which waits 2s for a graceful
	// exit but can block for good: on a plugin whose Shutdown never
	// answers, or on a child of the plugin that keeps its stderr open.
	killTimeout = 3 * time.Second
	// reapTimeout bounds the wait for Kill after the plugin's process
	// group has been killed.
	reapTimeout = 2 * time.Second
)

// handshake is the transport handshake of docs/protocol.md.
var handshake = plugin.HandshakeConfig{
	ProtocolVersion:  tesserav1.ProtocolVersion,
	MagicCookieKey:   tesserav1.MagicCookieKey,
	MagicCookieValue: tesserav1.MagicCookieValue,
}

// viewVersions are the view schema versions this host renders.
var viewVersions = []uint32{tesserav1.ViewVersion}

// panelPlugin is the host side of PanelService for go-plugin.
type panelPlugin struct {
	plugin.NetRPCUnsupportedPlugin
}

func (panelPlugin) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error {
	return errors.New("the host does not serve PanelService")
}

func (panelPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, conn *grpc.ClientConn) (any, error) {
	return tesserav1.NewPanelServiceClient(conn), nil
}

// Instance is one running plugin process that has passed the handshake
// and Describe.
type Instance struct {
	Info    *tesserav1.DescribeResponse
	Client  tesserav1.PanelServiceClient
	client  *plugin.Client
	runner  *procRunner
	outputs []*lineWriter
}

// Launch starts the plugin called name in dir, checks its versions, and
// describes it. Each line the plugin writes goes to output: from its
// stderr pipe, and from the stdout and stderr a go-plugin plugin
// forwards over the GRPCStdio service. Canceling ctx stops the launch,
// the handshake included. On error, no process is left running.
func Launch(ctx context.Context, dir, name string, output func(line string), logger *log.Logger) (*Instance, error) {
	path, err := find(dir, name)
	if err != nil {
		return nil, err
	}
	inst := &Instance{runner: &procRunner{path: path}}
	for range 3 {
		inst.outputs = append(inst.outputs, &lineWriter{output: output})
	}
	inst.client = plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  handshake,
		Plugins:          plugin.PluginSet{"panel": panelPlugin{}},
		RunnerFunc:       inst.runner.runnerFunc,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		AutoMTLS:         true,
		StartTimeout:     startTimeout,
		Stderr:           inst.outputs[0],
		SyncStdout:       inst.outputs[1],
		SyncStderr:       inst.outputs[2],
		Logger: quietStderr{
			Logger: hclog.New(&hclog.LoggerOptions{
				Name:   "plugin " + name,
				Output: logger.Writer(),
				Level:  hclog.Warn,
			}),
			stderr: filepath.Base(path),
		},
	})
	if err := inst.start(ctx, name); err != nil {
		inst.Close()
		return nil, err
	}
	return inst, nil
}

func (inst *Instance) start(ctx context.Context, name string) error {
	type started struct {
		rpc plugin.ClientProtocol
		err error
	}
	done := make(chan started, 1)
	go func() {
		rpc, err := inst.client.Client()
		done <- started{rpc, err}
	}()
	var s started
	select {
	case s = <-done:
	case <-ctx.Done():
		inst.runner.kill() // Client returns once the plugin is gone
		<-done
		return fmt.Errorf("plugin %q: %w", name, ctx.Err())
	}
	rpc, err := s.rpc, s.err
	if err != nil && strings.Contains(err.Error(), "incompatible API version") {
		return fmt.Errorf("plugin %q does not speak protocol %d: %w", name, tesserav1.ProtocolVersion, err)
	}
	if err != nil {
		return fmt.Errorf("plugin %q failed to start: %w", name, err)
	}
	raw, err := rpc.Dispense("panel")
	if err != nil {
		return fmt.Errorf("plugin %q: %w", name, err)
	}
	inst.Client = raw.(tesserav1.PanelServiceClient)

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	info, err := inst.Client.Describe(ctx, &tesserav1.DescribeRequest{
		HostVersion:  tessera.Version,
		ViewVersions: viewVersions,
	})
	if err != nil {
		return fmt.Errorf("plugin %q: Describe: %w", name, err)
	}
	switch {
	case info.GetName() != name:
		return fmt.Errorf("plugin %q calls itself %q; the name must match its file name", name, info.GetName())
	case info.GetProtocolVersion() != tesserav1.ProtocolVersion:
		return fmt.Errorf("plugin %q reports protocol %d in Describe; this host speaks %d", name, info.GetProtocolVersion(), tesserav1.ProtocolVersion)
	case !slices.Contains(viewVersions, info.GetViewVersion()):
		return fmt.Errorf("plugin %q chose view schema %d; this host renders %v", name, info.GetViewVersion(), viewVersions)
	}
	inst.Info = info
	return nil
}

// Describe launches the plugin called name in dir only to describe it,
// as the panel picker does, and shuts it down.
func Describe(ctx context.Context, dir, name string, output func(line string), logger *log.Logger) (*tesserav1.DescribeResponse, error) {
	inst, err := Launch(ctx, dir, name, output, logger)
	if err != nil {
		return nil, err
	}
	defer inst.Close()
	return inst.Info, nil
}

// Close shuts the plugin down: Shutdown, then a kill if it is still
// running 2 seconds later. Then it kills the plugin's process group (on
// Windows, the plugin alone): at once if the plugin has exited, to end
// children it left behind, or after killTimeout if go-plugin is still
// waiting, on a plugin that never answers Shutdown or a child holding
// its stderr open. It returns within killTimeout plus reapTimeout.
func (inst *Instance) Close() {
	defer func() {
		for _, w := range inst.outputs {
			w.Flush()
		}
		// go-plugin removes it only for a plugin that started.
		if inst.runner.tmpDir != "" {
			_ = os.RemoveAll(inst.runner.tmpDir)
		}
	}()
	done := make(chan struct{})
	go func() {
		inst.client.Kill()
		close(done)
	}()
	select {
	case <-done:
		inst.runner.kill()
		return
	case <-time.After(killTimeout):
	}
	inst.runner.kill() // the exit unblocks Shutdown, and EOF the stderr reader
	select {
	case <-done:
	case <-time.After(reapTimeout):
	}
}

// Exited reports whether the plugin process has exited.
func (inst *Instance) Exited() bool { return inst.client.Exited() }

// quietStderr is go-plugin's logger minus the sub-logger it re-logs
// plugin stderr lines through, which it names after the executable,
// since Launch passes those lines on raw.
type quietStderr struct {
	hclog.Logger
	stderr string
}

func (l quietStderr) Named(name string) hclog.Logger {
	if name == l.stderr {
		return hclog.NewNullLogger()
	}
	return l.Logger.Named(name)
}

// maxLine caps a buffered line, so a plugin that never writes a
// newline cannot grow the host's memory; longer lines are passed on in
// pieces.
const maxLine = 64 << 10

// lineWriter passes each complete line written to it to output.
type lineWriter struct {
	output func(string)
	mu     sync.Mutex
	line   []byte
}

func (w *lineWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range b {
		if c == '\n' {
			w.output(string(w.line))
			w.line = w.line[:0]
			continue
		}
		w.line = append(w.line, c)
		if len(w.line) == maxLine {
			w.output(string(w.line))
			w.line = w.line[:0]
		}
	}
	return len(b), nil
}

// Flush passes on a final line that has no newline.
func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.line) > 0 {
		w.output(string(w.line))
		w.line = w.line[:0]
	}
}
