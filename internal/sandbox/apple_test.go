//go:build darwin

package sandbox

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestHasNetwork(t *testing.T) {
	tests := []struct {
		level Level
		want  bool
	}{
		{Strict, false},
		{Standard, false},
		{Network, true},
		{Privileged, true},
	}
	for _, tt := range tests {
		if got := hasNetwork(tt.level); got != tt.want {
			t.Errorf("hasNetwork(%s) = %v, want %v", tt.level, got, tt.want)
		}
	}
}

func TestBuildProfileNetworkDeny(t *testing.T) {
	profile := buildProfile(Config{Isolation: Standard})
	if !strings.Contains(profile, "(deny network*)") {
		t.Errorf("standard isolation profile should deny network, got:\n%s", profile)
	}
}

func TestBuildProfileNetworkAllow(t *testing.T) {
	profile := buildProfile(Config{Isolation: Network})
	if strings.Contains(profile, "(deny network*)") {
		t.Errorf("network isolation profile should not deny network, got:\n%s", profile)
	}
}

func TestBuildProfileDenyPaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	profile := buildProfile(Config{
		Isolation: Standard,
		Deny:      []string{home + "/.ssh", home + "/.gnupg"},
	})
	if !strings.Contains(profile, home+"/.ssh") {
		t.Errorf("profile should deny .ssh, got:\n%s", profile)
	}
	if !strings.Contains(profile, home+"/.gnupg") {
		t.Errorf("profile should deny .gnupg, got:\n%s", profile)
	}
}

func TestBuildProfileMountWriteIsolation(t *testing.T) {
	home, _ := os.UserHomeDir()
	profile := buildProfile(Config{
		Isolation: Standard,
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
		cfg:     Config{Isolation: Standard},
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

// Integration tests — actually run sandboxed processes

func TestSeatbeltNetworkBlocked(t *testing.T) {
	sb, err := newPlatform(Config{Isolation: Standard})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer sb.Destroy()

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
	sb, err := newPlatform(Config{Isolation: Network})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer sb.Destroy()

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
	// Create a temp file, deny access to its directory
	tmpDir := t.TempDir()
	testFile := tmpDir + "/secret.txt"
	os.WriteFile(testFile, []byte("secret"), 0644)

	sb, err := newPlatform(Config{
		Isolation: Network,
		Deny:      []string{tmpDir},
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer sb.Destroy()

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

func TestSeatbeltWriteRestriction(t *testing.T) {
	home, _ := os.UserHomeDir()
	jail := t.TempDir()

	sb, err := newPlatform(Config{
		Isolation: Network,
		Mounts: []Mount{
			{Source: jail, Target: jail},
		},
	})
	if err != nil {
		t.Fatalf("newPlatform: %v", err)
	}
	defer sb.Destroy()

	// Write inside mount should succeed
	cmd, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "echo ok > " + jail + "/test.txt"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("write to mount path should succeed: %v", err)
	}
	os.Remove(jail + "/test.txt")

	// Write outside mount (in home) should fail
	target := home + "/wt-sandbox-test-delete-me"
	cmd2, err := sb.Exec(context.Background(), "/bin/sh", []string{"-c", "echo fail > " + target})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	err = cmd2.Run()
	os.Remove(target) // clean up in case it leaked
	if err == nil {
		t.Fatal("expected write outside mount to fail, but it succeeded")
	}
}
