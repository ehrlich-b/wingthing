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
	if err := run("mcp", "connect", "add", "forge", "--ssh", "host", "--wingthing-dir", "~/state", "--wt-binary", "/opt/wt builds/wt-dev"); err != nil {
		t.Fatal(err)
	}
	remotes, err := config.LoadRemotes(state)
	if err != nil || remotes["forge"].WingID != "pinned-wing" || remotes["forge"].WingthingDir != "/remote/canonical" || remotes["forge"].WTBinary != "/opt/wt builds/wt-dev" {
		t.Fatalf("pin not persisted: %+v %v", remotes, err)
	}
	if err := run("remote", "ls"); err != nil || !strings.Contains(out.String(), "/remote/canonical") {
		t.Fatalf("shared registry: %v %s", err, &out)
	}
	out.Reset()
	if err := run("mcp", "connect", "ls"); err != nil || !strings.Contains(out.String(), "WT_BINARY") || !strings.Contains(out.String(), "/opt/wt builds/wt-dev") {
		t.Fatalf("binary not listed: %v %s", err, &out)
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

func TestMCPConnectAddRejectsInvalidBinaryBeforeSSHOrWrites(t *testing.T) {
	for _, binary := range []string{"", "~/bin/wt", "bin/wt", "./wt", "wt --client", "-wt", "/bin/wt;true", "/bin/$(wt)", "/bin/wt\n", "/bin/wt\x00"} {
		t.Run(binary, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("WINGTHING_DIR", state)
			err := executeCLI(context.Background(), []string{"mcp", "connect", "add", "forge", "--ssh", "host", "--wt-binary=" + binary}, remotepkg.IO{Out: io.Discard, ErrOut: io.Discard, SSHPath: filepath.Join(state, "must-not-run-ssh")})
			if err == nil || !strings.Contains(err.Error(), "wt-binary") {
				t.Fatalf("invalid binary %q: %v", binary, err)
			}
			if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
				t.Fatalf("invalid binary wrote state: %v %v", entries, err)
			}
		})
	}
}
