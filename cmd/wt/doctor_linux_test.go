//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAppArmorExecutablePathAcceptsSystemBinary(t *testing.T) {
	path, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAppArmorExecutablePath(path); err != nil {
		t.Fatalf("root-owned system binary rejected: %v", err)
	}
}

func TestValidateAppArmorExecutablePathRejectsTemporaryTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wt")
	if err := os.WriteFile(path, []byte("test"), 0755); err != nil {
		t.Fatal(err)
	}
	err := validateAppArmorExecutablePath(path)
	if err == nil {
		t.Fatal("temporary executable path accepted")
	}
	if !strings.Contains(err.Error(), "root-owned, root-writable-only") ||
		!strings.Contains(err.Error(), "/usr/local/bin/wt") {
		t.Fatalf("unsafe path error = %v", err)
	}
}

func TestValidateAppArmorExecutablePathRejectsRelativePath(t *testing.T) {
	err := validateAppArmorExecutablePath("./wt")
	if err == nil || !strings.Contains(err.Error(), "absolute executable path") {
		t.Fatalf("relative path error = %v", err)
	}
}

func TestAppArmorProfileExecutable(t *testing.T) {
	profile := []byte("abi <abi/4.0>,\nprofile wingthing /usr/local/bin/wt flags=(unconfined) {\n  userns,\n}\n")
	if got := appArmorProfileExecutable(profile); got != "/usr/local/bin/wt" {
		t.Fatalf("profile executable = %q", got)
	}
}

func TestOverlayFSAvailableFromRegisteredFilesystem(t *testing.T) {
	filesystems := []byte("nodev\tsysfs\nnodev\toverlay\n")
	if !overlayFSAvailableFrom(filesystems, "", nil) {
		t.Fatal("registered overlay filesystem was not detected")
	}
}

func TestOverlayFSAvailableFromLoadableModule(t *testing.T) {
	root := t.TempDir()
	release := "6.8.0-fixture"
	moduleDir := filepath.Join(root, release, "kernel", "fs", "overlayfs")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "overlay.ko.zst"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !overlayFSAvailableFrom(nil, release, []string{root}) {
		t.Fatal("loadable overlay module was not detected before registration")
	}
}

func TestOverlayFSAvailableFromMissingCapability(t *testing.T) {
	if overlayFSAvailableFrom([]byte("nodev\ttmpfs\n"), "6.8.0-missing", []string{t.TempDir()}) {
		t.Fatal("missing overlay capability was reported as available")
	}
}
