package dashboard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func contextFixture(n int) *state {
	now := time.Date(2026, time.November, 9, 14, 30, 0, 0, time.UTC)
	s := &state{localContext: "home", machines: map[string]machine{"local": {updated: now}, "studio": {updated: now}, "slide": {updated: now}}, remotes: map[string]config.Remote{"studio": {Context: "work"}, "slide": {Context: "work"}}}
	names := []string{"local", "slide", "studio"}
	for i := 0; i < n; i++ {
		name := names[i%3]
		m := s.machines[name]
		status := "working"
		if i < 3 {
			status = "blocked"
		}
		m.sessions = append(m.sessions, session(fmt.Sprintf("task%02d", i), fmt.Sprintf("task-%02d-investigate-long-running-build-and-verify-release", i), "/projects/product", status))
		s.machines[name] = m
	}
	s.reconcile()
	return s
}
func TestContextGroupsCountsAndCycle(t *testing.T) {
	s := contextFixture(3)
	screen := strings.Join(renderedLines(t, render(s, 120, 36, time.Date(2026, time.November, 9, 14, 30, 0, 0, time.UTC)), 120, 36), "\n")
	for _, want := range []string{"Home · 1 needs you", "Work · 2 needs you", "This machine", "slide", "studio"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("missing %q:\n%s", want, screen)
		}
	}
	for _, want := range []string{"home", "work", ""} {
		s.handle("c")
		if s.contextFilter != want || s.contextFiltered != (want != "") {
			t.Fatalf("cycle = %q %t", s.contextFilter, s.contextFiltered)
		}
		for _, row := range s.rows() {
			if want != "" && s.machineContext(row.machine) != want {
				t.Fatal("filter leaked another context")
			}
		}
	}
	s.handle("c")
	s.handle("c")
	s.handle("/")
	s.handle("t")
	s.handle("enter")
	for _, r := range s.rows() {
		if r.machine == "local" {
			t.Fatal("search escaped context")
		}
	}
	m := s.machines["slide"]
	m.err = "offline"
	s.machines["slide"] = m
	if s.contextNeedsYou("work") != 1 {
		t.Fatal("stale session counted as current needs you")
	}
	s.contextFiltered = false
	s.remotes["studio"] = config.Remote{}
	s.handle("c")
	if !s.contextFiltered || s.contextFilter != "" {
		t.Fatal("unassigned group missing from cycle")
	}
}
func TestContextViewportSelectionAndEvidence(t *testing.T) {
	for _, n := range []int{0, 1, 3, 8, 12} {
		for _, dim := range [][2]int{{48, 36}, {120, 36}, {180, 36}} {
			s := contextFixture(n)
			output := render(s, dim[0], dim[1], time.Date(2026, time.November, 9, 14, 30, 0, 0, time.UTC))
			lines := renderedLines(t, output, dim[0], dim[1])
			screen := strings.Join(lines, "\n")
			if dir := os.Getenv("WT_CONTEXT_EVIDENCE"); dir != "" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("context-%dx%d-%d", dim[0], dim[1], n)
				if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(screen+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(output), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < n; i++ {
				s.move(1)
				screen = strings.Join(renderedLines(t, render(s, dim[0], dim[1], time.Date(2026, time.November, 9, 14, 30, 0, 0, time.UTC)), dim[0], dim[1]), "\n")
				if !strings.Contains(screen, ">") {
					t.Fatalf("selected row disappeared at %v row %d:\n%s", dim, i, screen)
				}
			}
		}
	}
	// No context metadata preserves the established ungrouped layout.
	s := &state{machines: map[string]machine{"local": {sessions: []eggclient.LocalSession{session("one", "one", "/app", "idle")}}}}
	if s.hasContexts() {
		t.Fatal("old config unexpectedly grouped")
	}
}
