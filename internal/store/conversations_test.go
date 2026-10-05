package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func testConversation(id, parent, owner string) Conversation {
	return Conversation{ID: id, ParentID: parent, OwnerID: owner, SessionID: "session-" + id, LaunchKey: "request-" + id, SpecDigest: "digest-" + id, Agent: "claude", CWD: "/fixture", WingID: "wing-fixture", Title: id}
}

func reserveTestConversation(t *testing.T, s *Store, c Conversation) *Conversation {
	t.Helper()
	saved, created, err := s.ReserveConversation(c)
	if err != nil || !created {
		t.Fatalf("reserve %s: created=%v err=%v", c.ID, created, err)
	}
	return saved
}

func TestConversationLineageReplayAndResumePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root := reserveTestConversation(t, s, testConversation("root", "", "owner"))
	child := reserveTestConversation(t, s, testConversation("child", "root", "owner"))
	reserveTestConversation(t, s, testConversation("child2", "root", "owner"))
	if child.RootID != root.ID || child.ParentID != root.ID {
		t.Fatalf("child linkage %#v", child)
	}
	replay := testConversation("replacement", "root", "owner")
	replay.LaunchKey = child.LaunchKey
	replay.SpecDigest = child.SpecDigest
	got, created, err := s.ReserveConversation(replay)
	if err != nil || created || got.ID != child.ID || got.SessionID != child.SessionID {
		t.Fatalf("replay %#v created=%v err=%v", got, created, err)
	}
	replay.SpecDigest = "changed"
	if _, _, err := s.ReserveConversation(replay); err == nil {
		t.Fatal("changed replay accepted")
	}
	if _, _, err := s.ReserveConversation(testConversation("intruder", "root", "other-owner")); err == nil {
		t.Fatal("foreign parent accepted")
	}
	if err := s.ResumeConversationExecution(child.SessionID, "resumed-child"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, execution := range []string{child.SessionID, "resumed-child"} {
		got, err := s.ConversationForSession(execution)
		if err != nil || got.ID != child.ID || got.RootID != root.ID || got.SessionID != "resumed-child" {
			t.Fatalf("reopened execution %s %#v %v", execution, got, err)
		}
	}
}

func TestConversationDeliveryReplayAndCheckpointCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root := reserveTestConversation(t, s, testConversation("root", "", "owner"))
	child := reserveTestConversation(t, s, testConversation("child", "root", "owner"))
	events := []ConversationEvent{{SourceCursor: 1, State: "working", StateSource: "claude_hook", Type: "prompt_submitted"}, {SourceCursor: 2, State: "needs_input", StateSource: "claude_hook", Type: "input_requested"}, {SourceCursor: 3, State: "completed", StateSource: "claude_hook", Type: "turn_completed"}, {SourceCursor: 4, State: "working", StateSource: "claude_hook", Type: "prompt_submitted"}}
	if err := s.ImportConversationStates(child, events, 4); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.ImportConversationStates(child, events, 4); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConversationEvents("owner", root.ID, 0, 100)
	if err != nil || len(got) != 4 {
		t.Fatalf("state replay %v %v", got, err)
	}
	for i, event := range got {
		if event.State != events[i].State || event.SourceCursor != events[i].SourceCursor {
			t.Fatalf("event %d %#v", i, event)
		}
	}
	cursor, err := s.ConversationImportCursor(child.SessionID)
	if err != nil || cursor != 4 {
		t.Fatalf("import cursor %d %v", cursor, err)
	}
	last := got[len(got)-1].Sequence
	if err := s.CheckpointConversation("owner", root.ID, 0, last, "waiting for child"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckpointConversation("owner", root.ID, 0, last, "stale writer"); err == nil {
		t.Fatal("stale revision accepted")
	}
	if err := s.CheckpointConversation("owner", root.ID, 1, last-1, "rewind"); err == nil {
		t.Fatal("rewind accepted")
	}
	if err := s.CheckpointConversation("owner", root.ID, 1, last+1, "ack unseen"); err == nil {
		t.Fatal("unseen delivery acknowledged")
	}
	if err := s.CheckpointConversation("other-owner", root.ID, 1, last, "foreign"); err == nil {
		t.Fatal("foreign checkpoint accepted")
	}
	foreign, err := s.ConversationEvents("other-owner", root.ID, 0, 100)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign events %v %v", foreign, err)
	}
}

func TestConversationConcurrentLaunchReservationIsSingle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	errs := make(chan error, 2)
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			c := testConversation(fmt.Sprintf("candidate-%d", i), "", "owner")
			c.LaunchKey = "same-intent"
			c.SpecDigest = "same-spec"
			_, created, err := s.ReserveConversation(c)
			results <- created
			errs <- err
		}(i, s)
	}
	wg.Wait()
	close(results)
	close(errs)
	count := 0
	for created := range results {
		if created {
			count++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("created %d executions", count)
	}
}

func TestConversationInventoryBoundRejectsNewButAllowsRetry(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	first := testConversation("item-0", "", "owner")
	for i := 0; i < 256; i++ {
		reserveTestConversation(t, s, testConversation(fmt.Sprintf("item-%d", i), "", "owner"))
	}
	if _, _, err := s.ReserveConversation(testConversation("overflow", "", "owner")); err == nil {
		t.Fatal("inventory overflow accepted")
	}
	got, created, err := s.ReserveConversation(first)
	if err != nil || created || got.ID != first.ID {
		t.Fatalf("retry at limit %#v %v %v", got, created, err)
	}
}
