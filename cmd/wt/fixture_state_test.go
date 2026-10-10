package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
)

// Keep private state aliases inside the checkout while reserving enough bytes
// for a full egg ID and its longest socket name on Darwin. A long worktree name
// must fail before provider/FIFO startup, rather than hanging the CLI battery.
func shortFixtureStateAlias(t *testing.T, repo, target string) string {
	t.Helper()
	parent := filepath.Join(repo, ".s")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	alias, err := os.MkdirTemp(parent, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	if err := egg.ValidateSocketPath(filepath.Join(alias, "s", "eggs", strings.Repeat("x", 16), "tool.sock")); err != nil {
		t.Fatalf("fixture state alias is too long for this checkout: %v", err)
	}
	return alias
}
