//go:build darwin

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// protectedProfileEnv points HOME and TMPDIR at sibling directories outside
// /private/tmp, which Seatbelt always grants for writes. It returns canonical
// home and temp paths as Seatbelt sees them even when the suite uses TMPDIR=/tmp.
func protectedProfileEnv(t *testing.T) (home, tmp string) {
	t.Helper()
	root, err := os.MkdirTemp(os.Getenv("HOME"), "wt-protected-profile-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home = filepath.Join(root, "home")
	tmp = filepath.Join(root, "tmp")
	for _, dir := range []string{home, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)
	if home, err = canonicalSandboxPath(home); err != nil {
		t.Fatal(err)
	}
	if tmp, err = canonicalSandboxPath(tmp); err != nil {
		t.Fatal(err)
	}
	return home, tmp
}

func protectedProfileConfig(home string, protected ...string) Config {
	return Config{
		Mounts: []Mount{
			{Source: filepath.Join(home, "work"), Target: filepath.Join(home, "work")},
			{Source: filepath.Join(home, ".claude"), Target: filepath.Join(home, ".claude"), UseRegex: true},
			{Source: filepath.Join(home, ".wingthing", "eggs", "child", "browser-requests"), Target: filepath.Join(home, ".wingthing", "eggs", "child", "browser-requests")},
			{Source: "/usr", Target: "/usr", ReadOnly: true},
		},
		Deny:                  []string{filepath.Join(home, ".ssh")},
		DenyWrite:             []string{filepath.Join(home, "work", "egg.yaml")},
		NetworkNeed:           NetworkHTTPS,
		ProtectedWriteTargets: protected,
	}
}

func TestCheckedProfileEmptyProtectedSetIsUnchanged(t *testing.T) {
	home, _ := protectedProfileEnv(t)
	cfg := protectedProfileConfig(home)
	profile, err := buildCheckedProfile(cfg)
	if err != nil {
		t.Fatalf("empty protected set refused: %v", err)
	}
	if profile != buildProfile(cfg) {
		t.Fatal("empty protected set changed the emitted profile")
	}
	if cfg.ProtectedWriteTargets = []string{}; mustCheckedProfile(t, cfg) != profile {
		t.Fatal("zero-length protected set changed the emitted profile")
	}
}

func TestCheckedProfileControlBridgesRespectProtectedTargets(t *testing.T) {
	home, _ := protectedProfileEnv(t)
	tree := filepath.Join(home, ".wingthing", "eggs")
	bridge := filepath.Join(tree, "own", "browser-requests")
	cfg := Config{
		Mounts:                []Mount{{Source: filepath.Join(home, "work")}},
		ControlDenyPaths:      []string{tree},
		ControlBridges:        []Mount{{Source: bridge}},
		ProtectedWriteTargets: []string{filepath.Join(tree, "parent")},
	}
	if _, err := buildCheckedProfile(cfg); err != nil {
		t.Fatalf("non-overlapping control bridge refused: %v", err)
	}
	cfg.ProtectedWriteTargets = []string{bridge}
	_, err := buildCheckedProfile(cfg)
	requireProtectedError(t, err, "(allow file-write* (literal \""+bridge+"\"))")
}

func mustCheckedProfile(t *testing.T, cfg Config) string {
	t.Helper()
	profile, err := buildCheckedProfile(cfg)
	if err != nil {
		t.Fatalf("buildCheckedProfile: %v", err)
	}
	return profile
}

func TestCheckedProfileProtectsStateWithoutDenyingProviderOrWorkspace(t *testing.T) {
	home, _ := protectedProfileEnv(t)
	controller := filepath.Join(home, ".wingthing", "eggs", "parent")
	stateDB := filepath.Join(home, ".wingthing", "wt.db") // missing target
	if err := os.MkdirAll(controller, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := protectedProfileConfig(home, controller, stateDB)
	profile := mustCheckedProfile(t, cfg)

	base := buildProfile(protectedProfileConfig(home))
	if !strings.HasPrefix(profile, base) {
		t.Fatal("protected targets must only append rules to the ordinary profile")
	}
	wantTail := "" +
		"(deny file-write* (literal \"" + controller + "\"))\n" +
		"(deny file-write* (subpath \"" + controller + "\"))\n" +
		"(deny file-write* (literal \"" + stateDB + "\"))\n" +
		"(deny file-write* (subpath \"" + stateDB + "\"))\n"
	if got := strings.TrimPrefix(profile, base); got != wantTail {
		t.Fatalf("protected tail =\n%s\nwant\n%s", got, wantTail)
	}
	for _, kept := range []string{
		"(allow file-write* (subpath \"" + filepath.Join(home, "work") + "\"))",
		"(allow file-write* (regex #\"^" + sbplRegexEscape(filepath.Join(home, ".claude")) + "\"))",
		"(allow file-write* (subpath \"" + filepath.Join(home, ".wingthing", "eggs", "child", "browser-requests") + "\"))",
	} {
		if !strings.Contains(profile, kept) {
			t.Errorf("non-overlapping writable rule lost: %s", kept)
		}
	}
}

func TestCheckedProfileRefusesOverlappingWritableRules(t *testing.T) {
	home, tmp := protectedProfileEnv(t)
	alias := filepath.Join(home, "alias")
	if err := os.MkdirAll(filepath.Join(home, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "work"), alias); err != nil {
		t.Fatal(err)
	}
	workRule := "(allow file-write* (subpath \"" + filepath.Join(home, "work") + "\"))"
	for name, tc := range map[string]struct {
		target string
		rule   string
	}{
		"inside workspace":       {filepath.Join(home, "work", ".controller"), workRule},
		"workspace ancestor":     {home, ""},
		"symlink alias resolved": {filepath.Join(alias, "state"), workRule},
		"regex adjacent file":    {filepath.Join(home, ".claude-controller.json"), "(allow file-write* (regex #\"^" + sbplRegexEscape(filepath.Join(home, ".claude")) + "\"))"},
		"case variant of regex":  {filepath.Join(home, ".CLAUDE", "state"), "(allow file-write* (regex #\"^" + sbplRegexEscape(filepath.Join(home, ".claude")) + "\"))"},
		"implicit keychain":      {filepath.Join(home, "Library", "Keychains", "state"), "(allow file-write* (subpath \"" + filepath.Join(home, "Library", "Keychains") + "\"))"},
		"implicit TMPDIR":        {filepath.Join(tmp, "controller"), "(allow file-write* (subpath \"" + tmp + "\"))"},
		"implicit private tmp":   {"/private/tmp/wt-protected-controller", "(allow file-write* (subpath \"/private/tmp\"))"},
		"tmp symlink alias":      {"/tmp/wt-protected-controller", "(allow file-write* (subpath \"/private/tmp\"))"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildCheckedProfile(protectedProfileConfig(home, filepath.Join(home, ".wingthing", "ok"), tc.target))
			pe := requireProtectedError(t, err, tc.rule)
			if !strings.Contains(pe.Reason, "overlaps") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCheckedProfileWithoutWritableMountsSealsTargetsOverAllowDefault(t *testing.T) {
	home, _ := protectedProfileEnv(t)
	target := filepath.Join(home, ".wingthing", "parent")
	profile := mustCheckedProfile(t, Config{NetworkNeed: NetworkNone, ProtectedWriteTargets: []string{target}})
	if !strings.HasSuffix(profile, "(deny file-write* (subpath \""+target+"\"))\n") {
		t.Fatalf("protected deny not emitted last:\n%s", profile)
	}
}

func TestCheckedProfileRejectsRelativeTarget(t *testing.T) {
	home, _ := protectedProfileEnv(t)
	_, err := buildCheckedProfile(protectedProfileConfig(home, "relative/state"))
	requireProtectedError(t, err, "")
}

func TestCheckedProfileRejectsSymlinkDotDotProtectedTarget(t *testing.T) {
	home, _ := protectedProfileEnv(t)
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(filepath.Join(work, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "link")
	if err := os.Symlink(filepath.Join(work, "sub"), link); err != nil {
		t.Fatal(err)
	}
	// Preserve ..: Join would erase the difference between the kernel's path
	// (work/controller) and the lexical path (home/controller).
	target := link + "/../controller"
	_, err := buildCheckedProfile(protectedProfileConfig(home, target))
	requireProtectedError(t, err, "")
}

func TestNewReturnsProtectedWriteTargetErrorUnwrapped(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not on PATH")
	}
	home, _ := protectedProfileEnv(t)
	_, err := New(protectedProfileConfig(home, filepath.Join(home, "work", "state")))
	requireProtectedError(t, err, "(allow file-write* (subpath \""+filepath.Join(home, "work")+"\"))")

	sb, err := New(protectedProfileConfig(home, filepath.Join(home, ".wingthing", "parent")))
	if err != nil {
		t.Fatalf("non-overlapping protected set refused: %v", err)
	}
	destroySandboxForTest(t, sb)
}
