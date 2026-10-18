// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
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
	"google.golang.org/grpc/health/grpc_health_v1"
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
	if dir := os.Getenv(hostKey); dir != "" {
		runHost(dir)
		return
	}
	os.Exit(m.Run())
}

// rawStdout and rawStderr are the plugin's real stdout and stderr:
// plugin.Serve replaces os.Stdout and os.Stderr with pipes it forwards
// over GRPCStdio.
var rawStdout, rawStderr *os.File

func servePlugin(mode string) {
	rawStdout, rawStderr = os.Stdout, os.Stderr
	if mode == "dies-at-start" || mode == "fails-at-start" {
		fmt.Fprintln(os.Stderr, "boom")
		if mode == "fails-at-start" {
			os.Exit(1) // as log.Fatal does
		}
		os.Exit(7)
	}
	if mode == "escapes-early" {
		child := exec.Command(os.Args[0])
		child.Env = []string{sleeperKey + "=1"}
		child.Stdout = rawStdout // holds the handshake pipe
		leaveGroup(child)
		if err := child.Start(); err == nil {
			fmt.Fprintf(rawStderr, "child %d\n", child.Process.Pid)
		}
	}
	if mode == "junk-stdout" {
		fmt.Fprintln(rawStdout, "hello") // not a handshake line
	}
	if mode == "junk-mismatch" {
		fmt.Fprintln(rawStdout, "incompatible API version, says a log line")
	}
	if mode == "core-two" {
		fmt.Fprintln(rawStdout, "2|1|unix|/nonexistent|grpc|")
	}
	if mode == "slow-start" || mode == "escapes-early" || mode == "junk-stdout" || mode == "junk-mismatch" || mode == "core-two" {
		time.Sleep(time.Minute) // never reaches the handshake in a test
	}
	hs := handshake
	switch mode {
	case "proto-two":
		hs.ProtocolVersion = 2
	case "plaintext":
		os.Unsetenv("PLUGIN_CLIENT_CERT") // go-plugin then serves without TLS
	}
	newServer := plugin.DefaultGRPCServer
	if mode == "not-serving" {
		newServer = func(opts []grpc.ServerOption) *grpc.Server {
			return grpc.NewServer(append(opts, grpc.ChainUnaryInterceptor(notServing))...)
		}
	}
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: hs,
		Plugins:         plugin.PluginSet{"panel": testPlugin{mode: mode}},
		GRPCServer:      newServer,
		Logger:          hclog.New(&hclog.LoggerOptions{Output: io.Discard}),
	})
	if mode == "fails-shutdown" || mode == "misnamed-fails-shutdown" {
		os.Exit(2)
	}
}

// notServing answers every health check NOT_SERVING.
func notServing(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if info.FullMethod == grpc_health_v1.Health_Check_FullMethodName {
		return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING}, nil
	}
	return handler(ctx, req)
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
//   - escapes-early: as slow-start, after starting a child that leaves
//     its process group and holds its stdout.
//   - junk-stdout: writes a line that is not a handshake to stdout,
//     then as slow-start.
//   - junk-mismatch: as junk-stdout, with a line that names a
//     protocol mismatch.
//   - not-serving: answers every health check NOT_SERVING.
//   - proto-two: the handshake offers protocol 2.
//   - core-two: the handshake offers go-plugin's core protocol 2.
//   - plaintext: serves without mutual TLS.
//   - view-nine: Describe picks view schema 9.
//   - slow-describe: Describe never answers.
//   - rejects: Configure answers INVALID_ARGUMENT.
//   - frozen: the process stops itself after Describe, so it never
//     answers Shutdown.
//   - mute-frozen: as frozen, after closing its stdout and stderr.
//   - crashes: exits with status 3 shortly after each Configure.
//   - flaky: as crashes on its first two launches, then well-behaved.
//   - hangs-configure: Configure never answers.
//   - freezes-configuring: the process stops itself inside Configure,
//     so neither Configure nor Shutdown is answered.
//   - dies-configuring: exits with status 4 inside Configure.
//   - dies-at-start: prints "boom" and exits with status 7 before the
//     handshake.
//   - fails-at-start: as dies-at-start, with status 1.
//   - upstream-timeout: Configure answers DeadlineExceeded at once, as
//     a plugin whose own upstream call timed out would.
//   - crash-then-hang: as crashes on its first launch; then Configure
//     never answers.
//   - freezes-live: stops itself shortly after Configure.
//   - fails-shutdown: exits with status 2 when shut down.
//   - misnamed-fails-shutdown: as misnamed and fails-shutdown.
//   - forks: Describe starts a long-lived child that shares the
//     plugin's stderr, and reports its PID there.
//   - exits: as forks, then the plugin exits on its own.
//   - detached: as forks, but the child holds none of the plugin's
//     output, so the plugin exits promptly on Shutdown.
//   - escapes: as exits, but the child leaves the plugin's process
//     group, so it survives the plugin and keeps its stderr open.
//   - floods-stdout: after Describe, writes 1 MiB to stdout with no
//     newline, then reports "flooded" on stderr.
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
	case "misnamed", "misnamed-fails-shutdown":
		resp.Name = "other"
	case "view-nine":
		resp.ViewVersion = 9
	case "slow-describe":
		<-ctx.Done()
		return nil, ctx.Err()
	case "forks", "exits", "detached", "escapes":
		child := exec.Command(os.Args[0])
		child.Env = []string{sleeperKey + "=1"}
		if s.mode != "detached" {
			child.Stderr = rawStderr
		}
		if s.mode == "escapes" {
			leaveGroup(child)
		}
		if err := child.Start(); err != nil {
			return nil, err
		}
		fmt.Fprintf(rawStderr, "child %d\n", child.Process.Pid)
		if s.mode == "exits" || s.mode == "escapes" {
			go func() {
				time.Sleep(200 * time.Millisecond)
				fmt.Fprintln(rawStderr, "last words")
				os.Exit(0)
			}()
		}
	case "floods-stdout":
		go func() {
			rawStdout.Write(bytes.Repeat([]byte("x"), 1<<20))
			fmt.Fprintln(rawStderr, "flooded")
		}()
	case "mute-frozen":
		go func() {
			time.Sleep(200 * time.Millisecond) // let the response out first
			rawStdout.Close()
			rawStderr.Close()
			freeze()
		}()
	case "frozen":
		go func() {
			time.Sleep(200 * time.Millisecond) // let the response out first
			freeze()
		}()
	}
	return resp, nil
}

func (s *server) Configure(ctx context.Context, req *tesserav1.ConfigureRequest) (*tesserav1.ConfigureResponse, error) {
	switch s.mode {
	case "rejects":
		return nil, status.Error(codes.InvalidArgument, "timezone: unknown")
	case "hangs-configure":
		<-ctx.Done()
		return nil, ctx.Err()
	case "freezes-configuring":
		freeze() // the stop can land after kill returns
		<-ctx.Done()
		return nil, ctx.Err()
	case "crashes":
		crashSoon()
	case "flaky":
		if launches() <= 2 {
			crashSoon()
		}
	case "dies-configuring":
		os.Exit(4)
	case "upstream-timeout":
		return nil, status.Error(codes.DeadlineExceeded, "weather service did not answer")
	case "crash-then-hang":
		if launches() == 1 {
			crashSoon()
		} else {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	case "freezes-live":
		go func() {
			time.Sleep(200 * time.Millisecond)
			freeze()
		}()
	}
	fmt.Fprintf(os.Stderr, "configured %s %v %dx%d\n", req.GetPanelId(), req.GetSettings().AsMap(),
		req.GetSize().GetColumns(), req.GetSize().GetRows())
	return &tesserav1.ConfigureResponse{}, nil
}

// crashSoon exits with status 3 once Configure has answered.
func crashSoon() {
	go func() {
		time.Sleep(100 * time.Millisecond)
		os.Exit(3)
	}()
}

// launches counts this plugin's launches in a file next to it and
// returns the count, this one included.
func launches() int {
	path := os.Args[0] + ".launches"
	b, _ := os.ReadFile(path)
	n := len(b) + 1
	os.WriteFile(path, bytes.Repeat([]byte("x"), n), 0o644)
	return n
}

// sleeperKey makes the test binary sleep instead of running tests.
const sleeperKey = "TESSERA_TEST_SLEEPER"

// hostKey makes the test binary a host: it launches the clock plugin
// from the directory in the variable, prints the plugin's PID, and
// waits to be killed.
const hostKey = "TESSERA_TEST_HOST"

func runHost(dir string) {
	inst, err := Launch(context.Background(), dir, "clock", func(string) {}, log.New(io.Discard, "", 0))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println(inst.runner.proc.Pid)
	select {}
}

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
