package sshcontrol_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testssh"
)

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
	remote, err := transport.Inspect(ctx, "host", "~/state with space/it's isolated", "client")
	if err != nil {
		t.Fatal(err)
	}
	if remote.WingID != "remote-wing" || remote.WingthingDir != meta.WingthingDir {
		t.Fatalf("metadata: %+v", remote)
	}
	wire, err := os.ReadFile(filepath.Join(h.Root, "host.inspect"))
	if err != nil {
		t.Fatal(err)
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
func TestSSHInspectRejectsUnverifiedMetadata(t *testing.T) {
	h := testssh.New(t)
	transport := sshcontrol.Transport{SSHPath: h.SSHPath}
	for _, m := range []sshcontrol.Metadata{{WingID: "wing", Version: "old", WingthingDir: "/state", ControlSocket: "/s"}, {WingID: "wing", Version: control.ContractVersion, WingthingDir: "/state", ControlSocket: "/s:bad"}, {Version: control.ContractVersion, WingthingDir: "/state", ControlSocket: "/s"}} {
		h.Host(t, "host", m)
		if _, err := transport.Inspect(t.Context(), "host", "", ""); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
	if err := config.SaveRemotes(h.Root, map[string]config.Remote{"host": {SSHTarget: "host", WingID: "wing"}}); err == nil {
		t.Fatal("partial pin accepted")
	}
}
