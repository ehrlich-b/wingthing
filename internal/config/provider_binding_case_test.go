//go:build darwin || linux

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Case-insensitive volumes resolve a wrong-case spelling to the same directory
// while keeping the caller's spelling. Every path here is a temporary fixture
// and the OS-account home is the fixture's fake account.

func fixtureFoldsCase(t *testing.T, root string) bool {
	t.Helper()
	probe := filepath.Join(root, "case-probe")
	if err := os.WriteFile(probe, nil, 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer os.Remove(probe)
	_, err := os.Lstat(filepath.Join(root, "CASE-PROBE"))
	return err == nil
}

func requireCaseFoldingFixture(t *testing.T, root string) {
	t.Helper()
	if !fixtureFoldsCase(t, root) {
		t.Skip("fixture volume is case-sensitive; wrong-case aliases name no existing directory here")
	}
}

func requireRejectedBinding(t *testing.T, state, provider, want string) {
	t.Helper()
	before := stateEntries(t, state)
	home, bound, err := ResolvePreviewProviderHome(state)
	if err == nil || !bound || home != "" {
		t.Fatalf("ASSERTION: case alias binding accepted or fell back: home=%q bound=%v err=%v", home, bound, err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("ASSERTION: binding refused for the wrong reason: %v", err)
	}
	if cfg, err := Load(); err == nil {
		t.Fatalf("ASSERTION: Load accepted case alias binding with provider home %q", cfg.ProviderDataHome())
	}
	if after := stateEntries(t, state); after != before {
		t.Fatalf("ASSERTION: failed Load changed state: %s -> %s", before, after)
	}
	if entries := stateEntries(t, provider); entries != "" {
		t.Fatalf("ASSERTION: refused binding wrote into the provider home: %s", entries)
	}
}

// Runs on every volume: the exact on-disk spelling is the selected data home
// and the config directory derived from it.
func TestPreviewProviderBindingCaseExactSpellingIsIdentity(t *testing.T) {
	root, state, _, _ := providerBindingFixture(t)
	provider := filepath.Join(root, "Mixed", "Provider-Home")
	mustMkdir(t, provider, 0700)
	writeProviderBinding(t, state, provider+"\n", 0600)
	home, bound, err := ResolvePreviewProviderHome(state)
	if err != nil || !bound || home != provider {
		t.Fatalf("ASSERTION: exact binding not selected exactly: home=%q bound=%v err=%v", home, bound, err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("ASSERTION: Load refused exact binding: %v", err)
	}
	if cfg.ProviderDataHome() != provider {
		t.Fatalf("ASSERTION: Load did not carry the exact home: %q", cfg.ProviderDataHome())
	}
	if want := root + "/Mixed/Provider-Home/.claude"; filepath.Join(cfg.ProviderDataHome(), ".claude") != want {
		t.Fatalf("ASSERTION: config directory %q, want %q", filepath.Join(cfg.ProviderDataHome(), ".claude"), want)
	}
}

func TestPreviewProviderBindingCaseAliasRejected(t *testing.T) {
	for name, alias := range map[string]func(root string) string{
		"final-component":    func(root string) string { return filepath.Join(root, "Mixed", "PROVIDER-HOME") },
		"ancestor-component": func(root string) string { return filepath.Join(root, "mixed", "Provider-Home") },
	} {
		t.Run(name, func(t *testing.T) {
			root, state, _, _ := providerBindingFixture(t)
			provider := filepath.Join(root, "Mixed", "Provider-Home")
			mustMkdir(t, provider, 0700)
			requireCaseFoldingFixture(t, root)
			writeProviderBinding(t, state, alias(root), 0600)
			requireRejectedBinding(t, state, provider, "exact")
		})
	}
}

// A wrong-case spelling of the fake host .claude must not get past the
// case-sensitive overlap checks into the host provider directory.
func TestPreviewProviderBindingCaseHostClaudeCollisionRejected(t *testing.T) {
	t.Run("existing-host-claude", func(t *testing.T) {
		root, state, _, account := providerBindingFixture(t)
		inside := filepath.Join(account, ".claude", "x")
		mustMkdir(t, inside, 0700)
		requireCaseFoldingFixture(t, root)
		writeProviderBinding(t, state, filepath.Join(account, ".CLAUDE", "x"), 0600)
		requireRejectedBinding(t, state, inside, "overlap")
	})
	t.Run("future-host-claude", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("only macOS volumes may fold .CLAUDE onto a later host .claude")
		}
		_, state, _, account := providerBindingFixture(t)
		inside := filepath.Join(account, ".CLAUDE", "x")
		mustMkdir(t, inside, 0700)
		writeProviderBinding(t, state, inside, 0600)
		requireRejectedBinding(t, state, inside, "overlap")
	})
}
