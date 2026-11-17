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
	raised := make(map[string]string)
	sweep := func(r probeResult) {
		sweepInputWaits(sessions, map[string]probeResult{id: r}, write, raised)
	}

	sweep(probeResult{waiting: true, ok: true})
	if len(sent) != 1 || sent[0].SessionID != id || sent[0].Agent != "claude" || sent[0].Nonce == "" {
		t.Fatalf("first sweep sent %+v", sent)
	}
	if _, ok := wingAttention.Load(id); !ok {
		t.Fatal("session lists should show the session needs attention")
	}
	sweep(probeResult{waiting: true, ok: true})
	if len(sent) != 1 {
		t.Fatalf("a second sweep inside the cooldown re-sent: %+v", sent)
	}

	sweep(probeResult{ok: false})
	if _, ok := wingAttention.Load(id); !ok {
		t.Fatal("a failed screen read must not clear attention")
	}

	sweep(probeResult{waiting: false, ok: true})
	if _, ok := wingAttention.Load(id); ok || raised[id] != "" {
		t.Fatal("attention should clear once the prompt is gone")
	}

	raised[id] = "gone-session-nonce"
	sweepInputWaits(nil, nil, write, raised)
	if _, ok := raised[id]; ok {
		t.Fatal("a session that is no longer alive should be forgotten")
	}
}

// The browser bell path shares the attention state. A bell raised after the
// sweep's episode ended has a new nonce, and the sweep must leave it alone.
func TestSweepInputWaitsLeavesNewerBellAttention(t *testing.T) {
	const id = "input-wait-bell-test"
	forgetAttentionState(id)
	defer forgetAttentionState(id)
	write := func(any) error { return nil }
	sessions := []ws.SessionInfo{{SessionID: id}}
	raised := make(map[string]string)
	sweepInputWaits(sessions, map[string]probeResult{id: {waiting: true, ok: true}}, write, raised)

	clearAttentionCooldown(id) // the user answered in a browser
	wingAttentionCooldown.Delete(id)
	if !checkAndSendAttention(id, "", "", write) { // then a new bell rang
		t.Fatal("bell attention was not raised")
	}
	bellNonce, _ := wingAttentionNonce.Load(id)

	sweepInputWaits(sessions, map[string]probeResult{id: {waiting: false, ok: true}}, write, raised)
	if _, ok := wingAttention.Load(id); !ok {
		t.Fatal("the sweep cleared a bell's attention")
	}
	if nonce, _ := wingAttentionNonce.Load(id); nonce != bellNonce {
		t.Fatal("the sweep replaced the bell's nonce")
	}
}

// A bell that already holds attention when the sweep first sees the prompt
// is not the sweep's to send again or to clear.
func TestSweepInputWaitsNeverAdoptsABellEpisode(t *testing.T) {
	const id = "input-wait-adopt-test"
	forgetAttentionState(id)
	defer forgetAttentionState(id)
	sends := 0
	write := func(any) error { sends++; return nil }
	if !checkAndSendAttention(id, "", "", write) {
		t.Fatal("bell attention was not raised")
	}
	bellNonce, _ := wingAttentionNonce.Load(id)
	sessions := []ws.SessionInfo{{SessionID: id}}
	raised := make(map[string]string)

	sweepInputWaits(sessions, map[string]probeResult{id: {waiting: true, ok: true}}, write, raised)
	if sends != 1 || raised[id] != "" {
		t.Fatalf("the sweep adopted the bell episode: sends=%d raised=%q", sends, raised[id])
	}
	sweepInputWaits(sessions, map[string]probeResult{id: {waiting: false, ok: true}}, write, raised)
	if nonce, ok := wingAttentionNonce.Load(id); !ok || nonce != bellNonce {
		t.Fatal("the sweep cleared a bell episode it never raised")
	}
}

func TestProbeAllCollectsEveryResult(t *testing.T) {
	sessions := []ws.SessionInfo{{SessionID: "a"}, {SessionID: "b"}, {SessionID: "c"}, {SessionID: "d"}, {SessionID: "e"}}
	results := probeAll(sessions, func(id string) (bool, bool) { return id == "c", id != "e" })
	if len(results) != len(sessions) || !results["c"].waiting || results["a"].waiting || results["e"].ok {
		t.Fatalf("results = %+v", results)
	}
}
