package ws

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewMessage(t *testing.T) {
	before := time.Now().UnixMilli()
	msg, err := NewMessage(MsgTaskSubmit, TaskSubmitPayload{What: "test task"})
	after := time.Now().UnixMilli()

	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if msg.Type != MsgTaskSubmit {
		t.Errorf("Type = %q, want %q", msg.Type, MsgTaskSubmit)
	}
	if _, err := uuid.Parse(msg.ID); err != nil {
		t.Errorf("ID %q is not a valid UUID: %v", msg.ID, err)
	}
	if msg.Timestamp < before || msg.Timestamp > after {
		t.Errorf("Timestamp %d not in [%d, %d]", msg.Timestamp, before, after)
	}
}

func TestMessageRoundTrip(t *testing.T) {
	orig, err := NewMessage(MsgTaskSubmit, TaskSubmitPayload{
		What:  "deploy",
		Type:  "shell",
		Agent: "default",
		RunAt: "2026-02-07T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	orig.ReplyTo = "some-correlation-id"

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.Type != orig.Type {
		t.Errorf("Type = %q, want %q", decoded.Type, orig.Type)
	}
	if decoded.ID != orig.ID {
		t.Errorf("ID = %q, want %q", decoded.ID, orig.ID)
	}
	if decoded.ReplyTo != orig.ReplyTo {
		t.Errorf("ReplyTo = %q, want %q", decoded.ReplyTo, orig.ReplyTo)
	}
	if decoded.Timestamp != orig.Timestamp {
		t.Errorf("Timestamp = %d, want %d", decoded.Timestamp, orig.Timestamp)
	}
	if string(decoded.Payload) != string(orig.Payload) {
		t.Errorf("Payload = %s, want %s", decoded.Payload, orig.Payload)
	}
}

func TestParsePayload(t *testing.T) {
	msg, err := NewMessage(MsgTaskSubmit, TaskSubmitPayload{
		What:  "run tests",
		Type:  "shell",
		Agent: "ci",
	})
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}

	var p TaskSubmitPayload
	if err := msg.ParsePayload(&p); err != nil {
		t.Fatalf("ParsePayload: %v", err)
	}
	if p.What != "run tests" {
		t.Errorf("What = %q, want %q", p.What, "run tests")
	}
	if p.Type != "shell" {
		t.Errorf("Type = %q, want %q", p.Type, "shell")
	}
	if p.Agent != "ci" {
		t.Errorf("Agent = %q, want %q", p.Agent, "ci")
	}
}

func TestAllMessageTypes(t *testing.T) {
	types := []MsgType{
		MsgTaskSubmit, MsgTaskResult, MsgTaskStatus,
		MsgSyncRequest, MsgSyncResponse, MsgStatus,
		MsgPing, MsgPong, MsgAuth, MsgAuthResult, MsgError,
	}

	for _, mt := range types {
		msg, err := NewMessage(mt, nil)
		if err != nil {
			t.Fatalf("NewMessage(%q): %v", mt, err)
		}
		if msg.Type != mt {
			t.Errorf("Type = %q, want %q", msg.Type, mt)
		}
	}
}
