package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestVerifiedProfileLiteralTransportPin(t *testing.T) {
	profile := config.Remote{SSHTarget: "fixture-host", WTBinary: "/literal/wt", WingthingDir: "/state literal", WingID: "selected-wing"}
	args := append(remotepkg.ProfileArgs(profile), "attach", "--read-only", "--", "same-id")
	route, remote, err := remotepkg.ParseRemoteInvocation(args, false, newRootCommand)
	if err != nil || !remote || route.WingID != profile.WingID || route.State != profile.WingthingDir {
		t.Fatalf("pin dropped: %+v %v", route, err)
	}
	ssh := writeFakeRemoteSSH(t, "printf '%s' \"$3\"\n")
	var out bytes.Buffer
	if err := remotepkg.RunRemoteInvocation(t.Context(), route, remotepkg.IO{SSHPath: ssh, Out: &out, ErrOut: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "'--expected-wing-id' 'selected-wing' 'attach' '--read-only' '--' 'same-id'") {
		t.Fatal("receiver pin missing from literal command", out.String())
	}
	for _, extra := range [][]string{{"--remote-wing-id", "other-wing"}, {"--remote-wing-id", ""}} {
		if _, _, err := remotepkg.ParseRemoteInvocation(append(remotepkg.ProfileArgs(profile), append(extra, "attach", "same-id")...), false, newRootCommand); err == nil {
			t.Fatal("duplicate/empty pin accepted")
		}
	}
}

func TestBuiltWTVerifiedSSHRefusesReplacedWingBeforeIO(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	var calls, attachments atomic.Int32
	serve := func(id string) *controlsocket.Server {
		s, err := controlsocket.Listen(t.Context(), f.remote, id, func(hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
			return controlsocket.Welcome{}, func(_ context.Context, req control.DirectRequest) control.DirectResponse {
				calls.Add(1)
				var result map[string]any
				if req.Tool == "terminal_list" {
					result = map[string]any{"sessions": []eggclient.LocalSession{{ID: "same-id", Name: "same-name", PID: os.Getpid(), Status: "idle"}}}
				} else {
					result = map[string]any{"session": "same-id"}
				}
				return control.DirectResponse{Version: control.ContractVersion, ID: req.ID, Result: result}
			}, nil
		}, func(_ context.Context, hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.AttachHandler, error) {
			attachments.Add(1)
			if hello.WingID != id || hello.Attach.Session != "same-id" {
				t.Errorf("unbound attachment: %+v", hello)
			}
			return controlsocket.Welcome{}, func(_ context.Context, stream *controlsocket.SessionStream) error {
				if err := stream.Send(&pb.SessionMsg{SessionId: "same-id", Payload: &pb.SessionMsg_Output{Output: []byte("selected wing screen")}}); err != nil {
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
				}
			}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	profile := config.Remote{SSHTarget: "ns-devbox", WTBinary: binary, WingthingDir: f.remote, WingID: "wing-a", ControlVersion: control.ContractVersion, ControlSocket: f.remote + "/control.sock"}
	if err := config.SaveRemotes(f.local, map[string]config.Remote{"peer": profile}); err != nil {
		t.Fatal(err)
	}
	run := func(args []string) (string, string, error) {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Env, cmd.Dir, cmd.Stdin = f.env, f.work, strings.NewReader("input must stay on the selected wing\n")
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		err := cmd.Run()
		return out.String(), stderr.String(), err
	}
	a := serve("wing-a")
	args := append(remotepkg.ProfileArgs(profile), "session", "ps", "--json", "--remote-inventory")
	out, stderr, err := run(args)
	var inventory eggclient.RemoteSessionInventory
	if err != nil || json.Unmarshal([]byte(out), &inventory) != nil || inventory.WingID != "wing-a" || len(inventory.Sessions) != 1 {
		t.Fatalf("inventory: %s %s %v", out, stderr, err)
	}
	if _, stderr, err := run([]string{"attach", "--read-only", "--", "peer:same-id"}); err != nil || attachments.Load() != 1 {
		t.Fatalf("matching wing refused: %s %v", stderr, err)
	}
	// This switch occurs after the successful inventory. Actions must bind their
	// own handshake instead of trusting a separate successful preflight.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b := serve("wing-b")
	defer b.Close()
	priorCalls, priorAttachments := calls.Load(), attachments.Load()
	for _, command := range [][]string{
		{"attach", "--read-only", "--", "peer:same-id"},
		{"attach", "--takeover", "--", "peer:same-id"},
		append(remotepkg.ProfileArgs(profile), "session", "ps", "--json", "--remote-inventory"),
		append(remotepkg.ProfileArgs(profile), "session", "rename", "--", "same-id", "new-name"),
		append(remotepkg.ProfileArgs(profile), "session", "kill", "--", "same-id"),
		append(remotepkg.ProfileArgs(profile), "terminal", "--detach", "--cwd", f.work),
		append(remotepkg.ProfileArgs(profile), "egg", "codex", "--detach", "--cwd", f.work),
		append(remotepkg.ProfileArgs(profile), "claude", "--cwd", f.work),
		{"pin", "peer:same-id"},
	} {
		out, stderr, err := run(command)
		refused := strings.Contains(stderr, "pinned wing identity mismatch") || strings.Contains(stderr, "wing refused admission")
		if err == nil || out != "" || !refused {
			t.Fatalf("wrong wing reached by %q: stdout=%q stderr=%q err=%v", command, out, stderr, err)
		}
	}
	if calls.Load() != priorCalls || attachments.Load() != priorAttachments {
		t.Fatal("replacement wing received an operation or terminal I/O")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"attach", "--read-only", "--", "peer:same-id"}, {"pin", "peer:same-id"}} {
		if out, _, err := run(command); err == nil || out != "" {
			t.Fatal("missing pinned wing fell back to standalone access")
		}
	}
}

func TestVerifiedCLIUsesReceivingAuthorityForMutationsAndLaunch(t *testing.T) {
	dir := testsocket.Dir(t)
	t.Setenv("WINGTHING_DIR", dir)
	t.Setenv("WT_MCP_CLIENT", "")
	type call struct {
		tool string
		args map[string]any
	}
	seen := make(chan call, 8)
	s, err := controlsocket.Listen(t.Context(), dir, "selected-wing", func(hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		if hello.WingID != "selected-wing" {
			t.Errorf("missing pin: %+v", hello)
		}
		return controlsocket.Welcome{}, func(_ context.Context, req control.DirectRequest) control.DirectResponse {
			var args map[string]any
			if err := json.Unmarshal(req.Arguments, &args); err != nil {
				t.Error(err)
			}
			seen <- call{req.Tool, args}
			return control.DirectResponse{Version: control.ContractVersion, ID: req.ID, Result: map[string]any{"session": "session-id"}}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		args []string
		tool string
	}{
		{[]string{"session", "rename", "same-id", "literal-name"}, "terminal_rename"},
		{[]string{"session", "kill", "same-id"}, "terminal_stop"},
		{[]string{"terminal", "--detach", "--cwd", dir}, "terminal_start"},
		{[]string{"egg", "codex", "--detach", "--cwd", dir, "--", "-m", "fixture-model"}, "agent_start"},
	} {
		args := append([]string{"--expected-wing-id", "selected-wing"}, tc.args...)
		if err := executeCLI(t.Context(), args, remotepkg.IO{In: strings.NewReader(""), Out: io.Discard, ErrOut: io.Discard}); err != nil {
			t.Fatalf("%q: %v", args, err)
		}
		got := <-seen
		if got.tool != tc.tool {
			t.Fatalf("wrong operation: %+v", got)
		}
		if tc.tool == "terminal_rename" && got.args["name"] != "literal-name" {
			t.Fatal(got)
		}
		if tc.tool == "terminal_start" && len(got.args["command"].([]any)) != 0 {
			t.Fatal(got)
		}
	}
	for _, args := range [][]string{{"session", "send", "same-id", "text"}, {"terminal", "--unsandboxed", "--detach"}, {"attach", "other:same-id"}} {
		if err := executeCLI(t.Context(), append([]string{"--expected-wing-id", "selected-wing"}, args...), remotepkg.IO{In: strings.NewReader(""), Out: io.Discard, ErrOut: io.Discard}); err == nil {
			t.Fatal("unbound operation permitted", args)
		}
	}
	if len(seen) != 0 {
		t.Fatal("refused operation mutated the wing")
	}
}
