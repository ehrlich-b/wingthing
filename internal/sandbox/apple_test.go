//go:build darwin

package sandbox

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sandboxExecAvailable(t *testing.T) {
	t.Helper()
	cmd := exec.Command("sandbox-exec", "-p", "(version 1)(allow default)", "/bin/echo", "ok")
	if err := cmd.Run(); err != nil {
		t.Skip("sandbox-exec unavailable (nested sandbox or SIP): ", err)
	}
}

func destroySandboxForTest(t *testing.T, sb Sandbox) {
	t.Helper()
	if err := sb.Destroy(); err != nil {
		t.Errorf("destroy sandbox: %v", err)
	}
}

func TestBuildProfileNetworkDeny(t *testing.T) {
	profile := buildProfile(Config{NetworkNeed: NetworkNone})
	if !strings.Contains(profile, "(deny network*)") {
		t.Errorf("NetworkNone profile should deny network, got:\n%s", profile)
	}
}

func TestBuildProfileNetworkAllow(t *testing.T) {
	profile := buildProfile(Config{NetworkNeed: NetworkFull})
	if strings.Contains(profile, "(deny network*)") {
		t.Errorf("NetworkFull profile should not deny network, got:\n%s", profile)
	}
}

func TestBuildProfileDenyPaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	profile := buildProfile(Config{
		NetworkNeed: NetworkNone,
		Deny:        []string{home + "/.ssh", home + "/.gnupg"},
	})
	if !strings.Contains(profile, home+"/.ssh") {
		t.Errorf("profile should deny .ssh, got:\n%s", profile)
	}
	if !strings.Contains(profile, home+"/.gnupg") {
		t.Errorf("profile should deny .gnupg, got:\n%s", profile)
	}
}

func TestBuildProfileDenyPathCoversExactMissingPathAndDescendants(t *testing.T) {
	missing := filepath.Join(t.TempDir(), ".aws")
	canonical, err := canonicalSandboxPath(missing)
	if err != nil {
		t.Fatal(err)
	}
	profile := buildProfile(Config{Deny: []string{missing}})
	for _, want := range []string{
		`(deny file-read* file-write* (literal "` + canonical + `"))`,
		`(deny file-read* file-write* (subpath "` + canonical + `"))`,
		`(deny network-outbound (literal "` + canonical + `"))`,
		`(deny network-outbound (subpath "` + canonical + `"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing %s:\n%s", want, profile)
		}
	}
}

func TestBuildProfileDenyPathOverridesAllowedSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "agent.sock")
	canonical, err := canonicalSandboxPath(socket)
	if err != nil {
		t.Fatal(err)
	}
	profile := buildProfile(Config{
		NetworkNeed:  NetworkFull,
		AllowSockets: []string{socket},
		Deny:         []string{socket},
	})
	allow := `(allow network-outbound (literal "` + canonical + `"))`
	deny := `(deny network-outbound (literal "` + canonical + `"))`
	allowIndex := strings.Index(profile, allow)
	denyIndex := strings.Index(profile, deny)
	if allowIndex < 0 || denyIndex < 0 || denyIndex < allowIndex {
		t.Fatalf("socket deny must follow overlapping allow: allow=%d deny=%d\n%s", allowIndex, denyIndex, profile)
	}
}

func TestBuildProfileDenyPathOverridesWritableMount(t *testing.T) {
	denied := t.TempDir()
	canonical, err := canonicalSandboxPath(denied)
	if err != nil {
		t.Fatal(err)
	}
	profile := buildProfile(Config{
		Mounts: []Mount{{Source: denied, Target: denied}},
		Deny:   []string{denied},
	})
	allow := `(allow file-write* (subpath "` + canonical + `"))`
	deny := `(deny file-read* file-write* (subpath "` + canonical + `"))`
	allowIndex := strings.Index(profile, allow)
	denyIndex := strings.Index(profile, deny)
	if allowIndex < 0 || denyIndex < 0 || denyIndex < allowIndex {
		t.Fatalf("deny must follow overlapping writable mount allow: allow=%d deny=%d\n%s", allowIndex, denyIndex, profile)
	}
}

func TestCanonicalSandboxPathResolvesMissingPathThroughSymlinkedAncestor(t *testing.T) {
	parent := t.TempDir()
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(parent, "missing", "credential")
	got, err := canonicalSandboxPath(missing)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realParent, "missing", "credential")
	if got != want {
		t.Fatalf("canonicalSandboxPath(%q) = %q, want %q", missing, got, want)
	}
}

func TestBuildProfileMountWriteIsolation(t *testing.T) {
	home, _ := os.UserHomeDir()
	profile := buildProfile(Config{
		NetworkNeed: NetworkNone,
		Mounts: []Mount{
			{Source: home + "/scratch/jail", Target: home + "/scratch/jail"},
		},
	})
	// Should deny writes to home
	if !strings.Contains(profile, "(deny file-write* (subpath \""+home+"\"))") {
		t.Errorf("profile should deny writes to home, got:\n%s", profile)
	}
	// Should allow writes to mount path
	if !strings.Contains(profile, "(allow file-write* (subpath \""+home+"/scratch/jail\"))") {
		t.Errorf("profile should allow writes to mount, got:\n%s", profile)
	}
}

func TestSeatbeltExecBuildsCommand(t *testing.T) {
	sb := &seatbeltSandbox{
		cfg:     Config{NetworkNeed: NetworkNone},
		profile: "(version 1)(allow default)",
		tmpDir:  "/tmp/test",
	}
	cmd, err := sb.Exec(context.Background(), "echo", []string{"hello"})
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}
	args := cmd.Args
	if len(args) < 4 {
		t.Fatalf("expected at least 4 args, got %d: %v", len(args), args)
	}
	// args: [sandbox-exec, -p, <profile>, echo, hello]
	if args[1] != "-p" {
		t.Errorf("args[1] = %q, want -p", args[1])
	}
	if args[3] != "echo" {
		t.Errorf("args[3] = %q, want echo", args[3])
	}
	if args[4] != "hello" {
		t.Errorf("args[4] = %q, want hello", args[4])
	}
}

func TestBuildProfileDenyWritePaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	projectDir := home + "/project"
	eggYaml := projectDir + "/egg.yaml"
	profile := buildProfile(Config{
		NetworkNeed: NetworkFull,
		Mounts:      []Mount{{Source: projectDir, ReadOnly: false}},
		DenyWrite:   []string{eggYaml},
	})
	// Should contain a deny file-write* with literal for the specific file
	want := `(deny file-write* (literal "` + eggYaml + `"))`
	if !strings.Contains(profile, want) {
		t.Errorf("profile should deny writes to egg.yaml, got:\n%s", profile)
	}
	// deny-write must come AFTER mount allows so it takes precedence in SBPL
	mountAllow := `(allow file-write* (subpath "` + projectDir + `"))`
	mountIdx := strings.Index(profile, mountAllow)
	denyIdx := strings.Index(profile, want)
	if mountIdx < 0 || denyIdx < 0 {
		t.Fatalf("profile missing expected rules:\n%s", profile)
	}
	if denyIdx < mountIdx {
		t.Errorf("deny-write rule must come AFTER mount allow to take precedence in SBPL.\nmount allow at %d, deny-write at %d\nprofile:\n%s", mountIdx, denyIdx, profile)
	}
	// Should NOT deny reads
	denyRead := `(deny file-read* (literal "` + eggYaml + `"))`
	if strings.Contains(profile, denyRead) {
		t.Error("deny-write should not block reads")
	}
}

// Integration tests — actually run sandboxed processes

func TestSeatbeltNetworkBlocked(t *testing.T) {
	sandboxExecAvailable(t)
	sb, err := newPlatform(Config{NetworkNeed: NetworkNone})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer func() {
		if err := sb.Destroy(); err != nil {
			t.Errorf("destroy sandbox: %v", err)
		}
	}()

	cmd, err := sb.Exec(context.Background(), "/usr/bin/curl", []string{"-s", "--max-time", "3", "https://example.com"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	if err == nil {
		t.Fatal("expected curl to fail with network denied, but it succeeded")
	}
}

func TestSeatbeltNetworkAllowed(t *testing.T) {
	sandboxExecAvailable(t)
	sb, err := newPlatform(Config{NetworkNeed: NetworkFull})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer func() {
		if err := sb.Destroy(); err != nil {
			t.Errorf("destroy sandbox: %v", err)
		}
	}()

	// Just verify the process runs — don't actually hit the network in tests
	cmd, err := sb.Exec(context.Background(), "/bin/echo", []string{"network-ok"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "network-ok" {
		t.Errorf("output = %q, want %q", got, "network-ok")
	}
}

func TestSeatbeltDenyPathBlocked(t *testing.T) {
	sandboxExecAvailable(t)
	// Create a temp file, deny access to its directory
	tmpDir := t.TempDir()
	testFile := tmpDir + "/secret.txt"
	if err := os.WriteFile(testFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb, err := newPlatform(Config{
		NetworkNeed: NetworkFull,
		Deny:        []string{tmpDir},
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer destroySandboxForTest(t, sb)

	cmd, err := sb.Exec(context.Background(), "/bin/cat", []string{testFile})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	if err == nil {
		t.Fatal("expected cat to fail on denied path, but it succeeded")
	}
}

func TestSeatbeltSSHKnownHostsExceptionSurvivesExactDirectoryDeny(t *testing.T) {
	sandboxExecAvailable(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	sshDir := filepath.Join(home, ".ssh")
	knownHosts := filepath.Join(sshDir, "known_hosts")
	if _, err := os.Stat(knownHosts); err != nil {
		t.Skip("no SSH known_hosts file to exercise: ", err)
	}

	allowed, err := newPlatform(Config{NetworkNeed: NetworkFull, Deny: []string{sshDir}})
	if err != nil {
		t.Fatal(err)
	}
	defer destroySandboxForTest(t, allowed)
	cmd, err := allowed.Exec(context.Background(), "/usr/bin/head", []string{"-c", "1", knownHosts})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("known_hosts exception was blocked by exact directory deny: %v", err)
	}

	blocked, err := newPlatform(Config{NetworkNeed: NetworkFull, Deny: []string{sshDir, knownHosts}})
	if err != nil {
		t.Fatal(err)
	}
	defer destroySandboxForTest(t, blocked)
	cmd, err = blocked.Exec(context.Background(), "/usr/bin/head", []string{"-c", "1", knownHosts})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err == nil {
		t.Fatal("explicit known_hosts deny was bypassed")
	}
}

func TestSeatbeltWriteRestriction(t *testing.T) {
	sandboxExecAvailable(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	jail := t.TempDir()

	sb, err := newPlatform(Config{
		NetworkNeed: NetworkFull,
		Mounts: []Mount{
			{Source: jail, Target: jail},
		},
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer destroySandboxForTest(t, sb)

	// Write inside mount should succeed
	cmd, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "echo ok > " + jail + "/test.txt"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("write to mount path should succeed: %v", err)
	}
	if err := os.Remove(jail + "/test.txt"); err != nil {
		t.Fatal(err)
	}

	// Write outside mount (in home) should fail
	target := home + "/wt-sandbox-test-delete-me"
	cmd2, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "echo fail > " + target})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	err = cmd2.Run()
	if removeErr := os.Remove(target); removeErr != nil && !os.IsNotExist(removeErr) { // clean up in case it leaked
		t.Fatal(removeErr)
	}
	if err == nil {
		t.Fatal("expected write outside mount to fail, but it succeeded")
	}
}

func TestSeatbeltDenyWriteBlocksWrite(t *testing.T) {
	sandboxExecAvailable(t)
	// Create a file that should be readable but not writable.
	// Include a writable mount for the parent dir — this is the real scenario:
	// the project dir is rw-mounted AND egg.yaml inside it is deny-write.
	// Without the mount, deny-write trivially works (no competing allow rule).
	tmpDir := t.TempDir()
	protectedFile := tmpDir + "/egg.yaml"
	if err := os.WriteFile(protectedFile, []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb, err := newPlatform(Config{
		NetworkNeed: NetworkFull,
		Mounts:      []Mount{{Source: tmpDir, ReadOnly: false}},
		DenyWrite:   []string{protectedFile},
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer destroySandboxForTest(t, sb)

	// Read should succeed
	cmd, err := sb.Exec(context.Background(), "/bin/cat", []string{protectedFile})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("reading deny-write file should succeed: %v", err)
	}
	if got := out.String(); got != "original content" {
		t.Errorf("read content = %q, want %q", got, "original content")
	}

	// Write should fail
	cmd2, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "echo hacked > " + protectedFile})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	err = cmd2.Run()
	if err == nil {
		// Verify file wasn't modified
		data, _ := os.ReadFile(protectedFile)
		if string(data) != "original content" {
			t.Fatal("deny-write file was modified!")
		}
		t.Fatal("expected write to deny-write file to fail, but it succeeded")
	}
}
