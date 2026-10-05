package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func writeFakeRemoteSSH(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeInventorySSH(t *testing.T, inventory remoteSessionInventory) string {
	t.Helper()
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	return writeFakeRemoteSSH(t, "case \"$3\" in\n"+
		"*\"'--version'\"*) printf '%s\\n' 'wt version remote-test' ;;\n"+
		"*\"'session' 'ps' '--json' '--remote-inventory'\"*) printf '%s\\n' "+remotepkg.ShellQuote(string(data))+" ;;\n"+
		"*) printf 'unexpected command: %s\\n' \"$3\" >&2; exit 9 ;;\nesac\n")
}

func seedRemoteListSession(t *testing.T, cfg *config.Config, id, principal string) {
	t.Helper()
	dir := filepath.Join(cfg.Dir, "eggs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for file, value := range map[string]string{
		"egg.pid": fmt.Sprint(os.Getpid()), "egg.meta": "kind=command\ncommand=/bin/sh\ncwd=/tmp/work\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeSessionPrincipal(dir, principal); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredRemoteCommandsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	var out bytes.Buffer
	streams := remotepkg.IO{Out: &out, ErrOut: io.Discard, SSHPath: filepath.Join(dir, "must-not-run-ssh")}
	run := func(args ...string) error { return executeCLI(context.Background(), args, streams) }
	if err := run("remote", "add", "work", "me@host", "--wingthing-dir", "/home/me/state with space"); err != nil {
		t.Fatal(err)
	}
	if err := run("remote", "add", "lab", "lab-alias"); err != nil {
		t.Fatal(err)
	}
	if err := run("remote", "add", "work", "other"); err == nil {
		t.Fatal("duplicate add succeeded")
	}
	if err := run("remote", "ls"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "lab-alias", "me@host", "/home/me/state with space"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("listing missing %q: %s", want, &out)
		}
	}
	if strings.Index(out.String(), "lab-alias") > strings.Index(out.String(), "me@host") {
		t.Fatal("listing not sorted by name")
	}
	if err := run("remote", "rm", "work"); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadRemotes(dir)
	if err != nil || !reflect.DeepEqual(got, map[string]config.Remote{"lab": {SSHTarget: "lab-alias"}}) {
		t.Fatalf("registry after rm = %#v, %v", got, err)
	}
	if err := run("remote", "rm", "work"); err == nil {
		t.Fatal("removing unknown remote succeeded")
	}
}

func TestConfiguredRemoteCommandsValidateBeforeWriting(t *testing.T) {
	for _, args := range [][]string{
		{"remote", "add", "bad.name", "host"},
		{"remote", "add", "work", "-option"},
		{"remote", "add", "work", "host\targ"},
		{"remote", "add", "work", "host", "--wingthing-dir", "relative"},
		{"remote", "add", "work", "host", "--wingthing-dir="},
	} {
		dir := t.TempDir()
		t.Setenv("WINGTHING_DIR", dir)
		if err := executeCLI(context.Background(), args, remotepkg.IO{Out: io.Discard, ErrOut: io.Discard}); err == nil {
			t.Errorf("accepted invalid add %v", args)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("invalid add wrote files: %v", entries)
		}
	}
	for _, target := range []string{"host arg", "host\targ", "host\x1b", "host\x7f"} {
		if _, _, err := remotepkg.ParseRemoteInvocation([]string{"--remote", target, "attach", "session"}, false, newRootCommand); err == nil {
			t.Errorf("explicit --remote accepted invalid target %q", target)
		}
	}
}

func TestParseRemoteSession(t *testing.T) {
	for _, test := range []struct{ ref, name, session string }{
		{"", "", ""}, {"review", "", "review"}, {"abc123", "", "abc123"},
		{"work:review", "work", "review"}, {"Work_2:abc123", "Work_2", "abc123"}, {"dev-host:repo.main", "dev-host", "repo.main"},
	} {
		name, session, err := parseRemoteSession(test.ref)
		if err != nil || name != test.name || session != test.session {
			t.Errorf("parse %q = %q, %q, %v", test.ref, name, session, err)
		}
	}
	for _, invalid := range []string{":session", "work:", "bad.name:session", "work:a:b", "two words:session"} {
		if _, _, err := parseRemoteSession(invalid); err == nil {
			t.Errorf("accepted invalid reference %q", invalid)
		}
	}
}

func TestConfiguredRemoteAttachUsesRunner(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	if err := config.SaveRemotes(dir, map[string]config.Remote{"work": {SSHTarget: "me@host", WingthingDir: "/home/me/wt state/it's isolated"}}); err != nil {
		t.Fatal(err)
	}
	sshPath := writeFakeRemoteSSH(t, "printf '%s\\n' \"$1\" \"$2\" \"$3\"\ncat\n")
	for _, flag := range []string{"--read-only", "--takeover"} {
		var out bytes.Buffer
		err := executeCLI(context.Background(), []string{"attach", "work:review", flag}, remotepkg.IO{
			In: strings.NewReader("attach input"), Out: &out, ErrOut: io.Discard,
			StdinTTY: true, StdoutTTY: true, SSHPath: sshPath,
		})
		if err != nil {
			t.Fatal(err)
		}
		want := "-t\nme@host\nWINGTHING_DIR=" + remotepkg.ShellQuote("/home/me/wt state/it's isolated") +
			" WINGTHING_PREVIEW_DIR=" + remotepkg.ShellQuote("/home/me/wt state/it's isolated") +
			" 'wt' '--expected-channel' 'stable' 'attach' " + remotepkg.ShellQuote(flag) + " '--' 'review'\nattach input"
		if out.String() != want {
			t.Fatalf("attach transport = %q, want %q", out.String(), want)
		}
	}
	var out bytes.Buffer
	if err := executeCLI(context.Background(), []string{"attach", "work:--takeover"}, remotepkg.IO{Out: &out, ErrOut: io.Discard, SSHPath: sshPath}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "-T\nme@host\n") || !strings.Contains(out.String(), "'attach' '--' '--takeover'") {
		t.Fatalf("session reference became an option or noninteractive attach allocated a TTY: %q", out.String())
	}
	if err := executeCLI(context.Background(), []string{"attach", "missing:review"}, remotepkg.IO{SSHPath: sshPath}); err == nil || !strings.Contains(err.Error(), `unknown remote "missing"`) {
		t.Fatalf("unknown remote: %v", err)
	}
	if err := executeCLI(context.Background(), []string{"attach", "review"}, remotepkg.IO{SSHPath: filepath.Join(dir, "must-not-run-ssh")}); err == nil || strings.Contains(err.Error(), "ssh") {
		t.Fatalf("plain SESSION did not stay local: %v", err)
	}
}

func TestSessionPSAggregatesHealthyAndFailingFakeRemotesInParallel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	t.Setenv("WT_REMOTE_TEST_DIR", t.TempDir())
	cfg := &config.Config{Dir: dir}
	seedRemoteListSession(t, cfg, "local-session", "")
	if err := config.SaveRemotes(dir, map[string]config.Remote{"work": {SSHTarget: "healthy"}, "down": {SSHTarget: "down"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(remoteSessionInventory{Version: "remote-test", ContractVersion: remoteSessionContractVersion,
		Sessions: []localSession{{ID: "remote-session", Name: "review", Kind: "agent", Agent: "codex"}}})
	sshPath := writeFakeRemoteSSH(t, "touch \"$WT_REMOTE_TEST_DIR/$2\"\n"+
		"count=0\nwhile ! test -f \"$WT_REMOTE_TEST_DIR/healthy\" || ! test -f \"$WT_REMOTE_TEST_DIR/down\"; do\n"+
		"  count=$((count+1)); if test \"$count\" -gt 100; then echo 'remotes were not queried in parallel' >&2; exit 8; fi\n"+
		"  sleep 0.01\ndone\n"+
		"if test \"$2\" = down; then echo 'host is unreachable' >&2; exit 255; fi\n"+
		"case \"$3\" in\n*\"'--version'\"*) echo 'wt version remote-test' ;;\n"+
		"*\"'session' 'ps' '--json' '--remote-inventory'\"*) printf '%s\\n' "+remotepkg.ShellQuote(string(data))+" ;;\n"+
		"*) exit 9 ;;\nesac\n")
	for _, jsonOutput := range []bool{true, false} {
		var out bytes.Buffer
		args := []string{"session", "ps"}
		if jsonOutput {
			args = append(args, "--json")
		}
		if err := executeCLI(context.Background(), args, remotepkg.IO{Out: &out, ErrOut: io.Discard, SSHPath: sshPath}); err != nil {
			t.Fatalf("down remote failed the command: %v", err)
		}
		if !jsonOutput {
			for _, want := range []string{"MACHINE", "local-session", "remote-session", "down", "host is unreachable"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("table missing %q: %s", want, &out)
				}
			}
			continue
		}
		var rows []machineSession
		if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 || rows[0].Machine != "local" || rows[0].ID != "local-session" ||
			rows[1].Machine != "down" || !strings.Contains(rows[1].Error, "host is unreachable") ||
			rows[2].Machine != "work" || rows[2].ID != "remote-session" || rows[2].Error != "" {
			t.Fatalf("aggregated rows = %#v", rows)
		}
	}
}

func TestSessionPSRemoteTimeoutProducesOneRowPerRemote(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	if err := config.SaveRemotes(cfg.Dir, map[string]config.Remote{"one": {SSHTarget: "one"}, "two": {SSHTarget: "two"}}); err != nil {
		t.Fatal(err)
	}
	sshPath := writeFakeRemoteSSH(t, "exec sleep 30\n")
	start := time.Now()
	rows, err := discoverMachineSessions(context.Background(), cfg, remotepkg.IO{SSHPath: sshPath})
	if err != nil || len(rows) != 2 {
		t.Fatalf("timed out remotes: %#v, %v", rows, err)
	}
	if time.Since(start) > remoteSessionTimeout+time.Second {
		t.Fatal("remote timeouts ran sequentially or were not bounded")
	}
	for _, row := range rows {
		if !strings.Contains(row.Error, "context deadline exceeded") || !strings.Contains(row.Error, row.Machine) {
			t.Errorf("timeout row missing diagnostic: %#v", row)
		}
	}
}

func TestRemoteTimeoutBoundsInheritedOutputPipes(t *testing.T) {
	// The shell is canceled first; its short-lived child still owns the pipes.
	sshPath := writeFakeRemoteSSH(t, "sleep 1\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := queryRemoteSessions(ctx, "slow", config.Remote{SSHTarget: "host"}, remotepkg.IO{SSHPath: sshPath})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("timeout diagnostic: %v", err)
	}
	if time.Since(start) > 700*time.Millisecond {
		t.Fatal("inherited SSH output pipes defeated the remote deadline")
	}
}

func TestSessionPSRemoteInventorySkipsConfiguredRemotes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	// Even a malformed registry is irrelevant to a receiver's inventory.
	if err := os.WriteFile(filepath.Join(dir, "remotes.yaml"), []byte("invalid yaml: ["), 0600); err != nil {
		t.Fatal(err)
	}
	seedRemoteListSession(t, &config.Config{Dir: dir}, "receiver-session", "")
	var out bytes.Buffer
	if err := executeCLI(context.Background(), []string{"session", "ps", "--json", "--remote-inventory"}, remotepkg.IO{
		Out: &out, ErrOut: io.Discard, SSHPath: filepath.Join(dir, "must-not-run-ssh"),
	}); err != nil {
		t.Fatal(err)
	}
	var inventory remoteSessionInventory
	if err := json.Unmarshal(out.Bytes(), &inventory); err != nil || inventory.Version != version || inventory.ContractVersion != remoteSessionContractVersion || len(inventory.Sessions) != 1 || inventory.Sessions[0].ID != "receiver-session" {
		t.Fatalf("receiver inventory: %s, %v", &out, err)
	}
}

func TestSessionPSRemoteVersionMismatchNamesBothVersions(t *testing.T) {
	for _, test := range []struct{ name, response, contract string }{
		{"old ps", "echo 'unknown command ps' >&2; exit 1", "unsupported"},
		{"old json", "echo 'unknown flag: --json' >&2; exit 1", "unsupported"},
		{"old inventory", "echo 'unknown flag: --remote-inventory' >&2; exit 1", "unsupported"},
		{"different contract", "echo '{\"version\":\"v0.1.0\",\"contract_version\":\"v2\",\"sessions\":[]}'", "v2"},
		{"unversioned array", "echo '[]'", "unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			if err := config.SaveRemotes(cfg.Dir, map[string]config.Remote{"old": {SSHTarget: "host"}}); err != nil {
				t.Fatal(err)
			}
			sshPath := writeFakeRemoteSSH(t, "case \"$3\" in\n*\"'--version'\"*) echo 'wt version v0.1.0' ;;\n*) "+test.response+" ;;\nesac\n")
			rows, err := discoverMachineSessions(context.Background(), cfg, remotepkg.IO{SSHPath: sshPath})
			if err != nil || len(rows) != 1 || rows[0].Machine != "old" {
				t.Fatalf("mismatch rows: %#v, %v", rows, err)
			}
			for _, want := range []string{`remote "old"`, "version mismatch", "local wt " + version, "wt version v0.1.0", "session contract v1", "session contract " + test.contract} {
				if !strings.Contains(rows[0].Error, want) {
					t.Errorf("mismatch diagnostic missing %q: %s", want, rows[0].Error)
				}
			}
		})
	}
}
