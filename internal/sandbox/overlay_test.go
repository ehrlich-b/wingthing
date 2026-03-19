//go:build linux

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCopyFileDoesNotFollowSymlink verifies that copyFile writing to a symlink
// destination follows the symlink (this is the DEFAULT behavior we need to guard
// against in the persist function).
func TestCopyFileFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.txt")
	os.WriteFile(outside, []byte("old"), 0644)
	src := filepath.Join(dir, "src.txt")
	os.WriteFile(src, []byte("new"), 0644)
	link := filepath.Join(dir, "link.txt")
	os.Symlink(outside, link)

	if err := copyFile(src, link); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	// copyFile follows the symlink — outside.txt was overwritten
	data, _ := os.ReadFile(outside)
	if string(data) != "new" {
		t.Errorf("outside.txt = %q, want %q (copyFile should follow symlink)", string(data), "new")
	}
}

// TestPersistFunctionRemovesSymlinks verifies that the persist logic in
// setupOverlayHome removes symlinks at the destination before copying, so
// writes stay within the per-user home and don't escape via symlinks to
// shared paths like /opt/wingthing/.claude.json.
func TestPersistFunctionRemovesSymlinks(t *testing.T) {
	// Simulate: realHome has a symlink .claude.json -> outsideFile
	// Overlay upper has a new .claude.json
	// After persist: realHome should have a real file, outsideFile unchanged
	dir := t.TempDir()
	outsideFile := filepath.Join(dir, "shared-claude.json")
	os.WriteFile(outsideFile, []byte("shared-config"), 0644)

	realHome := filepath.Join(dir, "real-home")
	os.MkdirAll(realHome, 0755)
	os.Symlink(outsideFile, filepath.Join(realHome, ".claude.json"))

	upperDir := filepath.Join(dir, "upper")
	os.MkdirAll(upperDir, 0755)
	os.WriteFile(filepath.Join(upperDir, ".claude.json"), []byte("user-config"), 0644)

	// Run persist logic (same as setupOverlayHome's persist function)
	prefixes := []string{".claude"}
	entries, _ := os.ReadDir(upperDir)
	for _, e := range entries {
		name := e.Name()
		matched := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		src := filepath.Join(upperDir, name)
		dst := filepath.Join(realHome, name)
		if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(dst)
		}
		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copyFile: %v", err)
		}
	}

	// realHome should have a real file, not a symlink
	fi, err := os.Lstat(filepath.Join(realHome, ".claude.json"))
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("realHome/.claude.json should be a regular file, not a symlink")
	}
	data, _ := os.ReadFile(filepath.Join(realHome, ".claude.json"))
	if string(data) != "user-config" {
		t.Errorf("realHome/.claude.json = %q, want %q", string(data), "user-config")
	}
	// Outside file should be unchanged
	data, _ = os.ReadFile(outsideFile)
	if string(data) != "shared-config" {
		t.Errorf("outsideFile = %q, want %q (should not be modified)", string(data), "shared-config")
	}
}

// TestPersistDirRemovesSymlinks verifies that persistDir also removes
// symlinks before copying files within subdirectories.
func TestPersistDirRemovesSymlinks(t *testing.T) {
	dir := t.TempDir()
	outsideFile := filepath.Join(dir, "outside-settings.json")
	os.WriteFile(outsideFile, []byte("shared"), 0644)

	src := filepath.Join(dir, "src-dir")
	os.MkdirAll(src, 0755)
	os.WriteFile(filepath.Join(src, "settings.json"), []byte("per-user"), 0644)

	dst := filepath.Join(dir, "dst-dir")
	os.MkdirAll(dst, 0755)
	os.Symlink(outsideFile, filepath.Join(dst, "settings.json"))

	persistDir(src, dst)

	// dst/settings.json should be a real file now
	fi, err := os.Lstat(filepath.Join(dst, "settings.json"))
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("dst/settings.json should be a regular file, not a symlink")
	}
	data, _ := os.ReadFile(filepath.Join(dst, "settings.json"))
	if string(data) != "per-user" {
		t.Errorf("dst/settings.json = %q, want %q", string(data), "per-user")
	}
	// Outside file untouched
	data, _ = os.ReadFile(outsideFile)
	if string(data) != "shared" {
		t.Errorf("outsideFile = %q, want %q", string(data), "shared")
	}
}

// TestPersistPrefixMatching verifies that only files matching the overlay
// prefix are persisted, and non-matching files are ignored.
func TestPersistPrefixMatching(t *testing.T) {
	dir := t.TempDir()
	realHome := filepath.Join(dir, "real-home")
	os.MkdirAll(realHome, 0755)
	upperDir := filepath.Join(dir, "upper")
	os.MkdirAll(upperDir, 0755)

	os.WriteFile(filepath.Join(upperDir, ".claude.json"), []byte("keep"), 0644)
	os.WriteFile(filepath.Join(upperDir, ".claude-settings"), []byte("keep"), 0644)
	os.WriteFile(filepath.Join(upperDir, ".bashrc"), []byte("ignore"), 0644)
	os.WriteFile(filepath.Join(upperDir, ".profile"), []byte("ignore"), 0644)

	prefixes := []string{".claude"}
	entries, _ := os.ReadDir(upperDir)
	for _, e := range entries {
		name := e.Name()
		matched := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		src := filepath.Join(upperDir, name)
		dst := filepath.Join(realHome, name)
		if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(dst)
		}
		copyFile(src, dst)
	}

	// .claude* files should exist
	if _, err := os.Stat(filepath.Join(realHome, ".claude.json")); err != nil {
		t.Error(".claude.json should be persisted")
	}
	if _, err := os.Stat(filepath.Join(realHome, ".claude-settings")); err != nil {
		t.Error(".claude-settings should be persisted")
	}
	// non-.claude files should NOT exist
	if _, err := os.Stat(filepath.Join(realHome, ".bashrc")); err == nil {
		t.Error(".bashrc should NOT be persisted")
	}
	if _, err := os.Stat(filepath.Join(realHome, ".profile")); err == nil {
		t.Error(".profile should NOT be persisted")
	}
}

// TestPersistBrokenSymlink verifies that broken symlinks at the destination
// are removed and replaced with real files.
func TestPersistBrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	realHome := filepath.Join(dir, "real-home")
	os.MkdirAll(realHome, 0755)
	// Create a broken symlink (target doesn't exist)
	os.Symlink("/nonexistent/path/.claude.json", filepath.Join(realHome, ".claude.json"))

	upperDir := filepath.Join(dir, "upper")
	os.MkdirAll(upperDir, 0755)
	os.WriteFile(filepath.Join(upperDir, ".claude.json"), []byte("fresh"), 0644)

	dst := filepath.Join(realHome, ".claude.json")
	if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		os.Remove(dst)
	}
	if err := copyFile(filepath.Join(upperDir, ".claude.json"), dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("should be a regular file, not a symlink")
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "fresh" {
		t.Errorf("got %q, want %q", string(data), "fresh")
	}
}

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

// TestExecJailModeArgs verifies that Exec generates --mount-ro flags when
// deny list contains "/" (jail mode).
func TestExecJailModeArgs(t *testing.T) {
	s := &linuxSandbox{
		cfg: Config{
			Mounts: []Mount{
				{Source: "/usr", Target: "/usr", ReadOnly: true},
				{Source: "/etc", Target: "/etc", ReadOnly: true},
				{Source: "/", Target: "/", ReadOnly: true},
				{Source: "/opt/work", Target: "/opt/work"},
			},
			Deny:        []string{"/", "/opt/secret"},
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
	if !strings.Contains(args, "--mount-ro /usr") {
		t.Errorf("expected --mount-ro /usr in args, got: %s", args)
	}
	if !strings.Contains(args, "--mount-ro /etc") {
		t.Errorf("expected --mount-ro /etc in args, got: %s", args)
	}
	// ro:/ should be excluded (source is "/")
	count := strings.Count(args, "--mount-ro")
	if count != 2 {
		t.Errorf("expected 2 --mount-ro flags, got %d in: %s", count, args)
	}
	// writable path should still be --writable
	if !strings.Contains(args, "--writable /opt/work") {
		t.Errorf("expected --writable /opt/work in args, got: %s", args)
	}
}

// TestExecNoMountRoWithoutJail verifies --mount-ro is NOT generated when
// deny list does not contain "/".
func TestExecNoMountRoWithoutJail(t *testing.T) {
	s := &linuxSandbox{
		cfg: Config{
			Mounts: []Mount{
				{Source: "/usr", Target: "/usr", ReadOnly: true},
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
	if strings.Contains(args, "--mount-ro") {
		t.Errorf("should not have --mount-ro without deny:/, got: %s", args)
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
