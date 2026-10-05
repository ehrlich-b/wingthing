package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestConversationContinuationConcurrentReservationPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "continuations.db")
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
	root := reserveTestConversation(t, a, testConversation("root", "", "owner"))
	child := reserveTestConversation(t, a, testConversation("child", root.ID, "owner"))
	var wg sync.WaitGroup
	results := make(chan *ConversationContinuation, 2)
	errs := make(chan error, 2)
	createdCount := make(chan bool, 2)
	for i, db := range []*Store{a, b} {
		wg.Add(1)
		go func(i int, db *Store) {
			defer wg.Done()
			turn := ConversationContinuation{OwnerID: "owner", RequestID: "follow-up", ConversationID: root.ID, SourceSession: root.SessionID, SessionID: fmt.Sprintf("candidate-%d", i), ProviderSessionID: "provider", InputSHA256: "hash", SpecDigest: "same-intent"}
			saved, created, err := db.ReserveConversationContinuation(turn)
			results <- saved
			createdCount <- created
			errs <- err
		}(i, db)
	}
	wg.Wait()
	first, second := <-results, <-results
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if first.SessionID != second.SessionID || first.LaunchState != "starting" || (<-createdCount) == (<-createdCount) {
		t.Fatalf("duplicate continuation: %v %v", first, second)
	}
	starting, err := a.GetConversation("owner", root.ID)
	if err != nil || starting.SessionID != first.SessionID || starting.LaunchState != "starting" {
		t.Fatalf("root still names source before dispatch %v %v", starting, err)
	}
	if err := a.SetConversationContinuationLaunch(first, "started", ""); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.GetConversationContinuation("owner", "follow-up")
	if err != nil || got.SessionID != first.SessionID || got.LaunchState != "started" {
		t.Fatalf("durable acknowledgement %v %v", got, err)
	}
	for _, session := range []string{root.SessionID, got.SessionID} {
		linked, err := reopened.ConversationForSession(session)
		if err != nil || linked.ID != root.ID || linked.RootID != root.ID || linked.SessionID != got.SessionID {
			t.Fatalf("tree changed %v %v", linked, err)
		}
	}
	linkedChild, err := reopened.GetConversation("owner", child.ID)
	if err != nil || linkedChild.ParentID != root.ID || linkedChild.RootID != root.ID {
		t.Fatalf("child moved %v %v", linkedChild, err)
	}
	changed := *got
	changed.SpecDigest = "other-intent"
	if _, _, err := reopened.ReserveConversationContinuation(changed); err == nil {
		t.Fatal("different intent reused request_id")
	}
	initial := testConversation("other", "", "owner")
	initial.LaunchKey = got.RequestID
	if _, _, err := reopened.ReserveConversation(initial); err == nil {
		t.Fatal("initial launch reused continuation request_id")
	}
	initial = testConversation("another-root", "", "owner")
	reserveTestConversation(t, reopened, initial)
	changed.RequestID, changed.SpecDigest, changed.SourceSession, changed.SessionID = initial.LaunchKey, "another", got.SessionID, "new"
	if _, _, err := reopened.ReserveConversationContinuation(changed); err == nil {
		t.Fatal("continuation reused initial launch request_id")
	}
}

func TestConversationContinuationFinalizationPreservesNewerExecution(t *testing.T) {
	for _, state := range []string{"started", "failed"} {
		t.Run(state, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "continuations.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			root := reserveTestConversation(t, db, testConversation("root", "", "owner"))
			turn, _, err := db.ReserveConversationContinuation(ConversationContinuation{OwnerID: "owner", RequestID: "follow-up", ConversationID: root.ID, SourceSession: root.SessionID, SessionID: "target", ProviderSessionID: "provider", InputSHA256: "hash", SpecDigest: "intent"})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.ResumeConversationExecution(turn.SessionID, "newer"); err != nil {
				t.Fatal(err)
			}
			if err := db.SetConversationContinuationLaunch(turn, state, ""); err != nil {
				t.Fatal(err)
			}
			current, err := db.GetConversation("owner", root.ID)
			if err != nil || current.SessionID != "newer" {
				t.Fatalf("overwrote newer execution %v %v", current, err)
			}
			saved, err := db.GetConversationContinuation("owner", turn.RequestID)
			if err != nil || saved.LaunchState != state || saved.SessionID != "target" {
				t.Fatalf("acknowledgement changed %v %v", saved, err)
			}
		})
	}
}
