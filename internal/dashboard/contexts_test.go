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
	for _, n := range []int{0, 1, 3, 8, 12, 40} {
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

// Grouped navigation and action routing must follow the exact rendered order,
// including coordinators pinned on more than one machine.
func TestContextPinnedNavigationMatchesDisplay(t *testing.T) {
	s := contextFixture(9)
	for _, name := range s.machineNames() {
		m := s.machines[name]
		pinned := session("coordinator", "wing", "/projects/product", "working")
		pinned.Pinned, pinned.Role = true, "coordinator"
		m.sessions = append(m.sessions, pinned)
		s.machines[name] = m
	}
	s.reconcile()
	now := time.Date(2026, time.November, 9, 14, 30, 0, 0, time.UTC)
	check := func() {
		t.Helper()
		rows := s.rows()
		lines := make([]viewLine, len(rows)+10)
		s.renderContexts(lines, rows, 0, len(lines)-1, 120, now)
		var displayed []string
		for _, line := range lines {
			for _, r := range rows {
				if line.text == sessionLine(s, r, 120, now).text {
					displayed = append(displayed, r.key())
				}
			}
		}
		if len(displayed) != len(rows) {
			t.Fatalf("displayed %v; rows %v", displayed, rows)
		}
		s.selected = rows[0].key()
		for i, key := range displayed {
			if rows[i].key() != key || s.selected != key {
				t.Fatalf("row %d: displayed %s, ordered %s, selected %s", i, key, rows[i].key(), s.selected)
			}
			act, _ := s.handle("enter")
			if act == nil || act.row.key() != key {
				t.Fatalf("action targets hidden row: %+v", act)
			}
			s.move(1)
		}
	}
	check()
	for _, name := range s.machineNames() {
		s.machineFilter = name
		s.reconcile()
		for _, r := range s.rows() {
			if r.machine != name {
				t.Fatalf("hidden pinned row %s escaped machine filter %s", r.key(), name)
			}
		}
		check()
	}
	s.machineFilter = ""
	s.contextFiltered, s.contextFilter = true, "work"
	s.reconcile()
	check()
	// Search and attention filters still retain visible pins on scoped machines.
	s.machineFilter, s.filter, s.attentionOnly = "studio", "no-match", true
	s.reconcile()
	if rows := s.rows(); len(rows) != 1 || !rows[0].Pinned {
		t.Fatalf("lost scoped pin: %+v", rows)
	}
	check()
}
