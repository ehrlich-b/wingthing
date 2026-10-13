//go:build linux

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxProtectedRequiresLandlockABI3(t *testing.T) {
	old := providerLandlockABI
	t.Cleanup(func() { providerLandlockABI = old })
	for _, abi := range []int{0, 1, 2} {
		providerLandlockABI = func() (int, error) { return abi, nil }
		_, err := newPlatform(Config{ProtectedWriteTargets: []string{"/home/u/.wingthing"}})
		requireProtectedError(t, err, "")
		_, err = New(Config{ProtectedWriteTargets: []string{"/home/u/.wingthing"}})
		requireProtectedError(t, err, "")
	}
	providerLandlockABI = func() (int, error) { return 0, unix.ENOSYS }
	requireProtectedError(t, CheckProviderWriteProtection(), "")
	providerLandlockABI = func() (int, error) { return 0, unix.EOPNOTSUPP }
	requireProtectedError(t, CheckProviderWriteProtection(), "")
	for _, abi := range []int{3, 4, 6, 99} {
		if err := requireLandlockABI(abi, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLinuxProtectedDeclarationConflicts(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "state")
	// Move temporary grants out of this synthetic path so overlap tests reach
	// the specific declared mount rather than the system temporary grant.
	t.Setenv("TMPDIR", filepath.Join(root, "temporary"))
	for _, cfg := range []Config{
		{ProtectedWriteTargets: []string{"relative"}},
		{ProtectedWriteTargets: []string{"/proc/control"}},
		{ProtectedWriteTargets: []string{target}, Mounts: []Mount{{Source: root}}},
		{ProtectedWriteTargets: []string{target}, Mounts: []Mount{{Source: filepath.Join(target, "child")}}},
		{ProtectedWriteTargets: []string{target}, Mounts: []Mount{{Source: target[:len(target)-1], UseRegex: true}}},
		{ProtectedWriteTargets: []string{target}, Mounts: []Mount{{Source: "/src", Target: "/dst"}}},
	} {
		requireProtectedError(t, validateLinuxProtectedTargets(cfg), "")
	}
	if err := validateLinuxProtectedTargets(Config{}); err != nil {
		t.Fatal(err)
	}
	if err := validateLinuxProtectedTargets(Config{ProtectedWriteTargets: []string{"/opt/host-state"}, Mounts: []Mount{{Source: "/opt/work"}}}); err != nil {
		t.Fatal(err)
	}
	var ee *EnforcementError
	if errors.As(requireLandlockABI(0, unix.ENOSYS), &ee) {
		t.Fatal("missing Landlock lost its explicit explanation")
	}
}

func TestLinuxProtectedMountAliasIdentities(t *testing.T) {
	entries := map[string]mountInfoEntry{
		"/":                {Device: "8:1", Root: "/", Options: map[string]bool{"rw": true}},
		"/workspace/alias": {Device: "8:1", Root: "/authority/state", Options: map[string]bool{"rw": true}},
	}
	requireProtectedError(t, checkProtectedMountAliases(entries, []string{"/authority/state"}), "")
	if err := checkProtectedMountAliases(entries, []string{"/authority/other"}); err != nil {
		t.Fatal(err)
	}
	entries["/workspace/alias"] = mountInfoEntry{Device: "8:2", Root: "/", Options: map[string]bool{"rw": true}}
	if err := checkProtectedMountAliases(entries, []string{"/authority/state"}); err != nil {
		t.Fatal(err)
	}
	entries["/authority/state/nested"] = mountInfoEntry{Device: "8:2", Root: "/", Options: map[string]bool{"rw": true}}
	requireProtectedError(t, checkProtectedMountAliases(entries, []string{"/authority/state"}), "")
}

func TestLinuxProtectedMinimalDirectoryMasks(t *testing.T) {
	root := t.TempDir()
	ssh := filepath.Join(root, ".ssh")
	if err := os.Mkdir(ssh, 0700); err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(ssh, "known_hosts")
	if err := os.WriteFile(known, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := []string{ssh, known, ssh}
	got := minimalDenyPaths(paths)
	if len(got) != 1 || got[0] != ssh {
		t.Fatalf("redundant masks retained: %v", got)
	}
	if !containsPath(paths, known) {
		t.Fatal("known_hosts explicit denial lost")
	}
}

func TestLinuxProtectedInternalSymlinkMetadata(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "record")
	if err := os.WriteFile(file, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("record", filepath.Join(root, "internal-link")); err != nil {
		t.Fatal(err)
	}
	if err := checkProtectedHardlinks(root); err != nil {
		t.Fatalf("contained symlink refused: %v", err)
	}
}
