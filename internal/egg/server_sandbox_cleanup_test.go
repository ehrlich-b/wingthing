package egg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestRunSessionDestroysSandbox(t *testing.T) {
	// Keep race-instrumented namespace probes within the session deadline.
	t.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	if ok, help := sandbox.CheckCapability(); !ok {
		t.Skip(help)
	}
	root, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"exit", "initial turn", "endpoint", "pty", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			temp, err := os.MkdirTemp(root, "s")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(temp) })
			dir := filepath.Join(temp, "state", "eggs", "s")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			home, tmp := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("TMPDIR", tmp)
			t.Setenv("WINGTHING_DIR", filepath.Join(home, ".wingthing"))
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			rc := RunConfig{Agent: "custom", Command: []string{"/bin/sh", "-c", "exit 0"},
				UserHome: home, CWD: home, FS: []string{"ro:/"}, Network: []string{"*"},
				Rows: 24, Cols: 80, OmitBrowserBridge: true}
			want := ""
			switch scenario {
			case "initial turn":
				rc.Agent, rc.Command = "codex", nil
				bin := filepath.Join(home, "bin")
				if err := os.MkdirAll(bin, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'codex-cli 0.159.3'; fi\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				rc.InitialRun = &RunTurnRequest{}
				want = "reserve initial native turn"
			case "endpoint":
				if err := os.Mkdir(filepath.Join(dir, "egg.token"), 0700); err != nil {
					t.Fatal(err)
				}
				want = "token"
			case "pty":
				rc.CWD = filepath.Join(home, "missing")
				want = "start pty"
			case "cancel":
				rc.Command = []string{"/bin/sh", "-c", "exec sleep 30"}
				cancel()
				want = "context canceled"
			}
			err = (&Server{dir: dir, token: "fixture"}).RunSession(ctx, rc)
			if (want == "" && err != nil) || (want != "" && (err == nil || !strings.Contains(err.Error(), want))) {
				t.Fatalf("RunSession error = %v, want %q", err, want)
			}
			left, err := filepath.Glob(filepath.Join(tmp, "wt-sandbox-*"))
			if err != nil || len(left) != 0 {
				t.Fatalf("sandbox directories after %s: %v (error %v)", scenario, left, err)
			}
		})
	}
}
