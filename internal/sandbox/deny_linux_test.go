//go:build linux

package sandbox

import (
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
