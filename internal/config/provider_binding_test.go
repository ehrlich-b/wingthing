//go:build darwin || linux

package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// Every path is a temporary fixture. The OS-account lookup is replaced so the
// overlap checks never inspect a real account's provider configuration.
func providerBindingFixture(t *testing.T) (root, state, provider, account string) {
	t.Helper()
	previewTest(t)
	root = canonicalConfiguredPath(t.TempDir(), "")
	account = filepath.Join(root, "account")
	state = filepath.Join(root, "state")
	provider = filepath.Join(root, "provider-home")
	for _, dir := range []string{account, state, provider} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, channelMarker), []byte("preview\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old := previewAccountHome
	previewAccountHome = func() (string, error) { return account, nil }
	t.Cleanup(func() { previewAccountHome = old })
	t.Setenv("WINGTHING_DIR", state)
	return root, state, provider, account
}

func writeProviderBinding(t *testing.T, state, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(state, ProviderHomeBinding)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func stateEntries(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func TestPreviewProviderBindingMissingKeepsExactDefault(t *testing.T) {
	_, state, _, _ := providerBindingFixture(t)
	home, bound, err := ResolvePreviewProviderHome(state)
	if err != nil || bound || home != PreviewProviderHome(state) || home != filepath.Join(state, "provider-home") {
		t.Fatalf("default provider home changed: %q %v %v", home, bound, err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProviderDataHome() != PreviewProviderHome(cfg.Dir) {
		t.Fatalf("loaded default provider home changed: %q", cfg.ProviderDataHome())
	}
	if unloaded := (&Config{Dir: state}); unloaded.ProviderDataHome() != PreviewProviderHome(state) {
		t.Fatal("unloaded config did not use the default provider home")
	}
}

func TestPreviewProviderBindingSelectsExactCanonicalExistingHome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		newline bool
		mode    os.FileMode
	}{{"newline-0600", true, 0600}, {"bare-0400", false, 0400}} {
		t.Run(tc.name, func(t *testing.T) {
			_, state, provider, _ := providerBindingFixture(t)
			content := provider
			if tc.newline {
				content += "\n"
			}
			writeProviderBinding(t, state, content, tc.mode)
			home, bound, err := ResolvePreviewProviderHome(state)
			if err != nil || !bound || home != provider {
				t.Fatalf("binding not selected exactly: %q %v %v", home, bound, err)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ProviderDataHome() != provider {
				t.Fatalf("Load did not carry the bound home: %q", cfg.ProviderDataHome())
			}
			// The binding relocates only the data home; it creates nothing there.
			if entries := stateEntries(t, provider); entries != "" {
				t.Fatalf("binding resolution wrote into the provider home: %s", entries)
			}
		})
	}
}

func TestPreviewProviderBindingInvalidPresentBindingFailsClosed(t *testing.T) {
	cases := map[string]func(t *testing.T, root, state, provider, account string){
		"empty":        func(t *testing.T, _, state, _, _ string) { writeProviderBinding(t, state, "", 0600) },
		"newline-only": func(t *testing.T, _, state, _, _ string) { writeProviderBinding(t, state, "\n", 0600) },
		"two-lines": func(t *testing.T, _, state, provider, _ string) {
			writeProviderBinding(t, state, provider+"\n"+provider+"\n", 0600)
		},
		"carriage-return": func(t *testing.T, _, state, provider, _ string) {
			writeProviderBinding(t, state, provider+"\r\n", 0600)
		},
		"invalid-utf8": func(t *testing.T, _, state, provider, _ string) {
			writeProviderBinding(t, state, provider+"\xff", 0600)
		},
		"relative": func(t *testing.T, _, state, _, _ string) { writeProviderBinding(t, state, "provider-home\n", 0600) },
		"unclean": func(t *testing.T, root, state, _, _ string) {
			writeProviderBinding(t, state, root+"/state/../provider-home\n", 0600)
		},
		"trailing-slash": func(t *testing.T, _, state, provider, _ string) { writeProviderBinding(t, state, provider+"/\n", 0600) },
		"oversized": func(t *testing.T, _, state, _, _ string) {
			writeProviderBinding(t, state, "/"+strings.Repeat("a", providerHomeBindingLimit), 0600)
		},
		"group-readable": func(t *testing.T, _, state, provider, _ string) { writeProviderBinding(t, state, provider, 0640) },
		"world-writable": func(t *testing.T, _, state, provider, _ string) { writeProviderBinding(t, state, provider, 0606) },
		"directory": func(t *testing.T, _, state, _, _ string) {
			mustMkdir(t, filepath.Join(state, ProviderHomeBinding), 0700)
		},
		"fifo-never-read": func(t *testing.T, _, state, _, _ string) { mustMkfifo(t, filepath.Join(state, ProviderHomeBinding)) },
		"symlink-to-valid-file": func(t *testing.T, root, state, provider, _ string) {
			target := filepath.Join(root, "binding-elsewhere")
			if err := os.WriteFile(target, []byte(provider), 0600); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, target, filepath.Join(state, ProviderHomeBinding))
		},
		"dangling-symlink": func(t *testing.T, root, state, _, _ string) {
			mustSymlink(t, filepath.Join(root, "missing"), filepath.Join(state, ProviderHomeBinding))
		},
		"hard-linked": func(t *testing.T, root, state, provider, _ string) {
			path := writeProviderBinding(t, state, provider, 0600)
			if err := os.Link(path, filepath.Join(root, "second-link")); err != nil {
				t.Fatal(err)
			}
		},
		"missing-target": func(t *testing.T, root, state, _, _ string) {
			writeProviderBinding(t, state, filepath.Join(root, "absent"), 0600)
		},
		"file-target": func(t *testing.T, root, state, _, _ string) {
			target := filepath.Join(root, "plain-file")
			if err := os.WriteFile(target, nil, 0600); err != nil {
				t.Fatal(err)
			}
			writeProviderBinding(t, state, target, 0600)
		},
		"alias-target": func(t *testing.T, root, state, provider, _ string) {
			alias := filepath.Join(root, "provider-alias")
			mustSymlink(t, provider, alias)
			writeProviderBinding(t, state, alias, 0600)
		},
		"group-writable-target": func(t *testing.T, _, state, provider, _ string) {
			if err := os.Chmod(provider, 0770); err != nil {
				t.Fatal(err)
			}
			writeProviderBinding(t, state, provider, 0600)
		},
		"inside-selected-state": func(t *testing.T, _, state, _, _ string) {
			inside := filepath.Join(state, "provider-home")
			mustMkdir(t, inside, 0700)
			writeProviderBinding(t, state, inside, 0600)
		},
		"contains-selected-state": func(t *testing.T, root, state, _, _ string) { writeProviderBinding(t, state, root, 0600) },
		"stable-state": func(t *testing.T, _, state, _, account string) {
			stable := filepath.Join(account, ".wingthing", "provider-home")
			mustMkdir(t, stable, 0700)
			writeProviderBinding(t, state, stable, 0600)
		},
		"host-claude-config": func(t *testing.T, _, state, _, account string) {
			writeProviderBinding(t, state, filepath.Join(account, ".claude"), 0600)
		},
		"host-claude-json": func(t *testing.T, _, state, _, account string) {
			writeProviderBinding(t, state, filepath.Join(account, ".claude.json"), 0600)
		},
		"account-home": func(t *testing.T, _, state, _, account string) { writeProviderBinding(t, state, account, 0600) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root, state, provider, account := providerBindingFixture(t)
			setup(t, root, state, provider, account)
			before := stateEntries(t, state)
			if home, bound, err := ResolvePreviewProviderHome(state); err == nil || !bound || home != "" {
				t.Fatalf("invalid binding accepted or fell back: %q %v %v", home, bound, err)
			}
			if cfg, err := Load(); err == nil {
				t.Fatalf("Load accepted invalid binding with provider home %q", cfg.ProviderDataHome())
			}
			if after := stateEntries(t, state); after != before {
				t.Fatalf("failed Load changed state: %s -> %s", before, after)
			}
		})
	}
}

func TestPreviewProviderBindingIsIgnoredByStable(t *testing.T) {
	_, state, provider, _ := providerBindingFixture(t)
	ReleaseChannel = "stable"
	if _, _, err := ResolvePreviewProviderHome(state); err == nil {
		t.Fatal("stable resolved a preview binding")
	}
	stableState := filepath.Join(t.TempDir(), "stable")
	mustMkdir(t, stableState, 0700)
	// Even an invalid binding file is never read by the stable loader.
	writeProviderBinding(t, stableState, provider, 0644)
	t.Setenv("WINGTHING_DIR", stableState)
	if _, err := Load(); err != nil {
		t.Fatalf("stable Load read a preview binding: %v", err)
	}
}

func TestPreviewProviderBindingReentryFollowsBoundHome(t *testing.T) {
	_, state, provider, _ := providerBindingFixture(t)
	writeProviderBinding(t, state, provider, 0600)
	if ok, err := validatePreviewProviderReentry(state, provider); err != nil || !ok {
		t.Fatalf("bound provider home could not re-enter its parent: %v %v", ok, err)
	}
	unbound := filepath.Join(state, "provider-home")
	mustMkdir(t, unbound, 0700)
	if ok, err := validatePreviewProviderReentry(state, unbound); err != nil || ok {
		t.Fatalf("default provider home re-entered a bound parent: %v %v", ok, err)
	}
	if err := os.Chmod(filepath.Join(state, ProviderHomeBinding), 0644); err != nil {
		t.Fatal(err)
	}
	if ok, err := validatePreviewProviderReentry(state, provider); err == nil || ok {
		t.Fatal("re-entry accepted an invalid binding")
	}
}

func mustMkdir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func mustMkfifo(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
}
