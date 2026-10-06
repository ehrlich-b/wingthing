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

func TestPrepareDenyMountpointsCreatesMissingAndPreservesExisting(t *testing.T) {
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

	operation, path, err := prepareDenyMountpoints([]string{missing, existing})
	if err != nil {
		t.Fatalf("prepareDenyMountpoints() operation=%q path=%q error=%v", operation, path, err)
	}
	if info, statErr := os.Stat(missing); statErr != nil || !info.IsDir() {
		t.Fatalf("missing deny mountpoint was not prepared: info=%v error=%v", info, statErr)
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil || string(data) != "host key" {
		t.Fatalf("existing deny path changed: data=%q error=%v", data, readErr)
	}
}

func TestPrepareDenyMountpointsReportsUncreatablePath(t *testing.T) {
	path := filepath.Join("/proc", "wingthing-deny-mountpoint-must-not-exist")
	operation, gotPath, err := prepareDenyMountpoints([]string{path})
	if err == nil {
		t.Fatal("prepareDenyMountpoints() accepted an uncreatable mountpoint")
	}
	if operation != "create deny mountpoint" || gotPath != path {
		t.Fatalf("failure = operation %q path %q, want create failure for %q", operation, gotPath, path)
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

func TestMissingDeniedPolicyUsesDirectoryPlaceholder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egg.yaml")
	if _, _, err := prepareDenyMountpoints([]string{path}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("policy placeholder = %v, %v", info, err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Fatal("missing policy became a loadable empty configuration")
	}
}

func TestMissingDeniedPathRejectsSymlinkParent(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareDenyMountpoints([]string{filepath.Join(root, "link", "egg.yaml")}); err == nil {
		t.Fatal("created a denied mountpoint through a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "egg.yaml")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was changed: %v", err)
	}
}
