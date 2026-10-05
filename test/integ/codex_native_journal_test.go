//go:build e2e

package integ

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
)

// Protocol fixture, no Codex process/login/model or sandbox-enforcement claim.
func TestCodexNativeJournalReconnectWithoutClaimingAdapterSupport(t *testing.T) {
	dir := t.TempDir()
	thread := "fixture-exact-native-thread"
	frames := []string{
		`{"method":"turn/started","params":{"threadId":"fixture-exact-native-thread","turn":{"id":"turn-1","status":"inProgress"}}}`,
		`{"method":"item/completed","params":{"threadId":"fixture-exact-native-thread","turnId":"turn-1","completedAtMs":1,"item":{"id":"user-1","type":"userMessage","content":[{"type":"text","text":"fixture input"}]}}}`,
		`{"method":"item/commandExecution/requestApproval","id":"approval-1","params":{"threadId":"fixture-exact-native-thread","turnId":"turn-1","itemId":"tool-1","startedAtMs":2,"command":"fixture-never-executed"}}`,
		`{"method":"item/completed","params":{"threadId":"fixture-exact-native-thread","turnId":"turn-1","completedAtMs":3,"item":{"id":"assistant-1","type":"agentMessage","text":"fixture answer"}}}`,
		`{"method":"turn/completed","params":{"threadId":"fixture-exact-native-thread","turn":{"id":"turn-1","status":"completed"}}}`,
	}
	for _, frame := range frames {
		if _, accepted, err := egg.RecordCodexNativeEvent(dir, thread, []byte(frame)); err != nil || !accepted {
			t.Fatalf("record native fixture: %v %v", accepted, err)
		}
	}
	// Reopen the durable journal, replay authoritative item/completion frames.
	for _, frame := range frames[3:] {
		if _, _, err := egg.RecordCodexNativeEvent(dir, thread, []byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	view, err := egg.ReadSessionLifecycle(dir, "codex", "", "", thread, true, 0, 2)
	if err != nil || view.State != "unknown" || view.StateSource != "unsupported" || view.Ready || !view.HasMore || view.Cursor != 2 || view.HeadCursor != 5 {
		t.Fatalf("draft claimed active support or lost durable cursor: %+v %v", view, err)
	}
	reconnected, err := egg.ReadSessionLifecycle(dir, "codex", "", "", thread, true, view.Cursor, 200)
	if err != nil || len(reconnected.Events) != 3 || reconnected.Events[0].State != "needs_input" || reconnected.Events[1].State != "" || reconnected.Events[2].Type != "turn_completed" {
		t.Fatalf("reconnect lifecycle delivery: %+v %v", reconnected, err)
	}
	if !json.Valid(reconnected.Events[0].Raw) || !strings.Contains(string(reconnected.Events[0].Raw), `"id":"approval-1"`) || !strings.Contains(string(reconnected.Events[2].Raw), `"id":"turn-1"`) {
		t.Fatal("lost native request/turn correlation in journal")
	}
}
