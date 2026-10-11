package egg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Include a symlinked parent on every platform, even where TempDir does not
// traverse macOS's /var -> /private/var. Keep canonical socket paths short too.
func terminalDogfoodDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "wt-forge-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	return alias
}

func TestResolveSessionCommandPathUsesEggCWD(t *testing.T) {
	for _, command := range []string{"./run.sh", "bin/run.sh", "../run.sh", "link/../run.sh"} {
		t.Run(command, func(t *testing.T) {
			root := terminalDogfoodDir(t)
			cwd := filepath.Join(root, "work")
			for _, dir := range []string{cwd, filepath.Join(cwd, "bin"), filepath.Join(root, "target", "child")} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(root, "target", "child"), filepath.Join(cwd, "link")); err != nil {
				t.Fatal(err)
			}
			// Distinct outputs catch accidental lexical cleaning of link/..
			// as well as lookup in the server's own working directory.
			for path, marker := range map[string]string{
				filepath.Join(cwd, "run.sh"):            "cwd",
				filepath.Join(cwd, "bin", "run.sh"):     "bin",
				filepath.Join(root, "run.sh"):           "parent",
				filepath.Join(root, "target", "run.sh"): "symlink-parent",
			} {
				if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '"+marker+"'\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			want := map[string]string{"./run.sh": "cwd", "bin/run.sh": "bin", "../run.sh": "parent", "link/../run.sh": "symlink-parent"}[command]
			path, err := resolveSessionCommandPath(command, cwd)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(path)
			cmd.Dir = cwd
			if output, err := cmd.Output(); err != nil || string(output) != want {
				t.Fatalf("resolved command output = %q, err=%v, want %q", output, err, want)
			}
		})
	}
}

func TestResolveSessionCommandPathPreservesLookupAndErrors(t *testing.T) {
	root := terminalDogfoodDir(t)
	t.Setenv("PATH", root)
	path := filepath.Join(root, "run.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"run.sh", path} {
		got, err := resolveSessionCommandPath(command, filepath.Join(root, "absent"))
		if err != nil || got != path {
			t.Fatalf("lookup %q = %q, %v; want %q", command, got, err, path)
		}
	}
	for _, command := range []string{"./missing.sh", "./run.sh", "."} {
		if command == "./run.sh" {
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := resolveSessionCommandPath(command, root); err == nil {
			t.Fatalf("invalid executable %q resolved to %q", command, got)
		}
	}
}

func TestRunSessionRelativeCommandUsesEggCWD(t *testing.T) {
	root := terminalDogfoodDir(t)
	cwd, dir := filepath.Join(root, "work"), filepath.Join(root, "state", "eggs", "ours")
	for _, path := range []string{cwd, dir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil || len(filepath.Join(canonicalDir, "egg.sock")) > 103 {
		t.Fatalf("socket fixture is not short: %q, %v", canonicalDir, err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "run.sh"), []byte("#!/bin/sh\nprintf 'ran-in-egg-cwd' > result\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := server.RunSession(ctx, RunConfig{Command: []string{"./run.sh"}, CWD: cwd, Cols: 80, Rows: 24, OuterBoundary: true, Env: map[string]string{"HOME": root}, Network: []string{"*"}, OmitBrowserBridge: true}); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(filepath.Join(cwd, "result"))
	if err != nil || strings.TrimSpace(string(result)) != "ran-in-egg-cwd" {
		t.Fatalf("relative command result = %q, err=%v", result, err)
	}
}
