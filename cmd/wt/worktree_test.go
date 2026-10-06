package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeNewReportsRequiredCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(workspace, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "Initial commit"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
	}
	t.Setenv("WINGTHING_DIR", filepath.Join(workspace, "state"))
	t.Setenv("WINGTHING_PREVIEW_DIR", "")
	t.Setenv("WINGTHING_WORKTREE_ROOT", "")
	var stdout, stderr bytes.Buffer
	cmd := worktreeCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--repo", repo, "new", "child"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, ".wingthing-worktrees", "repo", "child")
	if stdout.String() != path+"\n" || !strings.Contains(stderr.String(), "checkout_required: true") || !strings.Contains(stderr.String(), "child sandbox") {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Fatal(err)
	}
}
