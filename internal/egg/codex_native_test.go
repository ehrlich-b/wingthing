package egg

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const codexFixtureThread = "native-thread-exact"

func codexFixture(t *testing.T, method string, params any, requestID any) []byte {
	t.Helper()
	message := map[string]any{"method": method, "params": params}
	if requestID != nil {
		message["id"] = requestID
	}
	wire, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestCodexNativeExactThreadBindingAndCommandPlan(t *testing.T) {
	thread, err := CodexNativeThreadResult([]byte(`{"id":8,"result":{"thread":{"id":"native-thread-exact"}}}`), codexFixtureThread)
	if err != nil || thread != codexFixtureThread {
		t.Fatalf("exact resume response: %q, %v", thread, err)
	}
	for _, wire := range []string{`{"id":8,"result":{"thread":{"id":"another-thread"}}}`, `{"id":8,"result":{"data":[{"id":"native-thread-exact"}]}}`, `{"id":8,"error":{"message":"failed"}}`} {
		if _, err := CodexNativeThreadResult([]byte(wire), codexFixtureThread); err == nil {
			t.Fatalf("accepted mismatched or inventory response: %s", wire)
		}
	}
	server, tui, err := CodexNativeCommandPlan("/tools/codex", "/private/provider", "/private/provider/native.sock", thread)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(server, []string{"/tools/codex", "app-server", "--listen", "unix:///private/provider/native.sock", "--disable", "plugins", "-c", "forced_login_method=chatgpt"}) || !reflect.DeepEqual(tui, []string{"/tools/codex", "--remote", "unix:///private/provider/native.sock", "resume", thread}) {
		t.Fatalf("interactive native argv: %v / %v", server, tui)
	}
	for _, socket := range []string{"/private/native.sock", "/private/provider/../native.sock", "relative.sock", "/private/provider"} {
		if _, _, err := CodexNativeCommandPlan("/tools/codex", "/private/provider", socket, thread); err == nil {
			t.Fatalf("accepted out-of-home socket: %q", socket)
		}
	}
	for _, invalidID := range []string{"--help", "not a native ID", "../outside"} {
		if _, _, err := CodexNativeCommandPlan("/tools/codex", "/private/provider", "/private/provider/native.sock", invalidID); err == nil {
			t.Fatalf("accepted malformed native argv target: %q", invalidID)
		}
	}
}

func TestCodexNativeAssistantItemCannotCompleteTurn(t *testing.T) {
	params := map[string]any{"threadId": codexFixtureThread, "turnId": "turn-1", "completedAtMs": 1, "item": map[string]any{"id": "assistant-1", "type": "agentMessage", "text": "I will inspect a tool next.", "phase": "commentary"}}
	event, ok, err := ParseCodexNativeEvent(codexFixtureThread, codexFixture(t, "item/completed", params, nil))
	if err != nil || !ok || event.Event.State != "" || event.Event.Type != "message" || event.Event.Role != "assistant" || event.TurnID != "turn-1" || event.ItemID != "assistant-1" {
		t.Fatalf("assistant item prematurely completed: %+v %v %v", event, ok, err)
	}
	for _, status := range []string{"completed", "interrupted", "failed"} {
		params = map[string]any{"threadId": codexFixtureThread, "turn": map[string]any{"id": "turn-1", "status": status}}
		event, ok, err = ParseCodexNativeEvent(codexFixtureThread, codexFixture(t, "turn/completed", params, nil))
		if err != nil || !ok || (event.Event.State == "completed") != (status == "completed") {
			t.Fatalf("native turn terminal state %s: %+v %v", status, event, err)
		}
	}
}

func TestCodexNativePromptAndToolEvidence(t *testing.T) {
	params := map[string]any{"threadId": codexFixtureThread, "turnId": "turn-1", "item": map[string]any{"id": "input-1", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "Exact human input"}}}}
	event, ok, err := ParseCodexNativeEvent(codexFixtureThread, codexFixture(t, "item/completed", params, nil))
	if err != nil || !ok || event.Event.Type != "prompt_submitted" || event.Event.Text != "Exact human input" || event.Event.State != "working" {
		t.Fatalf("native user receipt: %+v %v", event, err)
	}
	params["item"] = map[string]any{"id": "tool-1", "type": "mcpToolCall", "server": "wingthing", "tool": "session_read", "status": "completed", "result": map[string]any{"content": "inspectable evidence"}}
	event, ok, err = ParseCodexNativeEvent(codexFixtureThread, codexFixture(t, "item/completed", params, nil))
	if err != nil || !ok || event.Event.State != "" || event.Event.Role != "" || !strings.Contains(string(event.Event.Raw), "inspectable evidence") {
		t.Fatalf("tool evidence treated as prompt/completion: %+v %v", event, err)
	}
}

func TestCodexNativeApprovalRequestNeverProducesDecision(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput", "mcpServer/elicitation/request"} {
		params := map[string]any{"threadId": codexFixtureThread, "turnId": "turn-1", "itemId": "tool-1", "reason": "explicit owner input required"}
		wire := codexFixture(t, method, params, "request-9")
		event, ok, err := ParseCodexNativeEvent(codexFixtureThread, wire)
		if err != nil || !ok || event.Event.State != "needs_input" || string(event.ProviderRequestID) != `"request-9"` || !json.Valid(event.Event.Raw) || !reflect.DeepEqual([]byte(event.Event.Raw), wire) {
			t.Fatalf("approval state/correlation/evidence: %+v %v", event, err)
		}
		if _, _, err := ParseCodexNativeEvent(codexFixtureThread, codexFixture(t, method, params, nil)); err == nil {
			t.Fatal("accepted attention request without RPC identity")
		}
	}
	resolved, ok, err := ParseCodexNativeEvent(codexFixtureThread, codexFixture(t, "serverRequest/resolved", map[string]any{"threadId": codexFixtureThread, "requestId": "request-9"}, nil))
	if err != nil || !ok || resolved.Event.State != "" || !strings.Contains(resolved.Event.Reason, "no approval decision inferred") {
		t.Fatalf("resolution inferred approval or completion: %+v %v", resolved, err)
	}
	nonblocking, ok, err := ParseCodexNativeEvent(codexFixtureThread, codexFixture(t, "item/tool/requestUserInput", map[string]any{"threadId": codexFixtureThread, "turnId": "turn-1", "isBlocking": false}, 10))
	if err != nil || !ok || nonblocking.Event.State != "" {
		t.Fatalf("nonblocking user input paused native state: %+v %v", nonblocking, err)
	}
	for _, requestID := range []string{`null`, `true`, `1.5`, `9223372036854775808`} {
		if validNativeRequestID(json.RawMessage(requestID)) {
			t.Fatalf("accepted malformed native request ID: %s", requestID)
		}
	}
}

func TestCodexNativeRuntimeIdleIsNotInteractiveReady(t *testing.T) {
	for _, flag := range []string{"waitingOnApproval", "waitingOnUserInput"} {
		params := map[string]any{"threadId": codexFixtureThread, "status": map[string]any{"type": "active", "activeFlags": []string{flag}}}
		event, ok, err := ParseCodexNativeEvent(codexFixtureThread, codexFixture(t, "thread/status/changed", params, nil))
		if err != nil || !ok || event.Event.State != "needs_input" || event.Event.Reason != flag {
			t.Fatalf("blocked native status: %+v %v", event, err)
		}
	}
	params := map[string]any{"threadId": codexFixtureThread, "status": map[string]any{"type": "idle"}}
	event, ok, err := ParseCodexNativeEvent(codexFixtureThread, codexFixture(t, "thread/status/changed", params, nil))
	if err != nil || !ok || event.Event.Type == "session_ready" || event.Event.State != "idle" {
		t.Fatalf("claimed TUI readiness from engine idle: %+v %v", event, err)
	}
}

func TestCodexNativeForeignMalformedAndUnknownEvents(t *testing.T) {
	foreign := codexFixture(t, "turn/completed", map[string]any{"threadId": "other-thread", "turn": map[string]any{"id": "turn-1", "status": "completed"}}, nil)
	if _, ok, err := ParseCodexNativeEvent(codexFixtureThread, foreign); err != nil || ok {
		t.Fatalf("foreign event accepted: %v %v", ok, err)
	}
	conflict := codexFixture(t, "thread/started", map[string]any{"threadId": "other-thread", "thread": map[string]any{"id": codexFixtureThread}}, nil)
	if _, _, err := ParseCodexNativeEvent(codexFixtureThread, conflict); err == nil {
		t.Fatal("accepted conflicting outer/inner thread identity")
	}
	for _, wire := range [][]byte{[]byte(`{"method":`), bytesOfSize(maxCodexNativeWire + 1), codexFixture(t, "turn/completed", map[string]any{"threadId": codexFixtureThread, "turn": map[string]any{"status": "completed"}}, nil)} {
		if _, _, err := ParseCodexNativeEvent(codexFixtureThread, wire); err == nil {
			t.Fatal("accepted malformed or unbound native event")
		}
	}
	if _, ok, err := ParseCodexNativeEvent(codexFixtureThread, codexFixture(t, "item/agentMessage/delta", map[string]any{"threadId": codexFixtureThread, "turnId": "turn-1", "itemId": "item-1", "delta": "partial"}, nil)); err != nil || ok {
		t.Fatalf("delta accepted without durable replay position: %v %v", ok, err)
	}
	if _, ok, err := ParseCodexNativeEvent(codexFixtureThread, []byte(`{"method":"future/event","params":{"status":"a different shape"}}`)); err != nil || ok {
		t.Fatalf("unknown methods must ignore unknown params schema: %v %v", ok, err)
	}
}

func bytesOfSize(n int) []byte { return []byte(strings.Repeat("x", n)) }
