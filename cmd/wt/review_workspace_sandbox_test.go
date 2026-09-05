//go:build integration && darwin

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/reviewjob"
	"golang.org/x/sys/unix"
)

func TestReviewWorkspaceTestProcessTree(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(strconv.FormatBool(deadline), func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(root, "home")
			cwd := filepath.Join(root, "workspace")
			for _, dir := range []string{home, cwd} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			if err := os.WriteFile(filepath.Join(cwd, "egg.yaml"), []byte("fs:\n  - rw:./\nnetwork: none\n"), 0600); err != nil {
				t.Fatal(err)
			}
			script := "sleep 30 >/dev/null 2>&1 & echo $! > child.pid"
			if deadline {
				script += "; wait"
			}
			record := reviewWorkspaceRecord{Workspace: reviewjob.Workspace{ID: "test-process-tree", CWD: cwd}, Spec: reviewjob.Spec{TestArgv: []string{"sh", "-c", script}, TestSeconds: 1}}
			s := &localMCPServer{cfg: &config.Config{Dir: filepath.Join(home, "state")}}
			result, err := s.testReviewWorkspace(context.Background(), record)
			if deadline {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("timeout: %+v %v", result, err)
				}
			} else if err != nil || result.ExitCode != 0 {
				t.Fatalf("normal return: %+v %v", result, err)
			}
			data, err := os.ReadFile(filepath.Join(cwd, "child.pid"))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || pid <= 1 {
				t.Fatalf("invalid child pid %q", data)
			}
			t.Cleanup(func() { _ = unix.Kill(pid, unix.SIGKILL) })
			until := time.Now().Add(2 * time.Second)
			for time.Now().Before(until) {
				if errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("test child %d survived operation completion", pid)
		})
	}
}

func TestReviewWorkspaceTestDeniesCredentials(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "workspace")
	state := filepath.Join(home, "state")
	for _, dir := range []string{state, cwd, filepath.Join(home, ".codex")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	for _, path := range []string{filepath.Join(state, "token"), filepath.Join(home, ".codex", "auth.json")} {
		if err := os.WriteFile(path, []byte("fixture-not-a-secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, "egg.yaml"), []byte("fs:\n  - rw:./\nnetwork: none\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := "if cat '" + state + "/token' >/dev/null 2>&1; then exit 91; fi; if cat '" + home + "/.codex/auth.json' >/dev/null 2>&1; then exit 92; fi; echo DENIED"
	record := reviewWorkspaceRecord{Workspace: reviewjob.Workspace{ID: "test-credentials", CWD: cwd}, Spec: reviewjob.Spec{TestArgv: []string{"sh", "-c", script}, TestSeconds: 10}}
	s := &localMCPServer{cfg: &config.Config{Dir: state}}
	result, err := s.testReviewWorkspace(context.Background(), record)
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "DENIED") {
		t.Fatalf("credential boundary: %+v %v", result, err)
	}
}
