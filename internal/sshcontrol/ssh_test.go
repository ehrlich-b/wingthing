package sshcontrol_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testssh"
)

func TestSSHForwardBoundSocketWaitsForListen(t *testing.T) {
	for _, listen := range []bool{false, true} {
		t.Run(fmt.Sprintf("listen=%t", listen), func(t *testing.T) {
			h := testssh.New(t)
			t.Setenv("WT_FAKE_SSH_BIND_BARRIER", "1")
			state := filepath.Join(h.Root, "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			s, err := controlsocket.Listen(t.Context(), state, "remote-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
				return controlsocket.Welcome{}, func(_ context.Context, r control.DirectRequest) control.DirectResponse {
					return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"ready": true}}
				}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			meta, err := sshcontrol.InspectLocal(t.Context(), state, "")
			if err != nil {
				t.Fatal(err)
			}
			h.Host(t, "host", meta)
			remote := config.Remote{SSHTarget: "host", WingID: meta.WingID, WingthingDir: state, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			type result struct {
				conn *sshcontrol.Connection
				err  error
			}
			done := make(chan result, 1)
			go func() {
				conn, err := (sshcontrol.Transport{SSHPath: h.SSHPath, SocketDir: h.Root}).Dial(ctx, remote, controlsocket.Hello{})
				done <- result{conn, err}
			}()
			var forward *testssh.Forward
			select {
			case forward = <-h.Started:
			case <-ctx.Done():
				t.Fatal("forward did not reach its bind barrier")
			}
			// Prove the exact state that filesystem creation alone cannot resolve.
			probe, err := net.Dial("unix", forward.Socket)
			if probe != nil {
				_ = probe.Close()
			}
			if !errors.Is(err, syscall.ECONNREFUSED) {
				t.Fatalf("bound socket was already listening: %v", err)
			}
			if listen {
				if err := forward.StartListening(); err != nil {
					t.Fatal(err)
				}
			}
			got := <-done
			if !listen {
				if got.conn != nil {
					_ = got.conn.Close()
				}
				if !errors.Is(got.err, context.DeadlineExceeded) {
					t.Fatalf("bound socket must wait for readiness until canceled: %v", got.err)
				}
				return
			}
			if got.err != nil {
				t.Fatal(got.err)
			}
			defer got.conn.Close()
			data, denied, err := got.conn.Client.Call(ctx, "ping", json.RawMessage(`{}`))
			if err != nil || denied || data["ready"] != true {
				t.Fatalf("ready forward: %v %t %v", data, denied, err)
			}
		})
	}
}

func TestSSHInspectAndPinnedForward(t *testing.T) {
	h := testssh.New(t)
	state := filepath.Join(h.Root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := controlsocket.Listen(t.Context(), state, "remote-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, func(_ context.Context, r control.DirectRequest) control.DirectResponse {
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"ok": true}}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Setenv("HOME", h.Root)
	meta, err := sshcontrol.InspectLocal(t.Context(), "~/state", "")
	if err != nil {
		t.Fatal(err)
	}
	if meta.WingthingDir != state {
		t.Fatalf("remote home was not resolved once: %+v", meta)
	}
	h.Host(t, "host", meta)
	transport := sshcontrol.Transport{SSHPath: h.SSHPath, SocketDir: h.Root}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	remote, err := transport.Inspect(ctx, "host", "~/state with space/it's isolated", "/opt/wt builds/wt-dev", "client")
	if err != nil {
		t.Fatal(err)
	}
	if remote.WingID != "remote-wing" || remote.WingthingDir != meta.WingthingDir || remote.WTBinary != "/opt/wt builds/wt-dev" {
		t.Fatalf("metadata: %+v", remote)
	}
	wire, err := os.ReadFile(filepath.Join(h.Root, "host.inspect"))
	if err != nil {
		t.Fatal(err)
	}
	var inspectArgs []string
	if err := json.Unmarshal(wire, &inspectArgs); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(inspectArgs[len(inspectArgs)-1], "'/opt/wt builds/wt-dev' mcp inspect --client 'client'") {
		t.Fatalf("remote executable was not quoted: %v", inspectArgs)
	}
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "UpdateHostKeys=no", "~/state with space/it"} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("missing %q in inspect %s", want, wire)
		}
	}
	c, err := transport.Dial(ctx, remote, controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	forward := <-h.Started
	for _, want := range []string{"ExitOnForwardFailure=yes", "ServerAliveInterval=15", "ServerAliveCountMax=2", "StreamLocalBindUnlink=no", "StrictHostKeyChecking=yes"} {
		if !strings.Contains(strings.Join(forward.Args, " "), want) {
			t.Fatal(want)
		}
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(forward.Socket): 0700, forward.Socket: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("private endpoint %s: %v %v", path, info, err)
		}
	}
	data, _, err := c.Client.Call(ctx, "ping", json.RawMessage(`{}`))
	if err != nil || data["ok"] != true {
		t.Fatalf("forward: %v %v", data, err)
	}
	forward.Drop()
	<-c.Done()
	_ = c.Close()
	if _, err := os.Stat(filepath.Dir(forward.Socket)); !os.IsNotExist(err) {
		t.Fatalf("forward directory leaked: %v", err)
	}
	wrong := remote
	wrong.WingID = "changed-wing"
	if c, err := transport.Dial(ctx, wrong, controlsocket.Hello{}); err == nil {
		c.Close()
		t.Fatal("changed wing accepted")
	}
}

func TestSSHInspectUsageErrorsSuggestWTBinary(t *testing.T) {
	for _, test := range []struct {
		diagnostic string
		code       int
		tooOld     bool
	}{
		{"Error: unknown flag: --client", 1, true},
		{"Error: unknown command inspect for wt mcp", 1, true},
		{"flag provided but not defined: -client", 2, true},
		{"Permission denied (publickey)", 255, false},
		{"SSH configuration: unknown command", 255, false},
		{"wt: command not found", 127, false},
		{"wing control socket does not exist", 1, false},
	} {
		t.Run(test.diagnostic, func(t *testing.T) {
			dir := t.TempDir()
			ssh := filepath.Join(dir, "ssh")
			script := fmt.Sprintf("#!/bin/sh\ncat >&2 <<'DIAGNOSTIC'\n%s\nDIAGNOSTIC\nexit %d\n", test.diagnostic, test.code)
			if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := (sshcontrol.Transport{SSHPath: ssh}).Inspect(t.Context(), "host", "", "", "")
			if err == nil || !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("lost inspect diagnostic: %v", err)
			}
			if got := strings.Contains(err.Error(), "remote wt is too old for this build") && strings.Contains(err.Error(), "--wt-binary PATH"); got != test.tooOld {
				t.Fatalf("too-old hint = %v, want %v: %v", got, test.tooOld, err)
			}
		})
	}
}

func TestSSHInspectRejectsInvalidBinaryBeforeSSH(t *testing.T) {
	for _, binary := range []string{"~/bin/wt", "bin/wt", "wt;true", "/bin/wt\n", "/bin/wt\x00"} {
		_, err := (sshcontrol.Transport{SSHPath: filepath.Join(t.TempDir(), "must-not-run-ssh")}).Inspect(t.Context(), "host", "", binary, "")
		if err == nil || !strings.Contains(err.Error(), "wt-binary") {
			t.Errorf("invalid binary %q: %v", binary, err)
		}
	}
}
func TestSSHInspectRejectsUnverifiedMetadata(t *testing.T) {
	h := testssh.New(t)
	transport := sshcontrol.Transport{SSHPath: h.SSHPath}
	for _, m := range []sshcontrol.Metadata{{WingID: "wing", Version: "old", WingthingDir: "/state", ControlSocket: "/s"}, {WingID: "wing", Version: control.ContractVersion, WingthingDir: "/state", ControlSocket: "/s:bad"}, {Version: control.ContractVersion, WingthingDir: "/state", ControlSocket: "/s"}} {
		h.Host(t, "host", m)
		if _, err := transport.Inspect(t.Context(), "host", "", "", ""); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
	if err := config.SaveRemotes(h.Root, map[string]config.Remote{"host": {SSHTarget: "host", WingID: "wing"}}); err == nil {
		t.Fatal("partial pin accepted")
	}
}
