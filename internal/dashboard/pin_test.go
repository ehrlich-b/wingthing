package dashboard

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
)

func TestDashboardPinsLeadAcrossMachinesAndKeepExplicitSelection(t *testing.T) {
	s := fixture()
	local := s.machines["local"]
	for i := 0; i < 40; i++ {
		local.sessions = append(local.sessions, session(fmt.Sprintf("egg-%d", i), fmt.Sprintf("agent-%d", i), "/a", "blocked"))
	}
	s.machines["local"] = local
	pin := session("parent", "coordinator", "/z", "idle")
	pin.Metadata = sessionrole.Metadata{Pinned: true, Role: "coordinator"}
	s.machines["remote"] = machine{sessions: []eggclient.LocalSession{pin}, updated: time.Now()}
	s.machines["offline"] = machine{err: "unreachable"}
	s.reconcile()
	rows := s.rows()
	if rows[0].key() != "remote:parent" || s.selected != "remote:parent" {
		t.Fatalf("first=%v selected=%s", rows[0], s.selected)
	}
	text := strings.Join(renderedLines(t, render(s, 100, 24, time.Now()), 100, 24), "\n")
	if strings.Index(text, "[coordinator]") > strings.Index(text, "local  ") || !strings.Contains(text, "@ remote") {
		t.Fatal(text)
	}
	act, _ := s.handle("enter")
	if act == nil || act.row.machine != "remote" || act.row.ID != "parent" {
		t.Fatalf("attach=%v", act)
	}
	s.move(1)
	selected := s.selected
	s.reconcile()
	if s.selected != selected {
		t.Fatal("refresh stole explicit selection")
	}
	pin.Pinned = false
	s.machines["remote"] = machine{sessions: []eggclient.LocalSession{pin}}
	if s.rows()[0].machine != "local" {
		t.Fatal("unpinned changed ordinary machine order")
	}
}

func TestDashboardCachedPinFromUnreachableRemoteRemainsStale(t *testing.T) {
	s := fixture()
	pin := session("parent", "coordinator", "/z", "idle")
	pin.Metadata = sessionrole.Metadata{Pinned: true, Role: "coordinator"}
	s.machines["remote"] = machine{sessions: []eggclient.LocalSession{pin}, err: "unreachable", updated: time.Now().Add(-time.Minute)}
	s.reconcile()
	text := strings.Join(renderedLines(t, render(s, 100, 24, time.Now()), 100, 24), "\n")
	if !strings.Contains(text, "@ remote stale") || !strings.Contains(text, "unreachable") {
		t.Fatal(text)
	}
}
