package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCanonicalDenyPathsPreservesNamesAndMissingSymlinkTargets(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "policies")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	// deny-write must protect a file that can be created after compilation.
	declared := filepath.Join(alias, "missing.yaml")
	resolved := filepath.Join(real, "missing.yaml")
	got := CanonicalDenyPaths([]string{declared, resolved, declared})
	if !slices.Equal(got, []string{declared, resolved}) {
		t.Fatalf("deny plan = %v, want both names without duplicates", got)
	}
}

func TestCanonicalDenyPathsProjectsReadAliases(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dotfiles := filepath.Join(root, "dotfiles")
	if err := os.MkdirAll(filepath.Join(dotfiles, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"netrc", "egg.yaml", "zshrc"} {
		if err := os.WriteFile(filepath.Join(dotfiles, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "alias")
	nested := filepath.Join(root, "nested")
	for name, target := range map[string]string{".netrc": filepath.Join(dotfiles, "netrc"), ".policy": filepath.Join(dotfiles, "egg.yaml"), "alias": dotfiles, "nested": alias} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	aliases := []Mount{
		{Source: dotfiles, Target: alias, ReadOnly: true},
		{Source: alias, Target: nested, ReadOnly: true},
		{Source: filepath.Join(root, "dotfiles-other"), Target: filepath.Join(root, "unrelated"), ReadOnly: true},
	}
	for _, test := range []struct {
		name  string
		paths []string
		want  []string
	}{
		{"deny", []string{"/", filepath.Join(root, ".netrc"), filepath.Join(dotfiles, "private")}, []string{"/", filepath.Join(root, ".netrc"), filepath.Join(dotfiles, "netrc"), filepath.Join(dotfiles, "private"), filepath.Join(alias, "netrc"), filepath.Join(alias, "private"), filepath.Join(nested, "netrc"), filepath.Join(nested, "private")}},
		{"deny-write", []string{filepath.Join(root, ".policy"), filepath.Join(dotfiles, "missing.yaml")}, []string{filepath.Join(root, ".policy"), filepath.Join(dotfiles, "egg.yaml"), filepath.Join(dotfiles, "missing.yaml"), filepath.Join(alias, "egg.yaml"), filepath.Join(alias, "missing.yaml"), filepath.Join(nested, "egg.yaml"), filepath.Join(nested, "missing.yaml")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := CanonicalDenyPaths(test.paths, aliases...)
			if !slices.Equal(got, test.want) {
				t.Fatalf("alias mask plan = %v, want %v", got, test.want)
			}
			if again := CanonicalDenyPaths(got, aliases...); !slices.Equal(again, got) {
				t.Fatalf("alias mask plan added duplicates: %v", again)
			}
		})
	}
}
