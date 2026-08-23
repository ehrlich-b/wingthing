package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestValidateSessionName(t *testing.T) {
	for _, valid := range []string{"", "work", "dev-server", "api_2", "repo.main"} {
		if err := validateSessionName(valid); err != nil {
			t.Errorf("validateSessionName(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{"-option", ".hidden", "two words", "a/b", "x\ncommand"} {
		if err := validateSessionName(invalid); err == nil {
			t.Errorf("validateSessionName(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestSessionNameRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := writeSessionName(dir, "api-server"); err != nil {
		t.Fatalf("writeSessionName: %v", err)
	}
	if got := readSessionName(dir); got != "api-server" {
		t.Fatalf("readSessionName = %q, want api-server", got)
	}
	info, err := os.Stat(filepath.Join(dir, sessionNameFile))
	if err != nil {
		t.Fatalf("stat session name: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("session name mode = %o, want 600", got)
	}
	if err := writeSessionName(dir, ""); err != nil {
		t.Fatalf("remove session name: %v", err)
	}
	if got := readSessionName(dir); got != "" {
		t.Fatalf("removed session name = %q, want empty", got)
	}
}

func TestReadSessionNameRejectsInvalidFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, sessionNameFile), []byte("bad\x1b[2Jname\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := readSessionName(dir); got != "" {
		t.Fatalf("readSessionName = %q, want invalid name ignored", got)
	}
}

func TestReadEggMetaValuesPreservesEquals(t *testing.T) {
	dir := t.TempDir()
	data := []byte("kind=command\ncommand=\"sh\" \"-c\" \"A=B echo $A\"\ncwd=/tmp/project\n")
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), data, 0600); err != nil {
		t.Fatal(err)
	}
	meta := readEggMetaValues(dir)
	if got, want := meta["command"], `"sh" "-c" "A=B echo $A"`; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestOwnedProcessSignalPermissionDeniedMeansRecycledPID(t *testing.T) {
	if ownedProcessSignalIndicatesAlive(syscall.EPERM) {
		t.Fatal("permission-denied owned-process probe was treated as the recorded child")
	}
}

func TestSessionDiscoveryDoesNotCleanDeadMetadata(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "eggs", "stale")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(dir, "egg.pid")
	if err := os.WriteFile(pidPath, []byte("-1"), 0600); err != nil {
		t.Fatal(err)
	}
	if sessions, err := discoverSessionRefs(&config.Config{Dir: root}); err != nil || len(sessions) != 0 {
		t.Fatalf("discover sessions = %#v, %v", sessions, err)
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("read-only session discovery removed metadata: %v", err)
	}
}

func TestEggEnvironmentUsesOwnerOnlyOneShotFile(t *testing.T) {
	dir := t.TempDir()
	secret := "must-not-enter-argv"
	args, path, err := prepareEggEnvironmentTransport(dir, []string{"egg", "run", "--session-id", "session"}, map[string]string{"OPENAI_API_KEY": secret})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("environment file mode = %o, want 600", info.Mode().Perm())
	}
	for _, arg := range args {
		if strings.Contains(arg, secret) {
			t.Fatal("environment secret entered child argv")
		}
	}
	if args[len(args)-1] != "--env-file-required" {
		t.Fatalf("environment transport args = %#v", args)
	}
	environment, err := readEggEnvironment(path, []string{"TERM=xterm"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if environment["OPENAI_API_KEY"] != secret || environment["TERM"] != "xterm" {
		t.Fatalf("decoded environment = %#v", environment)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("environment file survived read: %v", err)
	}
}

func TestEggRunRejectsTraversalSessionIDBeforeEnvironmentRead(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WINGTHING_DIR", state)
	payload := filepath.Join(state, "escape", ".egg.env")
	if err := os.MkdirAll(filepath.Dir(payload), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte(`{"SECRET":"must-survive"}`), 0600); err != nil {
		t.Fatal(err)
	}

	command := eggRunCmd()
	command.SetArgs([]string{"--session-id", "../escape", "--env-file-required"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "invalid session ID") {
		t.Fatalf("traversal session error = %v", err)
	}
	if _, err := os.Stat(payload); err != nil {
		t.Fatalf("invalid session ID consumed environment payload: %v", err)
	}
}
