package store

import (
	"path/filepath"
	"testing"
)

func TestConversationWakeOutboxRestartAndSeparateAcknowledgement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wake.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root := reserveTestConversation(t, db, testConversation("root", "", "owner"))
	child := reserveTestConversation(t, db, testConversation("child", root.ID, "owner"))
	if err = db.SetConversationWake("other", root.ID, true); err == nil {
		t.Fatal("wrong owner enabled wake")
	}
	if err = db.SetConversationWake("owner", child.ID, true); err == nil {
		t.Fatal("child enabled root policy")
	}
	if err = db.SetConversationWake("owner", root.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = db.ImportConversationStates(child, []ConversationEvent{{SourceCursor: 1, State: "working", StateSource: "claude_hook"}, {SourceCursor: 2, State: "needs_input", StateSource: "claude_hook"}, {SourceCursor: 3, State: "completed", StateSource: "claude_hook"}}, 3); err != nil {
		t.Fatal(err)
	}
	w, err := db.QueueConversationWake(root.ID)
	if err != nil || w == nil || w.Event.State != "needs_input" {
		t.Fatalf("queue %#v %v", w, err)
	}
	if err = db.BindConversationWake(w, "parent-old", "native-old", "host observation", 10); err != nil {
		t.Fatal(err)
	}
	request := w.RequestID
	if err = db.RecordConversationWake(w, "unconfirmed", "unknown", 0, 10); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w, err = db.QueueConversationWake(root.ID)
	if err != nil || w.RequestID != request || w.SessionID != "parent-old" {
		t.Fatalf("recovered %#v %v", w, err)
	}
	if err = db.BindConversationWake(w, "parent-new", "native-new", "changed", 20); err == nil {
		t.Fatal("redirected unknown wake")
	}
	if err = db.RecordConversationWake(w, "observed", "native receipt", 9, 20); err != nil {
		t.Fatal(err)
	}
	p, err := db.ConversationWakePolicy(root.ID)
	if err != nil || p.DeliveryCursor != w.EventSequence {
		t.Fatalf("policy %#v %v", p, err)
	}
	root, err = db.GetConversation("owner", root.ID)
	if err != nil || root.DeliveredCursor != 0 || root.Revision != 0 {
		t.Fatalf("wake acknowledged checkpoint %#v %v", root, err)
	}
	next, err := db.QueueConversationWake(root.ID)
	if err != nil || next.Event.State != "completed" || next.EventSequence <= w.EventSequence {
		t.Fatalf("next %#v %v", next, err)
	}
}

func TestConversationWakeKnownNotSentRetriesAreBounded(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "wake.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := reserveTestConversation(t, db, testConversation("root", "", "owner"))
	child := reserveTestConversation(t, db, testConversation("child", root.ID, "owner"))
	_ = db.SetConversationWake("owner", root.ID, true)
	_ = db.RecordConversationState(child, 1, "completed", "claude_hook")
	previous := ""
	for attempt := 1; attempt <= 3; attempt++ {
		w, err := db.QueueConversationWake(root.ID)
		if err != nil {
			t.Fatal(err)
		}
		now := int64(attempt * 10)
		if err = db.BindConversationWake(w, "parent", "native", "host observation", now); err != nil {
			t.Fatal(err)
		}
		if w.RequestID == previous {
			t.Fatal("new attempt reused rejected identity")
		}
		previous = w.RequestID
		if err = db.RecordConversationWake(w, "not_sent", "known no input", 0, now); err != nil {
			t.Fatal(err)
		}
		w, err = db.PendingConversationWake(root.ID)
		if err != nil {
			t.Fatal(err)
		}
		if attempt < 3 && db.BindConversationWake(w, "parent", "native", "host observation", now+1) == nil {
			t.Fatal("cooldown bypass")
		}
	}
	w, err := db.PendingConversationWake(root.ID)
	if err != nil || w.Status != "blocked" || w.Attempt != 3 {
		t.Fatalf("bounded %#v %v", w, err)
	}
	if err = db.BindConversationWake(w, "parent", "native", "host observation", 100); err == nil {
		t.Fatal("retry cap bypass")
	}
}

func TestConversationWakeRuntimeSourceAndTypeAreExact(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "wake.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := reserveTestConversation(t, db, testConversation("root", "", "owner"))
	child := reserveTestConversation(t, db, testConversation("child", root.ID, "owner"))
	if err = db.SetConversationWake("owner", root.ID, true); err != nil {
		t.Fatal(err)
	}
	events := []ConversationEvent{
		{SourceCursor: 1, State: "completed", StateSource: "pty_screen", Type: "message"},
		{SourceCursor: 2, State: "completed", StateSource: "egg_process", Type: "session_exit"},
		{SourceCursor: 3, State: "failed", StateSource: "egg_process", Type: "message"},
		{SourceCursor: 4, State: "failed", StateSource: "egg_process", Type: "session_failed"},
	}
	if err = db.ImportConversationStates(child, events, 4); err != nil {
		t.Fatal(err)
	}
	w, err := db.QueueConversationWake(root.ID)
	if err != nil || w == nil || w.Event.SourceCursor != 4 {
		t.Fatalf("runtime source %#v %v", w, err)
	}
}

func TestConversationWakeExplicitRetryPreservesMonotonicIDsAndTotalCap(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "wake.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := reserveTestConversation(t, db, testConversation("root", "", "owner"))
	child := reserveTestConversation(t, db, testConversation("child", root.ID, "owner"))
	if err = db.SetConversationWake("owner", root.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordConversationState(child, 1, "completed", "claude_hook"); err != nil {
		t.Fatal(err)
	}
	requests := map[string]bool{}
	for attempt := 1; attempt <= MaxConversationWakeAttempts; attempt++ {
		w, err := db.QueueConversationWake(root.ID)
		if err != nil {
			t.Fatal(err)
		}
		now := int64(attempt * 10)
		if err = db.BindConversationWake(w, "parent", "native", "host observation", now); err != nil {
			t.Fatal(err)
		}
		if requests[w.RequestID] || w.Attempt != attempt {
			t.Fatalf("attempt identity reused %#v", w)
		}
		requests[w.RequestID] = true
		if err = db.RetryNotSentConversationWake("owner", root.ID, now); err == nil {
			t.Fatal("retried ambiguous pending")
		}
		if err = db.RecordConversationWake(w, "not_sent", "explicit proof", 0, now); err != nil {
			t.Fatal(err)
		}
		if attempt%3 == 0 {
			w, _ = db.PendingConversationWake(root.ID)
			if w.Status != "blocked" {
				t.Fatalf("automated limit %#v", w)
			}
			if err = db.RetryNotSentConversationWake("other", root.ID, now); err == nil {
				t.Fatal("wrong owner retried")
			}
			err = db.RetryNotSentConversationWake("owner", root.ID, now)
			if attempt < MaxConversationWakeAttempts {
				if err != nil {
					t.Fatal(err)
				}
				next, _ := db.PendingConversationWake(root.ID)
				if next.Attempt != attempt || next.AttemptLimit != attempt+3 || next.RequestID != w.RequestID || next.Status != "not_sent" {
					t.Fatalf("reset identity %#v", next)
				}
				if db.BindConversationWake(next, "parent", "native", "host observation", now+1) == nil {
					t.Fatal("manual retry bypassed cooldown")
				}
			} else if err == nil {
				t.Fatal("manual retry exceeded total cap")
			}
		}
	}
	c, _ := db.GetConversation("owner", root.ID)
	if c.Revision != 0 || c.DeliveredCursor != 0 {
		t.Fatalf("retry reset checkpoint %#v", c)
	}
}

func TestConversationWakeExplicitRetryRejectsUnknown(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "wake.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := reserveTestConversation(t, db, testConversation("root", "", "owner"))
	child := reserveTestConversation(t, db, testConversation("child", root.ID, "owner"))
	_ = db.SetConversationWake("owner", root.ID, true)
	_ = db.RecordConversationState(child, 1, "completed", "claude_hook")
	w, err := db.QueueConversationWake(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.RetryNotSentConversationWake("owner", root.ID, 10); err == nil {
		t.Fatal("retried queued wake without proof")
	}
	if err = db.BindConversationWake(w, "parent", "native", "host observation", 10); err != nil {
		t.Fatal(err)
	}
	if err = db.RecordConversationWake(w, "unconfirmed", "unknown", 0, 10); err != nil {
		t.Fatal(err)
	}
	if err = db.RetryNotSentConversationWake("owner", root.ID, 20); err == nil {
		t.Fatal("retried unknown wake")
	}
	got, _ := db.PendingConversationWake(root.ID)
	if got.RequestID != w.RequestID || got.AttemptLimit != 3 || got.Status != "unconfirmed" {
		t.Fatalf("changed unknown %#v", got)
	}
}
