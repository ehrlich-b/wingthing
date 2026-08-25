package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const DirectChannelPrefix = "control:v1:"

// DirectRequest is one authenticated operation sent over a WebRTC control
// data channel. User identity is deliberately absent: the wing obtains it
// from the coordinator-authenticated signaling exchange.
type DirectRequest struct {
	Version   string          `json:"version"`
	ID        string          `json:"id"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// DirectResponse is the bounded result envelope returned by a wing.
type DirectResponse struct {
	Version string         `json:"version"`
	ID      string         `json:"id"`
	Result  map[string]any `json:"result,omitempty"`
	IsError bool           `json:"is_error,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// SplitWingTarget validates and removes the transport-only wing_id argument.
// The selected wing receives only the operation's original contract.
func SplitWingTarget(arguments json.RawMessage) (string, json.RawMessage, error) {
	trimmed := bytes.TrimSpace(arguments)
	if len(trimmed) == 0 {
		trimmed = []byte(`{}`)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return "", nil, fmt.Errorf("tool arguments must be an object: %w", err)
	}
	raw, ok := values["wing_id"]
	if !ok {
		return "", nil, fmt.Errorf("wing_id is required")
	}
	var wingID string
	if err := json.Unmarshal(raw, &wingID); err != nil || strings.TrimSpace(wingID) == "" {
		return "", nil, fmt.Errorf("wing_id must be a non-empty string")
	}
	delete(values, "wing_id")
	forwarded, err := json.Marshal(values)
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(wingID), forwarded, nil
}

// QualifyResult makes the owning wing part of the result envelope so clients
// never need mutable "current wing" state to interpret an object ID.
func QualifyResult(wingID string, result map[string]any) map[string]any {
	qualified := make(map[string]any, len(result)+1)
	for key, value := range result {
		qualified[key] = value
	}
	qualified["wing_id"] = wingID
	return qualified
}
