package dashboard

import (
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"testing"
)

func TestDashboardDurableUnreadSurvivesReopenAndKeepsRoutesSeparate(t *testing.T) {
	dir := testsocket.Dir(t)
	mk := func(completion int64) *state {
		s := &state{localDir: dir, persistAttention: true, machines: map[string]machine{"local": {sessions: []eggclient.LocalSession{{ID: "same", State: "completed", StateSource: "claude_hook", StateCursor: completion, CompletionCursor: completion}}}}}
		s.loadReadCursors()
		s.reconcile()
		return s
	}
	first := mk(1)
	r, _ := first.selection()
	first.acknowledge(r)
	next := mk(2)
	r, _ = next.selection()
	if r.attention != "unread" {
		t.Fatal("new completion lost on reopen")
	}
	next.acknowledge(r)
	again := mk(2)
	r, _ = again.selection()
	if r.attention == "unread" {
		t.Fatal("acknowledged cursor lost")
	}
	// Older open viewer cannot overwrite newer read cursor.
	first.acknowledge(r)
	latest := mk(2)
	r, _ = latest.selection()
	if r.attention == "unread" {
		t.Fatal("concurrent viewer regressed cursor")
	}
	remote := r.identity
	remote.machine = "other"
	if cursorKey(remote) == cursorKey(r.identity) {
		t.Fatal("colliding route merged")
	}
}

func TestDashboardFirstBaselineDecidedUnderLock(t *testing.T) {
	dir := testsocket.Dir(t)
	a := &state{localDir: dir, persistAttention: true}
	a.loadReadCursors()
	b := &state{localDir: dir, persistAttention: true}
	b.loadReadCursors()
	id := attentionIdentity{machine: "local", session: "same", localDir: dir}
	if got := b.baselineReadCursor(id, 1); got != 1 {
		t.Fatal(got)
	}
	// A loaded before B persisted and before the completion cursor advanced.
	if got := a.baselineReadCursor(id, 2); got != 1 {
		t.Fatalf("first baseline advanced past unread response: %d", got)
	}
	if got := readCursors(dir + "/navigator-read.json")[cursorKey(id)]; got != 1 {
		t.Fatal("persisted baseline regressed", got)
	}
}

func TestDashboardDelayedBaselineNeverRegressesSavedRead(t *testing.T) {
	s := &state{localDir: testsocket.Dir(t), persistAttention: true}
	id := attentionIdentity{machine: "local", session: "same", localDir: s.localDir}
	s.saveReadCursor(id, 2)
	if got := s.baselineReadCursor(id, 1); got != 1 {
		t.Fatal("view not clamped", got)
	}
	if got := readCursors(s.localDir + "/navigator-read.json")[cursorKey(id)]; got != 2 {
		t.Fatal("durable read regressed", got)
	}
	if got := s.baselineReadCursor(id, 3); got != 2 {
		t.Fatal("unread lost", got)
	}
}
