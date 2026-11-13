//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestContextBuiltPTYHomeWork(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	if err := config.SaveWingConfig(f.local, &config.WingConfig{MachineContext: "home"}); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.local, "remotes.yaml"), fmt.Sprintf("remotes:\n  studio:\n    ssh_target: ns-devbox\n    wingthing_dir: %s\n    context: work\n  office:\n    ssh_target: ns-empty\n    wingthing_dir: %s\n    context: work\n", f.remote, filepath.Join(f.home, "e")), 0600)
	northstarStandaloneEgg(t, f, f.local, "home-task", "home-long-running-task", "needs_input", "home")
	northstarStandaloneEgg(t, f, f.remote, "work-task", "work-long-running-task", "needs_input", "work")
	northstarStandaloneEgg(t, f, filepath.Join(f.home, "e"), "office-task", "office-long-running-task", "working", "office")
	h := northstarStartPTY(t, f, binary, 120, 36)
	screen := h.expect("Home and Work groups", 0, "Home · 1 needs you", "Work · 1 needs you", "studio", "office", "This machine")
	northstarWrite(t, filepath.Join(f.evidence, "context-built-pty-120x36.txt"), screen, 0600)
	h.expect("home filter", h.send("c"), "Home   1 machine connected")
	h.expect("work filter", h.send("c"), "Work   2 machines connected")
	h.exit("q")
}

func TestContextSetBuiltPTY(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	northstarWrite(t, filepath.Join(f.local, "remotes.yaml"), fmt.Sprintf("remotes:\n  studio:\n    ssh_target: ns-devbox\n    wingthing_dir: %s\n", f.remote), 0600)
	northstarStandaloneEgg(t, f, f.local, "home-task", "investigate-long-running-home-task", "needs_input", "home")
	h := northstarStartPTY(t, f, binary, 120, 36)
	screen := h.expect("ungrouped context hint", 0, "wt context set", "Needs you")
	if strings.Count(screen, "wt context set") != 1 {
		t.Fatalf("expected one hint:\n%s", screen)
	}
	northstarWrite(t, filepath.Join(f.evidence, "context-set-unassigned-120x36.txt"), screen, 0600)
	h.exit("q")
	for _, args := range [][]string{{"context", "set", "local", "home"}, {"context", "set", "studio", "work"}} {
		command := northstarStartPTY(t, f, binary, 120, 36, args...)
		command.exit("")
	}
	h = northstarStartPTY(t, f, binary, 120, 36)
	screen = h.expect("command configured Home and Work", 0, "Home · 1 needs you", "Work · 0 needs you", "studio", "This machine")
	if strings.Contains(screen, "wt context set") {
		t.Fatalf("assigned machines still show setup hint:\n%s", screen)
	}
	northstarWrite(t, filepath.Join(f.evidence, "context-set-grouped-120x36.txt"), screen, 0600)
	h.exit("q")
}

func TestContextBuiltCLIPreservesLegacyWingID(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	legacy, err := os.ReadFile("../../internal/config/testdata/context-legacy-wing-id")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.local, "wing-id")
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Env = f.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("wt %v: %v\n%s", args, err, out)
		}
		t.Logf("wt %v:\n%s", args, out)
		return string(out)
	}
	for _, value := range []string{"none", "home", "work", "none"} {
		run("context", "set", "local", value)
		out := run("wing", "config")
		if !strings.Contains(out, "wing_id:    0123456789abcdef0123\n") {
			t.Fatalf("identity lost after %s:\n%s", value, out)
		}
		cfg, err := config.LoadWingConfig(f.local)
		if err != nil || cfg.WingID != "0123456789abcdef0123" {
			t.Fatalf("loaded identity after %s: %+v, %v", value, cfg, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, legacy) {
			t.Fatalf("legacy bytes changed after %s: %q, %v", value, after, err)
		}
	}
}
