//go:build linux

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")

	if err := os.WriteFile(src, []byte("hello overlay"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(data) != "hello overlay" {
		t.Errorf("dst = %q, want %q", string(data), "hello overlay")
	}

	info, _ := os.Stat(dst)
	if info.Mode().Perm() != 0644 {
		t.Errorf("dst perm = %v, want 0644", info.Mode().Perm())
	}
}

func TestCopyFilePreservesMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "script.sh")
	dst := filepath.Join(dir, "copy.sh")

	if err := os.WriteFile(src, []byte("#!/bin/sh"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	info, _ := os.Stat(dst)
	if info.Mode().Perm() != 0755 {
		t.Errorf("dst perm = %v, want 0755", info.Mode().Perm())
	}
}

func TestPersistDir(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Create nested structure in src.
	os.MkdirAll(filepath.Join(src, "sub"), 0755)
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("aaa"), 0644)
	os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("bbb"), 0644)

	persistDir(src, dst)

	data, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil {
		t.Fatalf("read a.txt: %v", err)
	}
	if string(data) != "aaa" {
		t.Errorf("a.txt = %q, want %q", string(data), "aaa")
	}

	data, err = os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
	if err != nil {
		t.Fatalf("read sub/b.txt: %v", err)
	}
	if string(data) != "bbb" {
		t.Errorf("sub/b.txt = %q, want %q", string(data), "bbb")
	}
}

func TestContainsPath(t *testing.T) {
	paths := []string{"/home/user/.claude", "/home/user/.cache/claude"}

	if !containsPath(paths, "/home/user/.claude") {
		t.Error("should contain .claude")
	}
	if containsPath(paths, "/home/user") {
		t.Error("should not contain /home/user")
	}
	if containsPath(nil, "/anything") {
		t.Error("nil slice should not contain anything")
	}
}

// TestOverlayPrefixArgs verifies that Exec generates --overlay-prefix flags
// for UseRegex mounts that are under HOME.
func TestOverlayPrefixArgs(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no HOME set")
	}

	s := &linuxSandbox{
		cfg: Config{
			Mounts: []Mount{
				{Source: filepath.Join(home, ".claude"), Target: filepath.Join(home, ".claude"), UseRegex: true},
				{Source: filepath.Join(home, ".claude"), Target: filepath.Join(home, ".claude")},
			},
			Deny:        []string{"/tmp/test-deny"}, // triggers wrapper path
			NetworkNeed: NetworkFull,
		},
		tmpDir: t.TempDir(),
	}

	ctx := context.Background()
	cmd, err := s.Exec(ctx, "/bin/echo", []string{"test"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--overlay-prefix .claude") {
		t.Errorf("expected --overlay-prefix .claude in args, got: %s", args)
	}
	if !strings.Contains(args, "--home "+home) {
		t.Errorf("expected --home %s in args, got: %s", home, args)
	}
}

// TestOverlayPrefixSkipsOutsideHome verifies that UseRegex mounts outside HOME
// don't generate --overlay-prefix flags.
func TestOverlayPrefixSkipsOutsideHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no HOME set")
	}

	s := &linuxSandbox{
		cfg: Config{
			Mounts: []Mount{
				{Source: "/tmp/not-under-home", Target: "/tmp/not-under-home", UseRegex: true},
			},
			Deny:        []string{"/tmp/test-deny"},
			NetworkNeed: NetworkFull,
		},
		tmpDir: t.TempDir(),
	}

	ctx := context.Background()
	cmd, err := s.Exec(ctx, "/bin/echo", []string{"test"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	args := strings.Join(cmd.Args, " ")
	if strings.Contains(args, "--overlay-prefix") {
		t.Errorf("should not have --overlay-prefix for mount outside HOME, got: %s", args)
	}
}

// TestNoOverlayPrefixWithoutUseRegex verifies that non-UseRegex mounts
// don't generate --overlay-prefix flags.
func TestNoOverlayPrefixWithoutUseRegex(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no HOME set")
	}

	s := &linuxSandbox{
		cfg: Config{
			Mounts: []Mount{
				{Source: filepath.Join(home, ".claude"), Target: filepath.Join(home, ".claude")},
			},
			Deny:        []string{"/tmp/test-deny"},
			NetworkNeed: NetworkFull,
		},
		tmpDir: t.TempDir(),
	}

	ctx := context.Background()
	cmd, err := s.Exec(ctx, "/bin/echo", []string{"test"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	args := strings.Join(cmd.Args, " ")
	if strings.Contains(args, "--overlay-prefix") {
		t.Errorf("should not have --overlay-prefix without UseRegex, got: %s", args)
	}
}
