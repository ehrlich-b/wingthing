package dashboard

import (
	"github.com/ehrlich-b/wingthing/internal/eggclient"
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

func TestPaperLastLineMatchesRecentReplyBeforeReport(t *testing.T) {
	s := fixture()
	r, _ := s.selection()
	for _, tc := range []struct {
		name     string
		messages []previewMessage
		want     string
	}{
		{name: "reply", messages: []previewMessage{{speaker: "Codex", text: "The cache test passes."}, {speaker: "You", text: "Check the refresh too."}}, want: "The cache test passes."},
		{name: "report only", want: "Writing cache tests."},
		{name: "user only", messages: []previewMessage{{speaker: "You", text: "Check the refresh too."}}, want: "Writing cache tests."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.previews = map[attentionIdentity]sessionPreview{r.identity: {update: "Writing cache tests.", messages: tc.messages}}
			if got := lastLine(s, r); got != tc.want {
				t.Fatalf("last line = %q, want %q", got, tc.want)
			}
			text := strings.Join(renderedLines(t, render(s, 132, 36, time.Now()), 132, 36), "\n")
			if !strings.Contains(text, tc.want) {
				t.Fatalf("last line missing from list: %s", text)
			}
		})
	}
}

func TestMachineCountsNeverTurnMissingInventoryOrDenialIntoZero(t *testing.T) {
	for _, m := range []machine{{}, {err: "connection refused"}, {updated: time.Now(), err: "permission denied"}} {
		s := fixture()
		s.machines["work"] = m
		if strings.Contains(s.machineCount("work"), "0 session") {
			t.Fatal("unknown source became a zero count")
		}
		s.target.machine = "work"
		if strings.Contains(strings.Join(s.machineLines(time.Now()), "\n"), "0 session") {
			t.Fatal("details claimed known zero")
		}
	}
	s := fixture()
	s.machines["empty"] = machine{updated: time.Now()}
	if s.machineCount("empty") != "0 sessions" {
		t.Fatal("successful zero lost")
	}
}

func TestMachineCountsLeaveOutThePinnedWing(t *testing.T) {
	now := time.Now()
	s := &state{machines: map[string]machine{
		"local":  {updated: now, sessions: []eggclient.LocalSession{{ID: "a", Kind: "egg", Status: "idle"}, {ID: "b", Kind: "egg", Status: "idle"}}},
		"Studio": {updated: now, sessions: []eggclient.LocalSession{{ID: "wing", Kind: "egg", Status: "idle", Metadata: sessionrole.Metadata{Pinned: true, Role: "coordinator"}}, {ID: "c", Kind: "egg", Status: "idle"}}},
	}}
	if s.machineCount("local") != "2 sessions" || s.machineCount("Studio") != "1 session" {
		t.Fatalf("counts %q and %q include the pinned wing", s.machineCount("local"), s.machineCount("Studio"))
	}
	m := s.machines["Studio"]
	m.err = "connection refused"
	s.machines["Studio"] = m
	if s.machineCount("Studio") != "1 session cached" || !strings.Contains(render(s, 132, 36, now), "1 cached") {
		t.Fatalf("header cached count disagrees with %q", s.machineCount("Studio"))
	}
}

func TestPaperOfflinePinnedStateIsLastSeen(t *testing.T) {
	now := time.Date(2026, 10, 14, 13, 55, 0, 0, time.Local)
	for _, tc := range []struct {
		name, err, registryError, stamp string
		updated                         time.Time
	}{
		{name: "offline", err: "connection timed out", updated: now.Add(-14 * time.Minute), stamp: "13:41"},
		{name: "identity", err: "REMOTE HOST IDENTIFICATION HAS CHANGED", updated: now.Add(-14 * time.Minute), stamp: "13:41"},
		{name: "registry", registryError: "connection timed out", updated: now.Add(-14 * time.Minute), stamp: "13:41"},
		{name: "yesterday", err: "connection timed out", updated: now.Add(-24 * time.Hour), stamp: "Oct 13 13:55"},
		{name: "unknown", err: "connection timed out", stamp: "time unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture()
			m := s.machines["work"]
			m.sessions[0].Metadata = sessionrole.Metadata{Role: "coordinator", Pinned: true}
			m.err, m.updated = tc.err, tc.updated
			s.machines["work"] = m
			s.registryError = tc.registryError
			s.reconcile()
			for _, dim := range [][2]int{{80, 24}, {132, 36}} {
				lines := renderedLines(t, render(s, dim[0], dim[1], now), dim[0], dim[1])
				want := "Last seen " + tc.stamp + ". Reconnect work."
				if !strings.Contains(lines[4], want) || strings.Contains(lines[4], "o Idle") {
					t.Fatalf("%v cached coordinator row = %q; want %q", dim, lines[4], want)
				}
				if dim[0] == 132 && !tc.updated.IsZero() && !strings.Contains(strings.Join(lines, "\n"), "last seen "+tc.stamp) {
					t.Fatal("pinned row and machines pane lost the same observed time")
				}
			}
		})
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
