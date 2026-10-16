// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

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
	// group has been killed. If Kill is still waiting then, Close closes
	// the plugin's pipes and waits up to pipeTimeout more.
	reapTimeout = 2 * time.Second
	pipeTimeout = time.Second
	// flushTimeout bounds Close's wait for the plugin's last output.
	flushTimeout = 100 * time.Millisecond
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
	rpc     plugin.ClientProtocol
	runner  *procRunner
	outputs []*lineWriter
}

// Launch starts the plugin called name in dir, checks its versions, and
// describes it. Each line the plugin writes goes to output: from its
// stderr pipe, and from the stdout and stderr a go-plugin plugin
// forwards over the GRPCStdio service. Canceling ctx stops the launch,
// the handshake included. On error, no process is left running.
func Launch(ctx context.Context, dir, name string, output func(line string), logger *log.Logger) (*Instance, error) {
	inst, _, err := launch(ctx, dir, name, output, logger, callTimeout, nil)
	return inst, err
}

// launch is Launch with timeout as Describe's deadline. On failure it
// also says how the plugin's process ended, if it started at all, and
// calls stopping, if not nil, before it waits for or shuts down a
// process that started.
func launch(ctx context.Context, dir, name string, output func(line string), logger *log.Logger, timeout time.Duration, stopping func(error)) (*Instance, string, error) {
	path, err := find(dir, name)
	if err != nil {
		return nil, "", err
	}
	inst := &Instance{runner: &procRunner{path: path}}
	for range 3 {
		inst.outputs = append(inst.outputs, &lineWriter{output: output})
	}
	var exclude func(hclog.Level, string, ...any) bool
	if stopping != nil {
		// The supervisor reports exits itself, with the status.
		exclude = func(_ hclog.Level, msg string, _ ...any) bool {
			return msg == "plugin process exited"
		}
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
				Name:    "plugin " + name,
				Output:  logger.Writer(),
				Level:   hclog.Warn,
				Exclude: exclude,
			}),
			stderr: filepath.Base(path),
		},
	})
	if err := inst.start(ctx, name, timeout); err != nil {
		// Only a plugin that ended by itself has an exit to report:
		// decide before Close ends it. One refused for good, or one
		// that hung, is still running. go-plugin kills one whose
		// handshake failed, which is no exit of its own.
		if stopping != nil && inst.runner.started() && ctx.Err() == nil {
			stopping(err)
		}
		var exit string
		var hung hangError
		if inst.runner.started() && Retryable(err) && !errors.As(err, &hung) && !answered(err) && ctx.Err() == nil {
			select {
			case <-inst.Done():
				if s := awaitStatus(ctx, inst); inst.runner.ownExit(s) {
					exit = orUnknown(s)
				}
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
		inst.Close()
		return nil, exit, err
	}
	return inst, "", nil
}

func (inst *Instance) start(ctx context.Context, name string, timeout time.Duration) error {
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
	// go-plugin (v1.8.0) reports a protocol mismatch and a start timeout
	// only in its error text; TestLaunchRejects pins the wording.
	if err != nil && strings.Contains(err.Error(), "incompatible API version") {
		return permanent(fmt.Errorf("plugin %q does not speak protocol %d: %w", name, tesserav1.ProtocolVersion, err))
	}
	if err != nil && badExecutable(err) {
		return permanent(fmt.Errorf("plugin %q cannot be run: %w", name, err))
	}
	// As is a handshake that never comes.
	if err != nil && strings.Contains(err.Error(), "timeout while waiting for plugin to start") {
		return hangError{fmt.Errorf("plugin %q hung: no handshake within %v", name, startTimeout)}
	}
	if err != nil {
		return fmt.Errorf("plugin %q failed to start: %w", name, err)
	}
	inst.rpc = rpc
	raw, err := rpc.Dispense("panel")
	if err != nil {
		return fmt.Errorf("plugin %q: %w", name, err)
	}
	inst.Client = raw.(tesserav1.PanelServiceClient)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	info, err := inst.Client.Describe(ctx, &tesserav1.DescribeRequest{
		HostVersion:  tessera.Version,
		ViewVersions: viewVersions,
	})
	if err != nil && hung(ctx, err, timeout) {
		return hangError{fmt.Errorf("plugin %q hung: Describe did not answer within %v", name, timeout)}
	}
	if err != nil {
		return fmt.Errorf("plugin %q: Describe: %w", name, err)
	}
	switch {
	case info.GetName() != name:
		return permanent(fmt.Errorf("plugin %q calls itself %q; the name must match its file name", name, info.GetName()))
	case info.GetProtocolVersion() != tesserav1.ProtocolVersion:
		return permanent(fmt.Errorf("plugin %q reports protocol %d in Describe; this host speaks %d", name, info.GetProtocolVersion(), tesserav1.ProtocolVersion))
	case !slices.Contains(viewVersions, info.GetViewVersion()):
		return permanent(fmt.Errorf("plugin %q chose view schema %d; this host renders %v", name, info.GetViewVersion(), viewVersions))
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
// running 2 seconds later. If go-plugin is still waiting after
// killTimeout, on a plugin that never answers Shutdown or a child
// holding its stderr open, Close kills the plugin's process group (on
// Windows, the plugin alone). Where watch runs, the group of a plugin
// that exits is killed as it exits, so its children go with it.
// It returns within killTimeout, reapTimeout, pipeTimeout, and
// flushTimeout together, even if the plugin is still running then, as
// one stuck in the kernel can be after SIGKILL.
//
// A plugin whose exited process left its pipes to a child outside its
// group is finished by watch instead, within pipeGrace plus
// drainTimeout of its exit, which is less than killTimeout.
func (inst *Instance) Close() {
	defer func() {
		// Waited for briefly, so a last unterminated line comes before
		// whatever follows Close, but a stalled output callback cannot
		// hold Close up.
		flushed := make(chan struct{})
		go func() {
			for _, w := range inst.outputs {
				w.finish()
			}
			close(flushed)
		}()
		select {
		case <-flushed:
		case <-time.After(flushTimeout):
			for _, w := range inst.outputs {
				w.drop()
			}
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
		return
	case <-time.After(reapTimeout):
	}
	// Only a child that left the group can still hold the pipes, on a
	// plugin whose exit watch did not see, or the plugin's connection;
	// go-plugin waits for both.
	inst.runner.closePipes()
	if rpc, ok := inst.rpc.(*plugin.GRPCClient); ok {
		rpc.Conn.Close() // ends a Shutdown call that nothing will answer
	}
	select {
	case <-done:
	case <-time.After(pipeTimeout):
	}
}

// Ping checks that the plugin still answers, through the gRPC health
// service every plugin serves; ctx bounds the wait.
func (inst *Instance) Ping(ctx context.Context) error {
	rpc, ok := inst.rpc.(*plugin.GRPCClient)
	if !ok {
		return errors.New("plugin has no gRPC connection")
	}
	resp, err := grpc_health_v1.NewHealthClient(rpc.Conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: plugin.GRPCServiceName})
	if err != nil {
		return err
	}
	if s := resp.GetStatus(); s != grpc_health_v1.HealthCheckResponse_SERVING {
		return fmt.Errorf("plugin reports %v", s)
	}
	return nil
}

// Done is closed once the plugin process has exited.
func (inst *Instance) Done() <-chan struct{} { return inst.runner.exitCh }

// ExitStatus describes how the plugin process ended, such as "exit
// status 3" or "signal: killed", once it has been reaped; until then,
// and where the host cannot tell, it is "".
func (inst *Instance) ExitStatus() string { return inst.runner.status() }

// Exited reports whether the plugin process has exited.
func (inst *Instance) Exited() bool {
	return inst.client.Exited() || inst.runner.sawExit()
}

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

// lineWriter passes each complete line written to it to output, until
// finish.
type lineWriter struct {
	output   func(string)
	mu       sync.Mutex
	line     []byte
	finished atomic.Bool
}

func (w *lineWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range b {
		if w.finished.Load() {
			return len(b), nil
		}
		if c == '\n' {
			w.output(string(bytes.TrimSuffix(w.line, []byte("\r")))) // Windows line ends
			w.line = w.line[:0]
			continue
		}
		w.line = append(w.line, c)
		if len(w.line) == maxLine {
			cut := runeCut(w.line)
			w.output(string(w.line[:cut]))
			w.line = w.line[:copy(w.line, w.line[cut:])]
		}
	}
	return len(b), nil
}

// runeCut is where to cut a full line buffer: at its end, or before a
// UTF-8 character the buffer ends in the middle of.
func runeCut(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if utf8.FullRune(b[i:]) {
				return len(b)
			}
			return i
		}
	}
	return len(b)
}

// finish waits for a Write under way, flushes w, and drops whatever
// is written to it afterwards: go-plugin can still be passing on output
// a plugin sent before Close, which must not land after the next
// launch's. Close bounds the wait and then calls drop.
func (w *lineWriter) finish() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.finished.Load() && len(w.line) > 0 {
		w.output(string(w.line))
		w.line = w.line[:0]
	}
	w.finished.Store(true)
}

// drop stops w passing on any more lines, even in a Write under way.
func (w *lineWriter) drop() { w.finished.Store(true) }
