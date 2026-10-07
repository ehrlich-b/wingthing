//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReadMountInfoParsesEffectiveMounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mountinfo")
	data := strings.Join([]string{
		"31 20 0:25 / / rw,relatime - ext4 /dev/root rw",
		"32 31 0:44 / /home/agent/.aws ro,nosuid,nodev - tmpfs tmpfs ro",
		"33 31 0:45 / /home/agent/a\\040space rw,nosuid - tmpfs tmpfs rw",
		// A later entry at the same mountpoint is the visible top layer.
		"34 31 0:46 / /home/agent/.aws ro,nosuid,nodev - tmpfs tmpfs ro",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}

	entries, err := readMountInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := entries["/home/agent/.aws"]; got.FSType != "tmpfs" || !got.Options["ro"] {
		t.Fatalf(".aws entry = %#v", got)
	}
	if _, ok := entries["/home/agent/a space"]; !ok {
		t.Fatalf("escaped mountpoint missing: %#v", entries)
	}
}

func TestVerifyMountEntriesRejectsMissingOrWritableMask(t *testing.T) {
	entries := map[string]mountInfoEntry{
		"/home/agent/.aws": {FSType: "tmpfs", Options: map[string]bool{"rw": true}},
	}

	err := verifyMountEntries(entries, []expectedMount{{
		Path: "/home/agent/.aws", FSType: "tmpfs", ReadOnly: true,
	}})
	if err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("writable mask error = %v", err)
	}

	err = verifyMountEntries(entries, []expectedMount{{
		Path: "/home/agent/.ssh", FSType: "tmpfs", ReadOnly: true,
	}})
	if err == nil || !strings.Contains(err.Error(), "required mount missing") {
		t.Fatalf("missing mask error = %v", err)
	}
}

func TestVerifyMountEntriesAcceptsReadonlyMaskAndWritableHole(t *testing.T) {
	entries := map[string]mountInfoEntry{
		"/home/agent":         {FSType: "ext4", Options: map[string]bool{"ro": true}},
		"/home/agent/project": {FSType: "ext4", Options: map[string]bool{"rw": true}},
		"/home/agent/.aws":    {FSType: "tmpfs", Options: map[string]bool{"ro": true}},
	}
	err := verifyMountEntries(entries, []expectedMount{
		{Path: "/home/agent", ReadOnly: true},
		{Path: "/home/agent/project", Writable: true},
		{Path: "/home/agent/.aws", FSType: "tmpfs", ReadOnly: true},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMountFlagsFromOptionsPreservesRemountState(t *testing.T) {
	options := map[string]bool{
		"rw":          true,
		"nosuid":      true,
		"nodev":       true,
		"noexec":      true,
		"relatime":    true,
		"nosymfollow": true,
		"unknown":     true,
	}
	want := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC | unix.MS_RELATIME | unix.MS_NOSYMFOLLOW)
	if got := mountFlagsFromOptions(options); got != want {
		t.Fatalf("mount flags = %#x, want %#x", got, want)
	}
}

func TestReadonlyRootPlanSealsInheritedWritableSubmounts(t *testing.T) {
	entries := map[string]mountInfoEntry{
		"/":                {Options: map[string]bool{"rw": true, "relatime": true}},
		"/mnt/data":        {Options: map[string]bool{"rw": true, "nosuid": true, "nodev": true}},
		"/mnt/data/nested": {Options: map[string]bool{"rw": true, "noexec": true}},
		"/workspace":       {Options: map[string]bool{"rw": true}},
	}
	plan, err := readonlyBindMountPlan(entries, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != len(entries) || plan[len(plan)-1].Path != "/" {
		t.Fatalf("ro:/ did not seal every inherited mount before its parent: %+v", plan)
	}
	for _, step := range plan {
		want := uintptr(unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY) | mountFlagsFromOptions(entries[step.Path].Options)
		if step.Flags != want {
			t.Fatalf("mount %s flags = %#x, want %#x", step.Path, step.Flags, want)
		}
		entries[step.Path] = mountInfoEntry{Options: map[string]bool{"ro": true}}
	}
	// Only the explicit workspace grant is reopened after the recursive seal.
	entries["/workspace"] = mountInfoEntry{Options: map[string]bool{"rw": true}}
	expected := []expectedMount{{Path: "/", ReadOnly: true, RecursiveReadOnly: true}, {Path: "/workspace", Writable: true}}
	if err := verifyMountEntries(entries, expected); err != nil {
		t.Fatal(err)
	}
	entries["/mnt/data"] = mountInfoEntry{Options: map[string]bool{"rw": true}}
	if err := verifyMountEntries(entries, expected); err == nil {
		t.Fatal("ro:/ verification accepted an undeclared writable inherited mount")
	}
	// A grant for another subtree must not reopen /mnt/data.
	if _, err := readonlyBindMountPlan(entries, "/missing"); err == nil {
		t.Fatal("accepted a missing bind root")
	}
}

func TestDenyMountpointPlanningLeavesHostPathsAbsent(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, ".aws")
	existing := filepath.Join(root, ".ssh")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(existing, "known_hosts")
	if err := os.WriteFile(marker, []byte("host key"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := planDenyMountpoints([]string{missing, existing})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Parent != root || len(plan[0].Paths) != 1 || plan[0].Paths[0] != missing {
		t.Fatalf("private placeholder plan = %+v", plan)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("planning created a host deny path: %v", err)
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil || string(data) != "host key" {
		t.Fatalf("existing deny path changed: data=%q error=%v", data, readErr)
	}
}

func TestDenyMountpointPlanningRejectsNonDirectoryParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := planDenyMountpoints([]string{filepath.Join(path, "egg.yaml")}); err == nil {
		t.Fatal("accepted a non-directory deny parent")
	}
}

func TestPrepareJailWritablePathsPreservesFilesAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "existing")
	if err := os.WriteFile(file, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareJailWritablePaths([]string{file}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "unchanged" {
		t.Fatalf("existing writable file changed: %q, %v", data, err)
	}
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Join(link, "new")} {
		if err := prepareJailWritablePaths([]string{path}); err == nil {
			t.Fatalf("accepted symlink writable path %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatalf("created directory outside the declared path: %v", err)
	}
}

func TestJailMountpointPreservesExistingFile(t *testing.T) {
	root := t.TempDir()
	path := "/workspace/config"
	if err := os.MkdirAll(filepath.Join(root, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, path)
	if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := createConfinedMountpoint(root, path, false)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep me" {
		t.Fatalf("existing mountpoint was truncated: %q, %v", data, err)
	}
}

func TestJailMountpointRejectsSymlinks(t *testing.T) {
	for _, directory := range []bool{false, true} {
		for _, parentLink := range []bool{false, true} {
			t.Run(fmt.Sprintf("directory=%t/parentLink=%t", directory, parentLink), func(t *testing.T) {
				root, outside := t.TempDir(), t.TempDir()
				victim := filepath.Join(outside, "config")
				if err := os.WriteFile(victim, []byte("host config"), 0o600); err != nil {
					t.Fatal(err)
				}
				path, linkTarget := "/link/config", outside
				if !parentLink {
					path = "/link"
					linkTarget = victim
				}
				if err := os.Symlink(linkTarget, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
				file, err := createConfinedMountpoint(root, path, directory)
				if err == nil {
					file.Close()
					t.Fatal("accepted a symlink mountpoint")
				}
				data, err := os.ReadFile(victim)
				if err != nil || string(data) != "host config" {
					t.Fatalf("symlink target changed: %q, %v", data, err)
				}
			})
		}
	}
}

func TestJailMountpointRejectsSourceSymlinks(t *testing.T) {
	root, host := t.TempDir(), t.TempDir()
	link := filepath.Join(host, "link")
	if err := os.Symlink(host, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Join(link, "config")} {
		source, target, err := jailMkTarget(root, path)
		if err == nil {
			source.Close()
			target.Close()
			t.Fatalf("accepted symlink source %q", path)
		}
	}
}

func TestJailMountpointRejectsTraversal(t *testing.T) {
	for _, path := range []string{"relative", "/../escape", "/workspace/../../escape"} {
		file, err := createConfinedMountpoint(t.TempDir(), path, false)
		if err == nil {
			file.Close()
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
}

func TestJailMountpointRejectsSymlinkInRootPath(t *testing.T) {
	parent, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "root"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "link", "root")
	file, err := createConfinedMountpoint(root, "/config", false)
	if err == nil {
		file.Close()
		t.Fatal("accepted a symlink in the confinement root")
	}
	if _, err := os.Stat(filepath.Join(outside, "root", "config")); !os.IsNotExist(err) {
		t.Fatalf("created a file outside the confinement root: %v", err)
	}
}

func TestWritablePrefixFilesRejectsSymlinksAndUndeclaredExpansion(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{".cache", ".claude"} {
		if err := os.Mkdir(filepath.Join(home, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".zshrc", ".cache-sibling", ".claude.json"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".cache-host", ".claude-host"} {
		if err := os.Symlink(filepath.Join(home, ".zshrc"), filepath.Join(home, name)); err != nil {
			t.Fatal(err)
		}
	}
	writable := []string{filepath.Join(home, ".cache"), filepath.Join(home, ".claude")}
	files, err := writablePrefixFiles(home, writable, []string{".claude"})
	if err != nil || len(files) != 1 || files[0] != filepath.Join(home, ".claude.json") {
		t.Fatalf("prefix expansion = %v, %v", files, err)
	}
	files, err = writablePrefixFiles(home, writable, nil)
	if err != nil || len(files) != 0 {
		t.Fatalf("ordinary writable mounts expanded: %v, %v", files, err)
	}
	if _, err := writablePrefixFiles(home, writable, []string{".zshrc"}); err == nil {
		t.Fatal("accepted undeclared prefix")
	}
}

func TestMissingDeniedPolicyPlansPrivatePlaceholder(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "new", "egg.yaml")
	plan, err := planDenyMountpoints([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Parent != root || plan[0].Paths[0] != path {
		t.Fatalf("private placeholder plan = %+v", plan)
	}
	if _, err := os.Lstat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatalf("planning created an ancestor on the host: %v", err)
	}
}

func TestMissingDeniedPathRejectsSymlinkParent(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := planDenyMountpoints([]string{filepath.Join(root, "link", "egg.yaml")}); err == nil {
		t.Fatal("created a denied mountpoint through a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "egg.yaml")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was changed: %v", err)
	}
}
