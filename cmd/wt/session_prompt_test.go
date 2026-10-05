package main

import (
	"context"
	"encoding/json"
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
	s.principal = "foreign"
	if _, err := s.toolSessionPrompt(context.Background(), json.RawMessage(`{"session":"archived","request_id":"r","input":"x"}`)); err == nil {
		t.Fatal("foreign principal submitted prompt")
	}
	s.principal = "owner"
	s.grants = map[string]bool{"terminal.read": true}
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
	cmd := sessionCmd()
	found, _, err := cmd.Find([]string{"prompt"})
	if err != nil || found.Name() != "prompt" || found.Flags().Lookup("request-id") == nil || found.Flags().Lookup("json") == nil {
		t.Fatal("prompt CLI missing typed retry contract")
	}
}
