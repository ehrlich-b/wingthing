package store

import (
	"strings"
	"testing"
	"time"
)

func createTestMessage(t *testing.T, s *Store, owner, sender, recipient, id, content string) *Message {
	t.Helper()
	message := &Message{
		MessageID: id, OwnerID: owner, SenderActor: sender,
		RecipientActor: recipient, Channel: "factory", Kind: "message",
		Content: content, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.CreateMessage(message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestMessagesAreOwnerAndActorScoped(t *testing.T) {
	s := openTestStore(t)
	broadcast := createTestMessage(t, s, "owner-a", "codex", "", "m-a1", "broadcast")
	directed := createTestMessage(t, s, "owner-a", "codex", "claude", "m-a2", "for claude")
	createTestMessage(t, s, "owner-b", "codex", "", "m-b1", "other owner")

	claude, err := s.ListMessages("owner-a", "claude", "factory", "", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(claude) != 2 || claude[0].MessageID != broadcast.MessageID || claude[1].MessageID != directed.MessageID {
		t.Fatalf("claude messages = %#v", claude)
	}

	terra, err := s.ListMessages("owner-a", "terra", "factory", "", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(terra) != 1 || terra[0].MessageID != broadcast.MessageID {
		t.Fatalf("terra messages = %#v", terra)
	}

	codex, err := s.ListMessages("owner-a", "codex", "factory", "", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(codex) != 0 {
		t.Fatalf("sender received its own messages: %#v", codex)
	}

	withSent, err := s.ListMessages("owner-a", "codex", "factory", "", 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withSent) != 2 {
		t.Fatalf("include-sent messages = %#v", withSent)
	}
}

func TestMessageCursorAndReplyStayWithinOwner(t *testing.T) {
	s := openTestStore(t)
	first := createTestMessage(t, s, "owner-a", "codex", "", "m-a1", "first")
	second := createTestMessage(t, s, "owner-a", "codex", "", "m-a2", "second")

	messages, err := s.ListMessages("owner-a", "claude", "factory", first.MessageID, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].MessageID != second.MessageID {
		t.Fatalf("messages after cursor = %#v", messages)
	}

	if _, err := s.ListMessages("owner-b", "claude", "factory", first.MessageID, 50, false); err == nil ||
		!strings.Contains(err.Error(), "not found or not owned") {
		t.Fatalf("cross-owner cursor error = %v", err)
	}

	reply := &Message{
		MessageID: "m-b-reply", OwnerID: "owner-b", SenderActor: "claude",
		Channel: "factory", Kind: "answer", ReplyTo: first.MessageID,
		Content: "cross owner", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.CreateMessage(reply); err == nil || !strings.Contains(err.Error(), "not found or not owned") {
		t.Fatalf("cross-owner reply error = %v", err)
	}
}

func TestExpiredMessagesDisappearAndReleaseReplies(t *testing.T) {
	s := openTestStore(t)
	message := &Message{
		MessageID: "m-expired", OwnerID: "owner-a", SenderActor: "codex",
		Channel: "factory", Content: "expired",
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := s.CreateMessage(message); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeExpiredMessages(); err != nil {
		t.Fatal(err)
	}
	messages, err := s.ListMessages("owner-a", "claude", "factory", "", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("expired messages = %#v", messages)
	}
	got, err := s.GetMessage("owner-a", message.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expired message retained: %#v", got)
	}
}
