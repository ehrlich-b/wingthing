package localmcp

import (
	"context"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"os"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/control"
)

func TestSessionPromptStrictSchemaAndOwnerChecks(t *testing.T) {
	s, _ := lifecycleMCPFixture(t)
	for _, args := range []string{
		`{"session":"archived","request_id":"r","input":"x","unexpected":true}`,
		`{"session":"archived","request_id":"r","input":"x","timeout_seconds":61}`,
		`{"session":"archived","request_id":"r","input":"x","timeout_seconds":-1}`,
		`{"session":"archived","request_id":"","input":"x"}`,
		`{"session":"archived","request_id":"r","input":"x\u001b[31m"}`,
	} {
		_, isError, protocolErr := s.callTool(context.Background(), "session_prompt", json.RawMessage(args))
		if protocolErr != nil || !isError {
			t.Fatalf("invalid arguments allowed: %s", args)
		}
	}
	s.Principal = "foreign"
	if _, err := s.toolSessionPrompt(context.Background(), json.RawMessage(`{"session":"archived","request_id":"r","input":"x"}`)); err == nil {
		t.Fatal("foreign principal submitted prompt")
	}
	s.Principal = "owner"
	s.Grants = map[string]bool{"terminal.read": true}
	_, isError, _ := s.callTool(context.Background(), "session_prompt", json.RawMessage(`{"session":"archived","request_id":"r","input":"x"}`))
	if !isError {
		t.Fatal("read grant allowed prompt send")
	}
	tool, ok := control.Lookup("session_prompt")
	if !ok || tool.Grant != "terminal.send" || tool.Annotations["readOnlyHint"] != false {
		t.Fatalf("prompt authority missing: %+v", tool)
	}
	if !strings.Contains(tool.Description, "request-specific") {
		t.Fatal("receipt source limitation missing from discovery")
	}
}

func TestSessionPromptReceiptOnlyReadKeepsOwnershipAndCreatesNothing(t *testing.T) {
	s, dir := lifecycleMCPFixture(t)
	args := json.RawMessage(`{"session":"archived","original_request":{"request_id":"missing-answer","provider_session_id":"ours","answer_to_state_cursor":1}}`)
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.toolSessionRead(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := result["receipt"].(egg.SessionPromptResult)
	if !ok || receipt.Status != "unconfirmed" || receipt.TransportEnqueued || receipt.DefinitelyNotSent || receipt.ProviderRequestAcknowledged {
		t.Fatalf("missing lookup claimed delivery: %#v", result)
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("receipt-only read created a reservation or transport")
	}
	s.Principal = "foreign"
	if _, err := s.toolSessionRead(t.Context(), args); err == nil {
		t.Fatal("foreign principal inspected original receipt")
	}
	s.Principal = "owner"
	s.enforcePathBounds = true
	s.allowedPaths = []string{"/other"}
	if _, err := s.toolSessionRead(t.Context(), args); err == nil {
		t.Fatal("receipt lookup escaped workspace")
	}
}
