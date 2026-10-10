package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testssh"
)

func TestMCPConnectRegistryAddListRemove(t *testing.T) {
	h := testssh.New(t)
	state := t.TempDir()
	t.Setenv("WINGTHING_DIR", state)
	h.Host(t, "host", sshcontrol.Metadata{WingID: "pinned-wing", Version: control.ContractVersion, WingthingDir: "/remote/canonical", ControlSocket: "/remote/control.sock"})
	var out bytes.Buffer
	streams := remotepkg.IO{SSHPath: h.SSHPath, Out: &out, ErrOut: &out}
	run := func(args ...string) error { return executeCLI(context.Background(), args, streams) }
	if err := run("mcp", "connect", "add", "forge", "--ssh", "host", "--wingthing-dir", "~/state"); err != nil {
		t.Fatal(err)
	}
	remotes, err := config.LoadRemotes(state)
	if err != nil || remotes["forge"].WingID != "pinned-wing" || remotes["forge"].WingthingDir != "/remote/canonical" {
		t.Fatalf("pin not persisted: %+v %v", remotes, err)
	}
	if err := run("remote", "ls"); err != nil || !strings.Contains(out.String(), "/remote/canonical") {
		t.Fatalf("shared registry: %v %s", err, &out)
	}
	if err := run("mcp", "connect", "ls"); err != nil {
		t.Fatal(err)
	}
	if err := run("mcp", "connect", "add", "forge", "--ssh", "host"); err == nil {
		t.Fatal("overwrote existing pin")
	}
	if err := run("mcp", "connect", "rm", "forge"); err != nil {
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
