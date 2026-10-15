// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"slices"
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
	// exit but can block for good on a plugin whose Shutdown never answers.
	killTimeout = 3 * time.Second
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
	Info   *tesserav1.DescribeResponse
	Client tesserav1.PanelServiceClient
	client *plugin.Client
	cmd    *exec.Cmd
}

// Launch starts the plugin called name in dir, checks its versions, and
// describes it. The plugin's output goes to stderr: its stderr pipe,
// and the stdout and stderr a go-plugin plugin forwards over the
// GRPCStdio service. On error, no process is left running.
func Launch(ctx context.Context, dir, name string, stderr io.Writer, logger *log.Logger) (*Instance, error) {
	path, err := find(dir, name)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path)
	inst := &Instance{cmd: cmd}
	inst.client = plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  handshake,
		Plugins:          plugin.PluginSet{"panel": panelPlugin{}},
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		AutoMTLS:         true,
		StartTimeout:     startTimeout,
		Stderr:           stderr,
		SyncStdout:       stderr,
		SyncStderr:       stderr,
		Logger: hclog.New(&hclog.LoggerOptions{
			Name:   "plugin " + name,
			Output: logger.Writer(),
			Level:  hclog.Warn, // plugin stderr already reaches stderr
		}),
	})
	if err := inst.start(ctx, name); err != nil {
		inst.Close()
		return nil, err
	}
	return inst, nil
}

func (inst *Instance) start(ctx context.Context, name string) error {
	rpc, err := inst.client.Client()
	if err != nil {
		return fmt.Errorf("plugin %q failed to start (this host speaks protocol %d): %w", name, tesserav1.ProtocolVersion, err)
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
func Describe(ctx context.Context, dir, name string, stderr io.Writer, logger *log.Logger) (*tesserav1.DescribeResponse, error) {
	inst, err := Launch(ctx, dir, name, stderr, logger)
	if err != nil {
		return nil, err
	}
	defer inst.Close()
	return inst.Info, nil
}

// Close shuts the plugin down: Shutdown, then a kill if it is still
// running 2 seconds later. It returns once the process is gone, within
// killTimeout even if the plugin never answers Shutdown.
func (inst *Instance) Close() {
	done := make(chan struct{})
	go func() {
		inst.client.Kill()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(killTimeout):
	}
	if p := inst.cmd.Process; p != nil {
		_ = p.Kill() // Kill returns once the exit unblocks Shutdown
	}
	<-done
}

// Exited reports whether the plugin process has exited.
func (inst *Instance) Exited() bool { return inst.client.Exited() }
