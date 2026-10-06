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
