package config

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestStableLoadRequiresHomeUnlessStateIsExplicit(t *testing.T) {
	old := ReleaseChannel
	ReleaseChannel = "stable"
	t.Cleanup(func() { ReleaseChannel = old })
	t.Chdir(t.TempDir())
	for _, name := range []string{"HOME", "WINGTHING_DIR", "WINGTHING_PREVIEW_DIR"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("this platform resolves a home without HOME")
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "HOME") {
		t.Fatalf("missing home was not propagated: %v", err)
	}
	if _, err := os.Stat(".wingthing"); !os.IsNotExist(err) {
		t.Fatalf("Load created state in the working directory: %v", err)
	}
	t.Setenv("WINGTHING_DIR", filepath.Join(t.TempDir(), "explicit-state"))
	if _, err := Load(); err != nil {
		t.Fatalf("explicit state must not require HOME: %v", err)
	}
}

func TestPreviewStateRejectsCaseAliasedMissingChild(t *testing.T) {
	home := previewTest(t)
	stable := filepath.Join(home, ".wingthing")
	if err := os.Mkdir(stable, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, ".WINGTHING")
	stableInfo, err := os.Stat(stable)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(alias)
	if os.IsNotExist(err) || (err == nil && !os.SameFile(stableInfo, aliasInfo)) {
		t.Skip("filesystem is case-sensitive")
	}
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(alias, "missing", "preview")
	t.Setenv("WINGTHING_DIR", child)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("preview adopted a case alias of stable state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stable, "missing")); !os.IsNotExist(err) {
		t.Fatalf("preview created a child inside stable state: %v", err)
	}
}

func TestPreviewStateRejectsPhysicalAliasedMissingChild(t *testing.T) {
	home := previewTest(t)
	stable := canonicalConfiguredPath(filepath.Join(home, ".wingthing"), "")
	alias := canonicalConfiguredPath(filepath.Join(t.TempDir(), "alias"), "")
	for _, path := range []string{stable, alias} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Model a physical alias such as a firmlink, which EvalSymlinks cannot see.
	stat := statePathStat
	statePathStat = func(path string) (os.FileInfo, error) {
		if path == alias {
			path = stable
		}
		return stat(path)
	}
	t.Cleanup(func() { statePathStat = stat })
	t.Setenv("WINGTHING_DIR", filepath.Join(alias, "missing", "preview"))
	if _, err := StateDir(); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("preview accepted a missing child of a physical stable alias: %v", err)
	}
}

func previewTest(t *testing.T) string {
	t.Helper()
	old := ReleaseChannel
	ReleaseChannel = "preview"
	t.Cleanup(func() { ReleaseChannel = old })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", "")
	t.Setenv("WINGTHING_PREVIEW_DIR", "")
	return home
}

func TestPreviewProviderHomeReentryOnlyReopensMarkedParent(t *testing.T) {
	hostHome := previewTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	providerHome := filepath.Join(cfg.Dir, "provider-home")
	if err := os.MkdirAll(providerHome, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", providerHome)
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	again, err := Load()
	if err != nil || again.Dir != cfg.Dir || again.WingID != cfg.WingID {
		t.Fatalf("parent re-entry failed: %#v %v", again, err)
	}
	if _, err := os.Stat(filepath.Join(providerHome, ".wingthing")); !os.IsNotExist(err) {
		t.Fatal("re-entry created provider-local stable state")
	}
	// Canonical aliases of this exact marked parent retain the same identity.
	alias := filepath.Join(hostHome, "preview-parent-alias")
	if err := os.Symlink(cfg.Dir, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WINGTHING_DIR", alias)
	if _, err := Load(); err != nil {
		t.Fatal("canonical parent alias rejected", err)
	}
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	for _, unknownHome := range []string{cfg.Dir, filepath.Join(cfg.Dir, "other-home")} {
		t.Setenv("HOME", unknownHome)
		if _, err := StateDir(); err == nil {
			t.Fatal("re-entry accepted an unrelated HOME", unknownHome)
		}
	}
	t.Setenv("HOME", providerHome)
	childStable := filepath.Join(providerHome, ".wingthing")
	if err := os.MkdirAll(childStable, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childStable, channelMarker), []byte("preview\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stableAlias := filepath.Join(hostHome, "provider-stable-alias")
	if err := os.Symlink(childStable, stableAlias); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{childStable, stableAlias, providerHome} {
		t.Setenv("WINGTHING_DIR", dir)
		if _, err := StateDir(); err == nil {
			t.Fatal("re-entry adopted provider-local stable state or ancestor", dir)
		}
	}
}

func TestPreviewProviderHomeReentryRejectsMarkersAndEscapingArtifacts(t *testing.T) {
	previewTest(t)
	for _, mode := range []string{"unmarked", "stable-marker", "linked-marker", "dangling-marker", "foreign-artifact", "provider-home-link"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			providerHome := filepath.Join(root, "provider-home")
			if err := os.MkdirAll(providerHome, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(root, channelMarker)
			if mode != "unmarked" && mode != "linked-marker" && mode != "dangling-marker" {
				value := "preview\n"
				if mode == "stable-marker" {
					value = "stable\n"
				}
				if err := os.WriteFile(marker, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "linked-marker":
				backing := filepath.Join(root, "marker-backing")
				if err := os.WriteFile(backing, []byte("preview\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(backing, marker); err != nil {
					t.Fatal(err)
				}
			case "dangling-marker":
				if err := os.Symlink("missing", marker); err != nil {
					t.Fatal(err)
				}
			case "foreign-artifact":
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "eggs")); err != nil {
					t.Fatal(err)
				}
			case "provider-home-link":
				if err := os.Remove(providerHome); err != nil {
					t.Fatal(err)
				}
				other := filepath.Join(root, "other-home")
				if err := os.Mkdir(other, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, providerHome); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", providerHome)
			t.Setenv("WINGTHING_DIR", root)
			if _, err := StateDir(); err == nil {
				t.Fatal("unsafe parent re-entry accepted", mode)
			}
			if _, err := os.Stat(filepath.Join(providerHome, ".wingthing")); !os.IsNotExist(err) {
				t.Fatal("refusal created provider-local stable state")
			}
		})
	}
}

func TestPreviewProviderHomeReentryStillRefusesOSAccountStableRoot(t *testing.T) {
	previewTest(t)
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	stable := canonicalConfiguredPath(filepath.Join(account.HomeDir, ".wingthing"), "")
	for _, root := range []string{stable, canonicalConfiguredPath(account.HomeDir, "")} {
		allowed, err := validatePreviewProviderReentry(root, filepath.Join(root, "provider-home"))
		if err == nil || allowed {
			t.Fatal("parent re-entry accepted actual account stable root or ancestor")
		}
	}
}

func TestPreviewStateNeverAdoptsStableOrImportedDirectory(t *testing.T) {
	home := previewTest(t)
	stable := filepath.Join(home, ".wingthing")
	if err := os.MkdirAll(stable, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(stable, "config.yaml")
	if err := os.WriteFile(sentinel, []byte("secret-stable-marker"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{stable, home, filepath.Join(stable, "preview-child")} {
		t.Setenv("WINGTHING_DIR", path)
		if _, err := Load(); err == nil {
			t.Fatalf("adopted stable overlap %s", path)
		}
	}
	alias := filepath.Join(home, "stable-alias")
	if err := os.Symlink(stable, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WINGTHING_DIR", alias)
	if _, err := Load(); err == nil {
		t.Fatal("adopted stable symlink")
	}
	imported := t.TempDir()
	if err := os.WriteFile(filepath.Join(imported, "token.json"), []byte("imported"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WINGTHING_DIR", imported)
	if _, err := Load(); err == nil {
		t.Fatal("adopted imported state")
	}
	info, _ := os.Stat(stable)
	data, _ := os.ReadFile(sentinel)
	if info.Mode().Perm() != 0755 || string(data) != "secret-stable-marker" {
		t.Fatal("stable modified before rejection")
	}
}

func TestPreviewStateRestartAndStableReverseAccess(t *testing.T) {
	home := previewTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dir != filepath.Join(home, ".wingthing-preview") {
		t.Fatal(cfg.Dir)
	}
	if BinaryName() != "wt-preview" {
		t.Fatal(BinaryName())
	}
	again, err := Load()
	if err != nil || again.WingID != cfg.WingID {
		t.Fatalf("restart identity: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".wingthing")); !os.IsNotExist(err) {
		t.Fatal("stable initialized")
	}
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	ReleaseChannel = "stable"
	if _, err := Load(); err == nil {
		t.Fatal("stable adopted preview")
	}
}

func TestPreviewRejectsAliasedEggsAndOrganizationConfig(t *testing.T) {
	home := previewTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	stableEggs := filepath.Join(home, ".wingthing", "eggs")
	if err := os.MkdirAll(stableEggs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stableEggs, filepath.Join(cfg.Dir, "eggs")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("preview attached alias eggs")
	}
	if err := os.Remove(filepath.Join(cfg.Dir, "eggs")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Dir, "wing.yaml"), []byte("org: slide\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWingConfig(cfg.Dir); err == nil {
		t.Fatal("preview accepted org")
	}
	for _, address := range []string{"https://ws.wingthing.ai", "https://slide.test", "http://localhost@slide.test", "localhost:8180"} {
		if err := ValidatePreviewRelay(address); err == nil {
			t.Fatal("accepted coordinator", address)
		}
	}
	if err := ValidatePreviewRelay("http://localhost:8180"); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewProviderLinksStayInsideExactStateTree(t *testing.T) {
	previewTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	debug := filepath.Join(cfg.Dir, "provider-home", ".claude", "debug")
	if err := os.MkdirAll(debug, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(debug, "fixture.log")
	if err := os.WriteFile(target, []byte("nonsecret-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(debug, "latest")
	for _, destination := range []string{"fixture.log", target} {
		if err := os.Symlink(destination, link); err != nil {
			t.Fatal(err)
		}
		if err := ValidateStateDirectory(cfg.Dir); err != nil {
			t.Fatalf("in-tree link %q rejected: %v", destination, err)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "fixture.log")
	if err := os.WriteFile(outside, []byte("outside-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(os.Getenv("HOME"), ".wingthing", "token.json")
	if err := os.MkdirAll(filepath.Dir(stable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stable, []byte("stable-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{outside, stable, "missing.log", "latest"} {
		if err := os.Symlink(destination, link); err != nil {
			t.Fatal(err)
		}
		if err := ValidateStateDirectory(cfg.Dir); err == nil {
			t.Fatalf("unsafe link %q accepted", destination)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
	// A chained link must be checked at its final target, not just its first hop.
	hop := filepath.Join(debug, "hop")
	if err := os.Symlink(outside, hop); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("hop", link); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStateDirectory(cfg.Dir); err == nil {
		t.Fatal("chained outside alias accepted")
	}
}

func TestPreviewExternalBinaryLinksRequireExistingExecutable(t *testing.T) {
	previewTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(cfg.Dir, "provider-home", ".local", "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "provider")
	if err := os.WriteFile(external, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "provider")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStateDirectory(cfg.Dir); err != nil {
		t.Fatal("shared executable rejected", err)
	}
	if err := os.Chmod(external, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStateDirectory(cfg.Dir); err == nil {
		t.Fatal("non-executable external config link accepted")
	}
	if err := os.Remove(external); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStateDirectory(cfg.Dir); err == nil {
		t.Fatal("dangling external executable link accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("provider", link); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStateDirectory(cfg.Dir); err == nil {
		t.Fatal("looping external executable link accepted")
	}
}

func TestPreviewRootAliasStillChecksNestedArtifacts(t *testing.T) {
	home := previewTest(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "preview-alias")
	if err := os.Symlink(cfg.Dir, alias); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStateDirectory(alias); err != nil {
		t.Fatal("same-channel root alias rejected", err)
	}
	outside := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cfg.Dir, "nested-alias")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStateDirectory(alias); err == nil {
		t.Fatal("root alias bypassed nested artifact checks")
	}
}
