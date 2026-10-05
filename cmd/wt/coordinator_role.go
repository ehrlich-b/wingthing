package main

import (
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/store"
)

const coordinatorRoleVersion = "wingthing.personal-coordinator.v1"

// This is an original public product role, not an agent's private system prompt.
//
//go:embed prompts/personal_coordinator.md
var coordinatorRoleTemplate string

// coordinatorContext reports local identity facts. The absent home-roost and
// epoch fields deliberately do not stand in for cross-host adoption or fencing.
type coordinatorContext struct {
	Version                    string  `json:"version"`
	DotID                      string  `json:"dot_id"`
	TaskID                     string  `json:"task_id"`
	ParentTaskID               string  `json:"parent_task_id,omitempty"`
	ExecutorID                 string  `json:"executor_id"`
	ExecutorIdentityState      string  `json:"executor_identity_state"`
	SessionID                  string  `json:"session_id"`
	Workspace                  string  `json:"workspace"`
	IdentityScope              string  `json:"identity_scope"`
	HomeRoostID                *string `json:"home_roost_id"`
	HomeRoostIdentityState     string  `json:"home_roost_identity_state"`
	OwnerEpoch                 *uint64 `json:"owner_epoch"`
	OwnerEpochState            string  `json:"owner_epoch_state"`
	CrossHostAdoptionSupported bool    `json:"cross_host_adoption_supported"`
}

func contextForConversation(c *store.Conversation) coordinatorContext {
	result := coordinatorContext{
		Version: coordinatorRoleVersion, IdentityScope: "local_wing",
		ExecutorIdentityState: "unknown", HomeRoostIdentityState: "not_persisted",
		OwnerEpochState: "unsupported",
	}
	if c == nil {
		return result
	}
	result.DotID = c.RootID
	if result.DotID == "" && c.ParentID == "" {
		result.DotID = c.ID
	}
	result.TaskID, result.ParentTaskID = c.ID, c.ParentID
	result.ExecutorID, result.SessionID, result.Workspace = c.WingID, c.SessionID, c.CWD
	if c.WingID != "" {
		result.ExecutorIdentityState = "known"
	}
	return result
}

func publicCoordinatorPrompt(c *store.Conversation) string {
	// The fixed structure contains only JSON-supported types. JSON escaping keeps
	// workspace/label text as data rather than interpreting it as template source.
	facts, _ := json.MarshalIndent(contextForConversation(c), "", "  ")
	return strings.ReplaceAll(coordinatorRoleTemplate, "{{RUNTIME_FACTS}}", string(facts))
}
