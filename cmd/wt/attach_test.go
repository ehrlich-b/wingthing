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
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/testprovider"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
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
			scratch := testprovider.TempDir(t)
			if err := os.MkdirAll(scratch, 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.MkdirTemp(scratch, "a")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			state := testprovider.ShortStateDir(t, scratch)
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
			for _, args := range [][]string{{"fixture"}, {"--takeover", "fixture"}, {"--read-only", "fixture"}, {"--json"}, {"--select"}} {
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

func TestAttachReconnectPreservesExecutableStateAndProfileWithoutTakeover(t *testing.T) {
	dir := "/state with space; $(literal)"
	profile := config.Remote{SSHTarget: "user@fixture", WTBinary: "/custom wt/wt-preview", WingthingDir: "/remote state"}
	args := append(remotepkg.ProfileArgs(profile), "attach", "--takeover", "--", "full-id")
	command := attachReconnectCommand(dir, args)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"WINGTHING_DIR=" + remotepkg.ShellQuote(dir), "WINGTHING_PREVIEW_DIR=" + remotepkg.ShellQuote(dir), remotepkg.ShellQuote(executable), "'--remote' 'user@fixture'", "'--remote-binary' '/custom wt/wt-preview'", "'--remote-state' '/remote state'", "'attach' '--' 'full-id'"} {
		if !strings.Contains(command, want) {
			t.Fatalf("reconnect lost %q: %s", want, command)
		}
	}
	if strings.Contains(command, "'--takeover'") {
		t.Fatal("reconnect repeated takeover")
	}
	if command := attachReconnectCommand(dir, []string{"attach", "--", "--takeover"}); !strings.HasSuffix(command, "'--' '--takeover'") {
		t.Fatal("literal session argument changed")
	}
	if standaloneAttachAvailable(&eggclient.AttachConnectionError{Err: syscall.ECONNREFUSED}, "") {
		t.Fatal("established stream failure retried through standalone authority")
	}
}

func TestAttachRemoteUnknownOutcomePrintsExactReconnectOnlyOnStderr(t *testing.T) {
	state := testsocket.Dir(t)
	t.Setenv("WINGTHING_DIR", state)
	t.Setenv("WINGTHING_PREVIEW_DIR", "")
	if err := config.SaveRemotes(state, map[string]config.Remote{"devbox": {SSHTarget: "fixture-host", WTBinary: "/custom binary/wt", WingthingDir: "/saved state"}}); err != nil {
		t.Fatal(err)
	}
	ssh := writeFakeRemoteSSH(t, "echo 'Permission denied (publickey).' >&2; exit 255\n")
	var output, diagnostic bytes.Buffer
	err := executeCLI(t.Context(), []string{"attach", "--takeover", "devbox:full-id"}, remotepkg.IO{In: strings.NewReader(""), Out: &output, ErrOut: &diagnostic, SSHPath: ssh})
	if err == nil || !strings.Contains(err.Error(), "Permission denied") || output.Len() != 0 {
		t.Fatal(output.String(), err)
	}
	if strings.Contains(diagnostic.String(), "Reconnect:") {
		t.Fatal("saved attachment still requires manual reconnect", diagnostic.String())
	}
}

func TestGenericRemoteAttachUnknownOutcomePreservesPinnedRoute(t *testing.T) {
	state := testsocket.Dir(t)
	t.Setenv("WINGTHING_DIR", state)
	t.Setenv("WINGTHING_PREVIEW_DIR", "")
	ssh := writeFakeRemoteSSH(t, "exit 255\n")
	var output, diagnostic bytes.Buffer
	args := []string{"--remote", "fixture-host", "--remote-binary", "/custom binary/wt", "--remote-state", "/saved state", "attach", "--takeover", "--", "full-id"}
	err := executeCLI(t.Context(), args, remotepkg.IO{In: strings.NewReader(""), Out: &output, ErrOut: &diagnostic, SSHPath: ssh})
	var exit *cmdutil.CommandExitError
	if !errors.As(err, &exit) || exit.Code != 255 || output.Len() != 0 {
		t.Fatal(output.String(), err)
	}
	for _, want := range []string{"Connection to fixture-host ended", "session outcome unknown", "'--remote' 'fixture-host'", "'--remote-binary' '/custom binary/wt'", "'--remote-state' '/saved state'", "'attach' '--' 'full-id'"} {
		if !strings.Contains(diagnostic.String(), want) {
			t.Fatalf("missing %q: %s", want, diagnostic.String())
		}
	}
	if strings.Contains(diagnostic.String(), "'--takeover'") {
		t.Fatal("failure advice repeats takeover")
	}
}

func TestAttachRemoteEndedStreamPrintsExactReconnect(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WINGTHING_DIR", state)
	t.Setenv("WINGTHING_PREVIEW_DIR", "")
	if err := config.SaveRemotes(state, map[string]config.Remote{"devbox": {SSHTarget: "fixture-host", WTBinary: "/custom binary/wt", WingthingDir: "/saved state"}}); err != nil {
		t.Fatal(err)
	}
	ssh := writeFakeRemoteSSH(t, `read request
printf '%s\n' '{"message":{"sessionId":"canonical-id","output":"c2NyZWVu"}}' '{"error":"fixture attachment ended without provider exit"}'
`)
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	var output, diagnostic bytes.Buffer
	err = executeCLI(t.Context(), []string{"attach", "--takeover", "devbox:alias"}, remotepkg.IO{In: input, Out: &output, ErrOut: &diagnostic, SSHPath: ssh})
	var exit *cmdutil.CommandExitError
	if !errors.As(err, &exit) || exit.Code != 255 || output.String() != "screen" {
		t.Fatalf("output=%q error=%v", output.String(), err)
	}
	for _, want := range []string{"devbox (fixture-host)", "Reconnect:", "'attach' '--' 'devbox:canonical-id'", "Pinned route:", "'--remote-binary' '/custom binary/wt'", "'--remote-state' '/saved state'", "'attach' '--' 'canonical-id'"} {
		if !strings.Contains(diagnostic.String(), want) {
			t.Fatalf("missing %q: %s", want, diagnostic.String())
		}
	}
	if strings.Contains(diagnostic.String(), "'--takeover'") {
		t.Fatal("recovery advice repeats takeover", diagnostic.String())
	}
}

func TestGenericRemoteAttachAfterDelimiterRequestsTTY(t *testing.T) {
	ssh := writeFakeRemoteSSH(t, "printf '%s\\n' \"$@\"\n")
	for _, test := range []struct {
		args        []string
		interactive bool
		mode        string
	}{
		{[]string{"attach", "--", "full-session-id"}, true, "-t"},
		{[]string{"attach", "--read-only", "--", "--takeover"}, true, "-t"},
		{[]string{"attach", "--", "full-session-id"}, false, "-T"},
		{[]string{"attach"}, true, "-T"},
		{[]string{"attach", "--json"}, true, "-T"},
	} {
		var out bytes.Buffer
		args := append([]string{"--remote", "fixture-host", "--remote-binary", "/opt/wt builds/wt-dev", "--remote-state", "/saved state"}, test.args...)
		err := executeCLI(t.Context(), args, remotepkg.IO{In: strings.NewReader(""), Out: &out, ErrOut: io.Discard, StdinTTY: test.interactive, StdoutTTY: test.interactive, SSHPath: ssh})
		want := test.mode + "\nfixture-host\nWINGTHING_DIR='/saved state' WINGTHING_PREVIEW_DIR='/saved state' '/opt/wt builds/wt-dev' '--expected-channel' 'stable'"
		for _, arg := range test.args {
			want += " " + remotepkg.ShellQuote(arg)
		}
		want += "\n"
		if err != nil || out.String() != want {
			t.Fatalf("generic attach %q: %q, %v; want %q", test.args, out.String(), err, want)
		}
	}
}

func TestRemoteBatchInvocationPreservesArgvAndTTYPolicy(t *testing.T) {
	for _, test := range []struct {
		args, want []string
		tty, batch bool
	}{
		{[]string{"--remote-batch", "attach", "--", "full-id"}, []string{"attach", "--", "full-id"}, false, true},
		{[]string{"--remote-batch=false", "attach", "--", "full-id"}, []string{"attach", "--", "full-id"}, true, false},
		{[]string{"attach", "--", "--remote-batch"}, []string{"attach", "--", "--remote-batch"}, true, false},
		{[]string{"--remote-batch", "terminal", "--name", "--remote-state", "--json"}, []string{"terminal", "--name", "--remote-state", "--json"}, false, true},
		{[]string{"terminal", "--name", "--remote-batch", "--json"}, []string{"terminal", "--name", "--remote-batch", "--json"}, false, false},
		{[]string{"--remote-batch"}, []string{"_remote", "enter", "--json"}, false, true},
	} {
		argv := append([]string{"--remote", "fixture-host"}, test.args...)
		route, remote, err := remotepkg.ParseRemoteInvocation(argv, true, newRootCommand)
		if err != nil || !remote || route.AllocateTTY != test.tty || route.NonInteractive != test.batch || !reflect.DeepEqual(route.Args, test.want) {
			t.Fatalf("%q => %+v remote=%v err=%v; want %q tty=%v batch=%v", argv, route, remote, err, test.want, test.tty, test.batch)
		}
	}
	if _, _, err := remotepkg.ParseRemoteInvocation([]string{"--remote", "fixture-host", "--remote-batch=invalid", "attach"}, true, newRootCommand); err == nil {
		t.Fatal("invalid batch bool accepted")
	}
	if _, _, err := remotepkg.ParseRemoteInvocation([]string{"--remote-batch", "attach"}, true, newRootCommand); err == nil {
		t.Fatal("batch accepted without remote")
	}
	if _, _, err := remotepkg.ParseRemoteInvocation([]string{"--remote", "fixture-host", "wing"}, true, newRootCommand); err == nil {
		t.Fatal("remote command allowlist widened")
	}
}
