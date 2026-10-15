// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	tesserav1 "github.com/joncbenderkh/tessera/proto/tessera/v1"
)

// The test binary doubles as the test plugin: started by the host, it
// serves PanelService, behaving as its file name says (see server).
func TestMain(m *testing.M) {
	if os.Getenv(sleeperKey) != "" {
		time.Sleep(time.Minute) // the forks plugin's child
		return
	}
	if os.Getenv(tesserav1.MagicCookieKey) != "" {
		servePlugin(strings.TrimSuffix(filepath.Base(os.Args[0]), exeSuffix()))
		return
	}
	os.Exit(m.Run())
}

func servePlugin(mode string) {
	if mode == "slow-start" {
		time.Sleep(time.Minute) // never reaches the handshake in a test
	}
	hs := handshake
	switch mode {
	case "proto-two":
		hs.ProtocolVersion = 2
	case "plaintext":
		os.Unsetenv("PLUGIN_CLIENT_CERT") // go-plugin then serves without TLS
	}
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: hs,
		Plugins:         plugin.PluginSet{"panel": testPlugin{mode: mode}},
		GRPCServer:      plugin.DefaultGRPCServer,
		Logger:          hclog.New(&hclog.LoggerOptions{Output: io.Discard}),
	})
}

type testPlugin struct {
	plugin.NetRPCUnsupportedPlugin
	mode string
}

func (p testPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	tesserav1.RegisterPanelServiceServer(s, &server{mode: p.mode})
	return nil
}

func (testPlugin) GRPCClient(context.Context, *plugin.GRPCBroker, *grpc.ClientConn) (any, error) {
	return nil, errors.New("test plugin is server only")
}

// server is a PanelService whose behaviour is chosen by mode, the
// plugin's file name:
//
//   - misnamed: Describe reports the name "other".
//   - slow-start: no handshake for a minute.
//   - proto-two: the handshake offers protocol 2.
//   - plaintext: serves without mutual TLS.
//   - view-nine: Describe picks view schema 9.
//   - slow-describe: Describe never answers.
//   - rejects: Configure answers INVALID_ARGUMENT.
//   - frozen: the process stops itself after Describe, so it never
//     answers Shutdown.
//   - forks: Describe starts a long-lived child that shares the
//     plugin's stderr.
//
// Any other name is a well-behaved plugin of that name, which reports
// each Configure on stderr.
type server struct {
	tesserav1.UnimplementedPanelServiceServer
	mode string
}

func (s *server) Describe(ctx context.Context, _ *tesserav1.DescribeRequest) (*tesserav1.DescribeResponse, error) {
	resp := &tesserav1.DescribeResponse{
		Name:            s.mode,
		Version:         "0.0.0",
		ProtocolVersion: tesserav1.ProtocolVersion,
		ViewVersion:     tesserav1.ViewVersion,
	}
	switch s.mode {
	case "misnamed":
		resp.Name = "other"
	case "view-nine":
		resp.ViewVersion = 9
	case "slow-describe":
		<-ctx.Done()
		return nil, ctx.Err()
	case "forks":
		child := exec.Command(os.Args[0])
		child.Env = []string{sleeperKey + "=1"}
		child.Stderr = os.NewFile(2, "stderr") // go-plugin has replaced os.Stderr
		if err := child.Start(); err != nil {
			return nil, err
		}
	case "frozen":
		go func() {
			time.Sleep(200 * time.Millisecond) // let the response out first
			freeze()
		}()
	}
	return resp, nil
}

func (s *server) Configure(_ context.Context, req *tesserav1.ConfigureRequest) (*tesserav1.ConfigureResponse, error) {
	if s.mode == "rejects" {
		return nil, status.Error(codes.InvalidArgument, "timezone: unknown")
	}
	fmt.Fprintf(os.Stderr, "configured %s %v %dx%d\n", req.GetPanelId(), req.GetSettings().AsMap(),
		req.GetSize().GetColumns(), req.GetSize().GetRows())
	return &tesserav1.ConfigureResponse{}, nil
}

// sleeperKey makes the test binary sleep instead of running tests.
const sleeperKey = "TESSERA_TEST_SLEEPER"

// install copies the test binary into a new plugins directory once per
// name.
func install(t *testing.T, names ...string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name+exeSuffix()), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
