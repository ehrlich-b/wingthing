package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Preview discovery requires the same executable's exact egg argv. Hold a
// test-only process with that identity; never rerun the suite or a provider.
func init() {
	if os.Getenv("WT_ATTACH_PID_FIXTURE") == "1" {
		_, _ = io.WriteString(os.Stdout, "ready\n")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}

type standaloneAttachEgg struct {
	pb.UnimplementedEggServer
	attached chan *pb.AttachOptions
	busy     atomic.Bool
	inputs   atomic.Int32
}

func (f *standaloneAttachEgg) Session(stream grpc.BidiStreamingServer[pb.SessionMsg, pb.SessionMsg]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	f.attached <- first.AttachOptions
	if f.busy.Load() && first.AttachOptions.Claim {
		return status.Error(codes.FailedPrecondition, "terminal input owned by browser")
	}
	if err := stream.Send(&pb.SessionMsg{SessionId: first.SessionId, Payload: &pb.SessionMsg_Output{Output: []byte("persistent snapshot")}}); err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if msg.GetDetach() {
			return nil
		}
		if len(msg.GetInput()) > 0 {
			f.inputs.Add(1)
		}
	}
}

func TestAttachStandaloneEggRetainsDaemonlessContract(t *testing.T) {
	for _, channel := range []string{"stable", "preview"} {
		t.Run(channel, func(t *testing.T) {
			old := config.ReleaseChannel
			config.ReleaseChannel = channel
			t.Cleanup(func() { config.ReleaseChannel = old })
			scratch, err := filepath.Abs("../../.scratch")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(scratch, 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.MkdirTemp(scratch, "a")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			state := filepath.Join(root, "s")
			t.Setenv("HOME", filepath.Join(root, "home"))
			t.Setenv("WINGTHING_DIR", state)
			t.Setenv("WINGTHING_PREVIEW_DIR", "")
			t.Setenv("WT_MCP_CLIENT", "")
			dir := filepath.Join(state, "eggs", "fixture")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			identity := exec.CommandContext(t.Context(), exe, "egg", "run", "--session-id", "fixture")
			identity.Env = append(os.Environ(), "WT_ATTACH_PID_FIXTURE=1")
			input, err := identity.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			ready, err := identity.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := identity.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = input.Close(); _ = identity.Wait() })
			ack := make([]byte, len("ready\n"))
			if _, err := io.ReadFull(ready, ack); err != nil || string(ack) != "ready\n" {
				t.Fatalf("fixture identity not ready: %q %v", ack, err)
			}
			for name, value := range map[string]string{"egg.pid": strconv.Itoa(identity.Process.Pid), "egg.token": "fixture-token", "egg.meta": "kind=command\ncwd=" + root + "\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := eggclient.WriteSessionName(dir, "weekend"); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", filepath.Join(dir, "egg.sock"))
			if err != nil {
				t.Fatal(err)
			}
			fixture := &standaloneAttachEgg{attached: make(chan *pb.AttachOptions, 8)}
			rpc := grpc.NewServer()
			pb.RegisterEggServer(rpc, fixture)
			go func() { _ = rpc.Serve(listener) }()
			t.Cleanup(rpc.Stop)
			run := func(input string, args ...string) (string, error) {
				t.Helper()
				cmd := newRootCommand()
				cmd.SetArgs(append([]string{"attach"}, args...))
				cmd.SetIn(strings.NewReader(input))
				output := new(bytes.Buffer)
				cmd.SetOut(output)
				cmd.SetErr(new(bytes.Buffer))
				err := cmd.ExecuteContext(t.Context())
				return output.String(), err
			}
			output, err := run("", "--json")
			var sessions []eggclient.LocalSession
			if err != nil || json.Unmarshal([]byte(output), &sessions) != nil || len(sessions) != 1 || sessions[0].ID != "fixture" {
				t.Fatalf("standalone inventory: %s %v", output, err)
			}
			for _, ref := range []string{"weekend", "fixture"} {
				if output, err := run("\x02q", ref); err != nil || output != "persistent snapshot" {
					t.Fatalf("standalone attach %s: %q %v", ref, output, err)
				}
				if options := <-fixture.attached; !options.Claim || options.ReadOnly || options.Owner != "cli" {
					t.Fatalf("writer claim lost: %+v", options)
				}
			}
			fixture.busy.Store(true)
			if _, err := run("must-not-write\n", "fixture"); err == nil || !strings.Contains(err.Error(), "terminal input owned") {
				t.Fatalf("busy lease bypassed: %v", err)
			}
			<-fixture.attached
			if output, err := run("\x02q", "--read-only", "fixture"); err != nil || output != "persistent snapshot" {
				t.Fatalf("read-only observer: %q %v", output, err)
			}
			if options := <-fixture.attached; !options.ReadOnly || options.Claim {
				t.Fatalf("observer claimed input: %+v", options)
			}
			// A named client requires wing authorization even if the egg is local.
			t.Setenv("WT_MCP_CLIENT", "named")
			if _, err := run("", "fixture"); err == nil || !strings.Contains(err.Error(), "no local wing") {
				t.Fatalf("named client bypassed the wing: %v", err)
			}
			t.Setenv("WT_MCP_CLIENT", "")
			refusal := errors.New("wing policy refuses this client")
			wing, err := controlsocket.Listen(t.Context(), state, "fixture-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
				return controlsocket.Welcome{}, nil, refusal
			}, func(context.Context, controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
				return controlsocket.Welcome{}, nil, refusal
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = wing.Close() })
			for _, args := range [][]string{{"fixture"}, {"--json"}, {"--select"}} {
				if _, err := run("", args...); err == nil || !strings.Contains(err.Error(), refusal.Error()) {
					t.Fatalf("wing refusal bypassed for %v: %v", args, err)
				}
			}
			if fixture.inputs.Load() != 0 || len(fixture.attached) != 0 {
				t.Fatal("refused or detached clients sent input or bypassed wing policy")
			}
		})
	}
}
