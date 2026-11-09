package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"github.com/ehrlich-b/wingthing/internal/testssh"
)

func TestMCPConnectRegistryAddListRemove(t *testing.T) {
	h := testssh.New(t)
	state := testsocket.Dir(t)
	t.Setenv("WINGTHING_DIR", state)
	h.Host(t, "host", sshcontrol.Metadata{WingID: "pinned-wing", Version: control.ContractVersion, WingthingDir: "/remote/canonical", ControlSocket: "/remote/control.sock"})
	var out bytes.Buffer
	streams := remotepkg.IO{SSHPath: h.SSHPath, Out: &out, ErrOut: &out}
	run := func(args ...string) error { return executeCLI(context.Background(), args, streams) }
	if err := run("mcp", "connect", "add", "devbox", "--ssh", "host", "--wingthing-dir", "~/state", "--wt-binary", "/opt/wt builds/wt-dev"); err != nil {
		t.Fatal(err)
	}
	remotes, err := config.LoadRemotes(state)
	if err != nil || remotes["devbox"].WingID != "pinned-wing" || remotes["devbox"].WingthingDir != "/remote/canonical" || remotes["devbox"].WTBinary != "/opt/wt builds/wt-dev" {
		t.Fatalf("pin not persisted: %+v %v", remotes, err)
	}
	if err := run("remote", "ls"); err != nil || !strings.Contains(out.String(), "/remote/canonical") {
		t.Fatalf("shared registry: %v %s", err, &out)
	}
	out.Reset()
	if err := run("mcp", "connect", "ls"); err != nil || !strings.Contains(out.String(), "WT_BINARY") || !strings.Contains(out.String(), "/opt/wt builds/wt-dev") {
		t.Fatalf("binary not listed: %v %s", err, &out)
	}
	if err := run("mcp", "connect", "add", "devbox", "--ssh", "host"); err == nil {
		t.Fatal("overwrote existing pin")
	}
	if err := run("mcp", "connect", "rm", "devbox"); err != nil {
		t.Fatal(err)
	}
	remotes, err = config.LoadRemotes(state)
	if err != nil || len(remotes) != 0 {
		t.Fatalf("rm: %+v %v", remotes, err)
	}
	if err := run("mcp", "connect", "add", "bad.name", "--ssh", "host"); err == nil {
		t.Fatal("invalid name accepted")
	}
}

func TestMCPConnectAddRejectsInvalidBinaryBeforeSSHOrWrites(t *testing.T) {
	for _, binary := range []string{"", "~/bin/wt", "bin/wt", "./wt", "wt --client", "-wt", "/bin/wt;true", "/bin/$(wt)", "/bin/wt\n", "/bin/wt\x00"} {
		t.Run(binary, func(t *testing.T) {
			state := testsocket.Dir(t)
			t.Setenv("WINGTHING_DIR", state)
			err := executeCLI(context.Background(), []string{"mcp", "connect", "add", "devbox", "--ssh", "host", "--wt-binary=" + binary}, remotepkg.IO{Out: io.Discard, ErrOut: io.Discard, SSHPath: filepath.Join(state, "must-not-run-ssh")})
			if err == nil || !strings.Contains(err.Error(), "wt-binary") {
				t.Fatalf("invalid binary %q: %v", binary, err)
			}
			if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
				t.Fatalf("invalid binary wrote state: %v %v", entries, err)
			}
		})
	}
}

func TestMCPConnectVerifiesPreparedCLIProfileWithoutReplacingPins(t *testing.T) {
	h := testssh.New(t)
	state := testsocket.Dir(t)
	t.Setenv("WINGTHING_DIR", state)
	h.Host(t, "host", sshcontrol.Metadata{WingID: "pinned-wing", Version: control.ContractVersion, WingthingDir: "/remote/canonical", ControlSocket: "/remote/control.sock"})
	old := config.Remote{SSHTarget: "host", WTBinary: "/selected/wt", WingthingDir: "/selected/state"}
	if err := config.SaveRemotes(state, map[string]config.Remote{"fixture": old}); err != nil {
		t.Fatal(err)
	}
	run := func(host string) error {
		return executeCLI(context.Background(), []string{"mcp", "connect", "add", "fixture", "--ssh", host}, remotepkg.IO{SSHPath: h.SSHPath, Out: io.Discard, ErrOut: io.Discard})
	}
	if err := run("other-host"); err == nil || !strings.Contains(err.Error(), "another SSH target") {
		t.Fatal("route replacement accepted", err)
	}
	h.Offline(t, "host", true)
	if err := run("host"); err == nil {
		t.Fatal("offline verification accepted")
	}
	remotes, err := config.LoadRemotes(state)
	if err != nil || remotes["fixture"] != old {
		t.Fatal("failed handshake changed profile", remotes, err)
	}
	h.Offline(t, "host", false)
	if err := run("host"); err != nil {
		t.Fatal(err)
	}
	remotes, err = config.LoadRemotes(state)
	if err != nil || remotes["fixture"].WingID != "pinned-wing" || remotes["fixture"].WTBinary != old.WTBinary {
		t.Fatal(remotes, err)
	}
	inspected, err := os.ReadFile(filepath.Join(h.Root, "host.inspect"))
	if err != nil || !strings.Contains(string(inspected), old.WingthingDir) || !strings.Contains(string(inspected), old.WTBinary) {
		t.Fatal("saved binary/state not reused", err)
	}
	h.Host(t, "host", sshcontrol.Metadata{WingID: "replacement-wing", Version: control.ContractVersion, WingthingDir: "/replacement", ControlSocket: "/replacement/control.sock"})
	if err := run("host"); err == nil {
		t.Fatal("existing pin replaced")
	}
	unchanged, err := config.LoadRemotes(state)
	if err != nil || unchanged["fixture"] != remotes["fixture"] {
		t.Fatal("pin changed", unchanged, err)
	}
}

func TestMCPConnectUpgradeRejectsConcurrentRegistryChange(t *testing.T) {
	state := testsocket.Dir(t)
	t.Setenv("WINGTHING_DIR", state)
	original := config.Remote{SSHTarget: "host"}
	if err := config.SaveRemotes(state, map[string]config.Remote{"fixture": original}); err != nil {
		t.Fatal(err)
	}
	// Only this fixture transport changes the disposable registry during the
	// handshake. The production compare/update must preserve that new route.
	transport := filepath.Join(testsocket.Dir(t), "ssh")
	script := "#!/bin/sh\nprintf '%s\\n' 'remotes:' '  fixture:' '    ssh_target: concurrent-host' > " + remotepkg.ShellQuote(filepath.Join(state, "remotes.yaml")) + "\nprintf '%s\\n' '{\"wing_id\":\"verified-host\",\"version\":\"" + control.ContractVersion + "\",\"wingthing_dir\":\"/remote/state\",\"control_socket\":\"/remote/control.sock\"}'\n"
	if err := os.WriteFile(transport, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	err := executeCLI(context.Background(), []string{"mcp", "connect", "add", "fixture", "--ssh", "host"}, remotepkg.IO{SSHPath: transport, Out: io.Discard, ErrOut: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "changed during verification") {
		t.Fatal(err)
	}
	saved, err := config.LoadRemotes(state)
	if err != nil || saved["fixture"].SSHTarget != "concurrent-host" || saved["fixture"].WingID != "" {
		t.Fatal(saved, err)
	}
}
