package dashboard

import (
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"strings"
	"testing"
	"time"
)

func TestPaperLayoutKeepsReportsAndExactTargetAtBothSizes(t *testing.T) {
	s := fixture()
	r, _ := s.selection()
	s.previews = map[attentionIdentity]sessionPreview{r.identity: {messages: []previewMessage{{speaker: "Codex", text: "The DNS test is ready."}}, next: "Run the DNS tests."}}
	for _, dim := range [][2]int{{80, 24}, {132, 36}} {
		text := strings.Join(renderedLines(t, render(s, dim[0], dim[1], time.Now()), dim[0], dim[1]), "\n")
		for _, want := range []string{"State", "Unread", "Title", "Agent", "Machine", "Age", "FROM WING", "SELECTED", "Run the DNS tests.", "Enter open", "? help"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%v missing %q", dim, want)
			}
		}
		if dim[0] >= 120 {
			for _, want := range []string{"LAST LINE", "The DNS test is ready.", "MACHINES", "TODAY", "RECENT", "ASKING"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing wide context %q", want)
				}
			}
		}
	}
}
func TestMachineCountsNeverTurnMissingInventoryOrDenialIntoZero(t *testing.T) {
	for _, m := range []machine{{}, {err: "connection refused"}, {updated: time.Now(), err: "permission denied"}} {
		if strings.Contains(machineCount(m), "0 session") {
			t.Fatal("unknown source became a zero count")
		}
		s := fixture()
		s.machines["work"] = m
		s.target.machine = "work"
		if strings.Contains(strings.Join(s.machineLines(time.Now()), "\n"), "0 session") {
			t.Fatal("details claimed known zero")
		}
	}
	if machineCount(machine{updated: time.Now()}) != "0 sessions" {
		t.Fatal("successful zero lost")
	}
}

func TestPaperOfflinePinnedStateIsLastSeen(t *testing.T) {
	s := fixture()
	m := s.machines["work"]
	m.sessions[0].Metadata = sessionrole.Metadata{Role: "coordinator", Pinned: true}
	m.err = "connection timed out"
	s.machines["work"] = m
	s.reconcile()
	for _, w := range []int{80, 132} {
		h := 24
		if w == 132 {
			h = 36
		}
		lines := renderedLines(t, render(s, w, h, time.Now()), w, h)
		if !strings.Contains(lines[4], "Last seen") || strings.Contains(lines[4], "o Idle") {
			t.Fatal("cached coordinator advertised as live idle")
		}
		if !strings.Contains(lines[4], "Reconnect work") {
			t.Fatal("stale coordinator lost its reconnect step")
		}
	}
}

func TestUnavailableMachineScopeCannotPretendItHasNoMatches(t *testing.T) {
	s := fixture()
	s.machineFilter = "Missing"
	s.machines["Missing"] = machine{err: "permission denied"}
	s.reconcile()
	text := strings.Join(renderedLines(t, render(s, 80, 24, time.Now()), 80, 24), "\n")
	if !strings.Contains(text, "Sessions unavailable") || strings.Contains(text, "No matching sessions") {
		t.Fatal("failed scoped inventory was treated as no matches")
	}
}

func TestPaperNoticeKeepsWideFrameAndMissingWingUnknown(t *testing.T) {
	s := fixture()
	s.message = "Access denied. R refresh."
	lines := renderedLines(t, render(s, 132, 36, time.Now()), 132, 36)
	if lines[32] != paneBorder(s.theme, 132, 78, false) || !strings.Contains(lines[33], s.message) {
		t.Fatal("notice replaced the frame or lost its footer row")
	}
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "No wing session reported.") || strings.Contains(text, "No wing session configured.") {
		t.Fatal("missing wing observation became a configuration claim")
	}
}
