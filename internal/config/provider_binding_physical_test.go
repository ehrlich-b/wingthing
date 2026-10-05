//go:build darwin || linux

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Physical aliases such as macOS firmlinks cannot be created in a temporary
// fixture, so the identity hook reports one fixture directory's identity for
// another. Every path is a temporary fixture with the fake account home. These
// tests prove the identity comparison only; they do not prove enforcement
// against actual macOS firmlinks or Linux bind/nullfs mounts.
func fakePhysicalAliases(t *testing.T, aliases map[string]string) {
	t.Helper()
	old := providerPathIdentity
	providerPathIdentity = func(path string) (providerPathID, error) {
		if target, ok := aliases[path]; ok {
			path = target
		}
		return old(path)
	}
	t.Cleanup(func() { providerPathIdentity = old })
}

func requirePhysicalBindingRejected(t *testing.T, state, provider, want string) {
	t.Helper()
	writeProviderBinding(t, state, provider+"\n", 0600)
	before := stateEntries(t, state)
	home, bound, err := ResolvePreviewProviderHome(state)
	if err == nil || !bound || home != "" {
		t.Fatalf("ASSERTION: physically overlapping binding accepted or fell back: home=%q bound=%v err=%v", home, bound, err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("ASSERTION: binding refused for the wrong reason: %v", err)
	}
	if cfg, err := Load(); err == nil {
		t.Fatalf("ASSERTION: Load accepted physically overlapping binding with provider home %q", cfg.ProviderDataHome())
	}
	if after := stateEntries(t, state); after != before {
		t.Fatalf("ASSERTION: failed Load changed state: %s -> %s", before, after)
	}
	if entries := stateEntries(t, provider); entries != "" {
		t.Fatalf("ASSERTION: refused binding wrote into the provider home: %s", entries)
	}
}

func TestPreviewProviderBindingPhysicalOverlapRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		// alias returns the fixture directory the provider home (or, for
		// descendant, its parent) physically is.
		alias      func(root, state, account string) string
		descendant bool
		setup      func(t *testing.T, account string)
		want       string
	}{
		{name: "same-as-state", alias: func(_, state, _ string) string { return state }, want: "physically overlap selected preview state"},
		{name: "inside-state", alias: func(_, state, _ string) string { return state }, descendant: true, want: "physically overlap selected preview state"},
		{name: "ancestor-of-state", alias: func(root, _, _ string) string { return root }, want: "physically overlap"},
		{name: "ancestor-of-future-wingthing", alias: func(_, _, account string) string { return account }, want: "physically overlap"},
		{
			name:  "same-as-existing-claude",
			alias: func(_, _, account string) string { return filepath.Join(account, ".claude") },
			setup: func(t *testing.T, account string) { mustMkdir(t, filepath.Join(account, ".claude"), 0700) },
			want:  "physically overlap OS-account .claude ",
		},
		{
			name:  "inside-existing-wingthing",
			alias: func(_, _, account string) string { return filepath.Join(account, ".wingthing") },
			setup: func(t *testing.T, account string) {
				mustMkdir(t, filepath.Join(account, ".wingthing"), 0700)
			},
			descendant: true,
			want:       "physically overlap OS-account .wingthing",
		},
		{
			name:  "ancestor-of-existing-claude-json",
			alias: func(_, _, account string) string { return account },
			setup: func(t *testing.T, account string) {
				if err := os.WriteFile(filepath.Join(account, ".claude.json"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			},
			want: "physically overlap",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, state, _, account := providerBindingFixture(t)
			if tc.setup != nil {
				tc.setup(t, account)
			}
			// A lexically separate directory that the fake reports as the alias.
			provider := filepath.Join(root, "firm", "provider-home")
			mustMkdir(t, filepath.Dir(provider), 0700)
			mustMkdir(t, provider, 0700)
			aliased := provider
			if tc.descendant {
				aliased = filepath.Dir(provider)
			}
			fakePhysicalAliases(t, map[string]string{aliased: tc.alias(root, state, account)})
			requirePhysicalBindingRejected(t, state, provider, tc.want)
		})
	}
}

// The fake account home itself does not exist yet: every protected account
// path is a future suffix of the directory the provider home physically is.
func TestPreviewProviderBindingPhysicalFutureAccountSuffixRejected(t *testing.T) {
	root, state, _, account := providerBindingFixture(t)
	missing := filepath.Join(account, "not-yet", "home")
	previewAccountHome = func() (string, error) { return missing, nil }
	provider := filepath.Join(root, "firm-provider")
	mustMkdir(t, provider, 0700)
	fakePhysicalAliases(t, map[string]string{provider: account})
	requirePhysicalBindingRejected(t, state, provider, "physically overlap")
}

// A dangling protected link would create its target, possibly inside the
// provider home, so its future identity is unverifiable. Real fixture link.
func TestPreviewProviderBindingPhysicalDanglingProtectedLinkRejected(t *testing.T) {
	_, state, provider, account := providerBindingFixture(t)
	mustSymlink(t, filepath.Join(provider, "future-claude"), filepath.Join(account, ".claude"))
	requirePhysicalBindingRejected(t, state, provider, "link whose target does not exist")
}

func TestPreviewProviderBindingPhysicalIdentityErrorFailsClosed(t *testing.T) {
	_, state, provider, account := providerBindingFixture(t)
	old := providerPathIdentity
	providerPathIdentity = func(path string) (providerPathID, error) {
		if path == account {
			return providerPathID{}, errors.New("fixture metadata unavailable")
		}
		return old(path)
	}
	t.Cleanup(func() { providerPathIdentity = old })
	requirePhysicalBindingRejected(t, state, provider, "cannot verify the physical identity")
}

// Absent protected paths share only the account ancestor with a home below
// it; that is not overlap, so the home is selected exactly.
func TestPreviewProviderBindingPhysicalHomeBelowAbsentAccountPathsAccepted(t *testing.T) {
	_, state, _, account := providerBindingFixture(t)
	provider := filepath.Join(account, "providers", "home")
	mustMkdir(t, filepath.Dir(provider), 0700)
	mustMkdir(t, provider, 0700)
	for _, name := range []string{".wingthing", ".claude", ".claude.json"} {
		if _, err := os.Lstat(filepath.Join(account, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fixture account %s unexpectedly present: %v", name, err)
		}
	}
	writeProviderBinding(t, state, provider+"\n", 0600)
	home, bound, err := ResolvePreviewProviderHome(state)
	if err != nil || !bound || home != provider {
		t.Fatalf("ASSERTION: home below absent account paths not selected exactly: %q %v %v", home, bound, err)
	}
	cfg, err := Load()
	if err != nil || cfg.ProviderDataHome() != provider {
		t.Fatalf("ASSERTION: Load did not carry the exact bound home: %v", err)
	}
	if entries := stateEntries(t, provider); entries != "" {
		t.Fatalf("ASSERTION: binding resolution wrote into the provider home: %s", entries)
	}
}

// A physically separate home is selected with its exact lexical spelling,
// even while unrelated fixture aliases exist and protected paths are absent.
func TestPreviewProviderBindingPhysicalSeparateHomeAcceptedExactly(t *testing.T) {
	root, state, provider, account := providerBindingFixture(t)
	other := filepath.Join(root, "other")
	mustMkdir(t, other, 0700)
	fakePhysicalAliases(t, map[string]string{other: account})
	var seen []string
	inner := providerPathIdentity
	providerPathIdentity = func(path string) (providerPathID, error) {
		seen = append(seen, path)
		return inner(path)
	}
	writeProviderBinding(t, state, provider+"\n", 0600)
	home, bound, err := ResolvePreviewProviderHome(state)
	if err != nil || !bound || home != provider {
		t.Fatalf("separate binding not selected exactly: %q %v %v", home, bound, err)
	}
	if len(seen) == 0 {
		t.Fatal("ASSERTION: physical identities were not compared")
	}
	cfg, err := Load()
	if err != nil || cfg.ProviderDataHome() != provider {
		t.Fatalf("Load did not carry the exact bound home: %v", err)
	}
	if entries := stateEntries(t, provider); entries != "" {
		t.Fatalf("binding resolution wrote into the provider home: %s", entries)
	}
}
