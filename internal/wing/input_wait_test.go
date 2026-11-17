package wing

import (
	"testing"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestSweepInputWaitsNotifiesOnceAndClears(t *testing.T) {
	const id = "input-wait-sweep-test"
	forgetAttentionState(id)
	defer forgetAttentionState(id)
	var sent []ws.SessionAttention
	write := func(v any) error {
		if attention, ok := v.(ws.SessionAttention); ok {
			sent = append(sent, attention)
		}
		return nil
	}
	sessions := []ws.SessionInfo{{SessionID: id, Agent: "claude", CWD: "/work/egg"}}
	waiting := true
	flagged := make(map[string]bool)
	sweep := func() { sweepInputWaits(sessions, func(string) bool { return waiting }, write, flagged) }

	sweep()
	if len(sent) != 1 || sent[0].SessionID != id || sent[0].Agent != "claude" || sent[0].Nonce == "" {
		t.Fatalf("first sweep sent %+v", sent)
	}
	if _, ok := wingAttention.Load(id); !ok {
		t.Fatal("session lists should show the session needs attention")
	}
	sweep()
	if len(sent) != 1 {
		t.Fatalf("a second sweep inside the cooldown re-sent: %+v", sent)
	}

	waiting = false
	sweep()
	if _, ok := wingAttention.Load(id); ok || flagged[id] {
		t.Fatal("attention should clear once the prompt is gone")
	}

	waiting = true
	flagged[id] = true
	sweepInputWaits(nil, func(string) bool { return true }, write, flagged)
	if flagged[id] {
		t.Fatal("a session that is no longer alive should be forgotten")
	}
}
