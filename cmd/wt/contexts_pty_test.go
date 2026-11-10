//go:build !windows

package main

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestContextBuiltPTYHomeWork(t *testing.T) {
	f := northstarNewFixture(t)
	binary := northstarBuild(t, f, "stable")
	if err := config.SaveWingConfig(f.local, &config.WingConfig{MachineContext: "home"}); err != nil {
		t.Fatal(err)
	}
	northstarWrite(t, filepath.Join(f.local, "remotes.yaml"), fmt.Sprintf("remotes:\n  studio:\n    ssh_target: ns-devbox\n    wingthing_dir: %s\n    context: work\n  slide:\n    ssh_target: ns-empty\n    wingthing_dir: %s\n    context: work\n", f.remote, filepath.Join(f.home, "e")), 0600)
	northstarStandaloneEgg(t, f, f.local, "home-task", "home-long-running-task", "needs_input", "home")
	northstarStandaloneEgg(t, f, f.remote, "work-task", "work-long-running-task", "needs_input", "work")
	northstarStandaloneEgg(t, f, filepath.Join(f.home, "e"), "slide-task", "slide-long-running-task", "working", "slide")
	h := northstarStartPTY(t, f, binary, 120, 36)
	screen := h.expect("Home and Work groups", 0, "Home · 1 needs you", "Work · 1 needs you", "studio", "slide", "This machine")
	northstarWrite(t, filepath.Join(f.evidence, "context-built-pty-120x36.txt"), screen, 0600)
	h.expect("home filter", h.send("c"), "Home   1 machine connected")
	h.expect("work filter", h.send("c"), "Work   2 machines connected")
	h.exit("q")
}
