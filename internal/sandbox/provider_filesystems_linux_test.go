//go:build linux

package sandbox

import (
	"fmt"
	"strings"
	"testing"
)

func TestLinuxProtectedFilesystemContract(t *testing.T) {
	for _, kind := range []string{"ext4", "xfs", "btrfs", "zfs", "tmpfs", "overlay", "fuse", "fuse.sshfs", "nfs", "nfs4", "cifs", "ramfs", "futurefs", ""} {
		t.Run(kind, func(t *testing.T) {
			entries := map[string]mountInfoEntry{"/": {Device: "8:1", Root: "/", FSType: kind}}
			err := checkLinuxProviderFilesystemTree(entries, "/tree")
			allowed := kind == "ext4" || kind == "xfs" || kind == "btrfs" || kind == "zfs" || kind == "tmpfs"
			if allowed {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireProtectedError(t, err, "")
			if !strings.Contains(err.Error(), "use plain ext4") || kind != "" && !strings.Contains(err.Error(), kind) {
				t.Fatalf("missing actionable filesystem diagnostic: %v", err)
			}
		})
	}
}

func TestLinuxProtectedFilesystemNestedMounts(t *testing.T) {
	entries := map[string]mountInfoEntry{
		"/":            {Device: "8:1", Root: "/", FSType: "ext4"},
		"/tree/merged": {Device: "0:41", Root: "/", FSType: "overlay"},
	}
	for _, path := range []string{"/tree", "/tree/merged/child"} {
		requireProtectedError(t, checkLinuxProviderFilesystemTree(entries, path), "")
	}
	if err := checkLinuxProviderFilesystemTree(entries, "/unrelated"); err != nil {
		t.Fatalf("unrelated unsupported mount refused: %v", err)
	}
	entries["/tree/merged"] = mountInfoEntry{Device: "8:2", Root: "/", FSType: "xfs"}
	if err := checkLinuxProviderFilesystemTree(entries, "/tree"); err != nil {
		t.Fatalf("disjoint local submount refused: %v", err)
	}
	entries["/tree/merged"] = mountInfoEntry{Device: "8:2", Root: "/elsewhere", FSType: "xfs"}
	if err := checkLinuxProviderFilesystemTree(entries, "/tree"); err == nil || !strings.Contains(err.Error(), "bind mount") {
		t.Fatalf("preexisting subtree bind admitted: %v", err)
	}
	entries["/tree/merged"] = mountInfoEntry{Device: "8:2", Root: "/", FSType: "xfs"}
	entries["/other-view"] = entries["/tree/merged"]
	if err := checkLinuxProviderFilesystemTree(entries, "/tree"); err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("whole-filesystem bind alias admitted: %v", err)
	}
	requireProtectedError(t, checkLinuxProviderFilesystemTree(map[string]mountInfoEntry{}, "/tree"), "")
	entries = map[string]mountInfoEntry{"/": {FSType: "ext4", Root: "/"}}
	requireProtectedError(t, checkLinuxProviderFilesystemTree(entries, "/tree"), "")
}

func TestLinuxProtectedFilesystemBtrfsIdentities(t *testing.T) {
	entries := map[string]mountInfoEntry{
		"/a": {Device: "0:41", Root: "/subvol", FSType: "btrfs"},
		"/b": {Device: "0:42", Root: "/subvol", FSType: "btrfs"},
	}
	if err := normalizeProviderFilesystemIdentities(entries, func(string) (string, error) { return "btrfs:uuid", nil }); err != nil {
		t.Fatal(err)
	}
	requireProtectedError(t, checkLinuxProviderFilesystemTree(entries, "/a/tree"), "")
	entries["/b"] = mountInfoEntry{Device: "btrfs:uuid", Root: "/other-subvol", FSType: "btrfs"}
	if err := checkLinuxProviderFilesystemTree(entries, "/a/tree"); err != nil {
		t.Fatalf("disjoint subvolumes refused: %v", err)
	}
	entries["/b"] = mountInfoEntry{Device: "btrfs:uuid", Root: "/", FSType: "btrfs"}
	requireProtectedError(t, checkLinuxProviderFilesystemTree(entries, "/a/tree"), "")
	if err := normalizeProviderFilesystemIdentities(entries, func(string) (string, error) { return "", fmt.Errorf("unavailable") }); err == nil {
		t.Fatal("missing Btrfs identity admitted")
	}
}
