//go:build linux

package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
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
	root := protectedFixtureRoot(t)
	target := filepath.Join(root, "state")
	// Move temporary grants out of this synthetic path so overlap tests reach
	// the specific declared mount rather than the system temporary grant.
	t.Setenv("TMPDIR", filepath.Join(root, "temporary"))
	// Prove the target is admissible before testing individual overlap rules.
	if err := validateLinuxProtectedTargets(Config{ProtectedWriteTargets: []string{target}}); err != nil {
		t.Fatalf("fixture cannot reach mount validation: %v", err)
	}
	for _, cfg := range []Config{
		{ProtectedWriteTargets: []string{"relative"}},
		{ProtectedWriteTargets: []string{"/proc/control"}},
		{ProtectedWriteTargets: []string{target}, Mounts: []Mount{{Source: root}}},
		{ProtectedWriteTargets: []string{target}, Mounts: []Mount{{Source: filepath.Join(target, "child")}}},
		{ProtectedWriteTargets: []string{target}, Mounts: []Mount{{Source: target[:len(target)-1], UseRegex: true}}},
		{ProtectedWriteTargets: []string{target}, Mounts: []Mount{{Source: "/src", Target: "/dst"}}},
	} {
		wantRule := ""
		if len(cfg.Mounts) == 1 && cfg.Mounts[0].Target == "" {
			wantRule = cfg.Mounts[0].Source
		}
		pe := requireProtectedError(t, validateLinuxProtectedTargets(cfg), wantRule)
		if len(cfg.Mounts) > 0 && strings.Contains(pe.Reason, "temporary") {
			t.Fatalf("mount conflict masked by temporary grant: %v", pe)
		}
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

func protectedFixtureRoot(t *testing.T) string {
	t.Helper()
	return testsocket.RuntimeDir(t, os.Getenv("WT_PROVIDER_GUARD_FIXTURE_ROOT"))
}

func TestLinuxProtectedFixtureLayout(t *testing.T) {
	// Exercise the integration helper even on hosts without namespaces.
	base := testsocket.RuntimeDir(t, "")
	t.Setenv("WT_PROVIDER_GUARD_FIXTURE_ROOT", base)
	root := protectedFixtureRoot(t)
	if filepath.Dir(root) != base {
		t.Fatalf("dedicated fixture root ignored: %s", root)
	}
	state, work, tmp := filepath.Join(root, "state"), filepath.Join(root, "work"), filepath.Join(root, "tmp")
	for _, dir := range []string{state, work, tmp} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", tmp)
	cfg := Config{ProtectedWriteTargets: []string{state}, Mounts: []Mount{{Source: work}}}
	if err := validateLinuxProtectedTargets(cfg); err != nil {
		t.Fatalf("integration layout rejected before protection: %v", err)
	}
	cfg.ProtectedWriteTargets = []string{filepath.Join(tmp, "state")}
	pe := requireProtectedError(t, validateLinuxProtectedTargets(cfg), tmp)
	if !strings.Contains(pe.Reason, "temporary") {
		t.Fatalf("temporary state lost its rejection: %v", pe)
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
	root := testsocket.RuntimeDir(t, "")
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
	root := testsocket.RuntimeDir(t, "")
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
