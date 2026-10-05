package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestBrowserConversationLaunchEchoesSavedRequestID(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	canonical, err := filepath.EvalSymlinks(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Dir = canonical
	const requestID = "phone-launch-1"
	server := &localMCPServer{cfg: cfg, principal: roostSessionPrincipal("phone-owner")}
	// Reserve the normalized handler spec so this test only reads a saved
	// launch. It never starts a provider process or touches a live session.
	spec := struct {
		Agent                string   `json:"agent"`
		Model                string   `json:"model"`
		CWD                  string   `json:"cwd"`
		Label                string   `json:"label"`
		Unattended           bool     `json:"unattended"`
		Args                 []string `json:"args"`
		ConversationRole     string   `json:"conversation_role"`
		ParentConversationID string   `json:"parent_conversation_id"`
		RequestID            string   `json:"request_id"`
	}{Agent: "claude", CWD: cfg.Dir, Label: "phone-parent", ConversationRole: "parent"}
	conversation, _, err := server.reserveAgentConversation("claude", cfg.Dir, "phone-parent", "parent", "", requestID, "phone-session", spec)
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]any{"agent": "claude", "cwd": cfg.Dir, "label": "phone-parent", "conversation_role": "parent", "request_id": requestID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := browserSessionControl(context.Background(), cfg, &config.WingConfig{}, ws.TunnelRequest{SenderUserID: "phone-owner", SenderOrgRole: "owner"}, "agent_start", arguments, cfg.Dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if result["request_id"] != requestID || result["session"] != "phone-session" || result["conversation_id"] != conversation.ID || result["root_conversation_id"] != conversation.ID || result["reused"] != true {
		t.Fatalf("saved launch reply lost its encrypted correlation: %#v", result)
	}
}
