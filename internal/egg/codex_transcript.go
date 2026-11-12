package egg

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// The bound thread ID is required before looking at any rollout. No latest-file
// fallback is permitted, and symlinked date directories are never followed.
func openCodexRollout(dir *os.File, id string, depth int, budget *int) (*os.File, error) {
	entries, err := dir.ReadDir(*budget + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	*budget -= len(entries)
	if *budget < 0 {
		return nil, errors.New("Codex session directory exceeds discovery limit")
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, "-"+id+".jsonl") {
			return openProviderPath(dir, name, false)
		}
		length := 2
		if depth == 0 {
			length = 4
		}
		if depth >= 3 || len(name) != length || strings.Trim(name, "0123456789") != "" {
			continue
		}
		child, err := openProviderPath(dir, name, true)
		if err != nil {
			return nil, err
		}
		file, err := openCodexRollout(child, id, depth+1, budget)
		_ = child.Close()
		if file != nil || err != nil {
			return file, err
		}
	}
	return nil, nil
}

// response_item messages are the canonical chat rows. event_msg mirrors are
// deliberately ignored, so a provider writing both formats cannot double them.
// Transcript text does not establish input readiness or turn completion.
func parseCodexTranscriptEvent(event *SessionEvent, line []byte, id string) {
	var record struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Payload   struct {
			ID      string `json:"id"`
			TurnID  string `json:"turn_id"`
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &record) != nil {
		event.Type, event.Reason = "provider_warning", "invalid native transcript record"
		return
	}
	if record.Type == "session_meta" && record.Payload.ID != id {
		event.Type, event.Reason = "provider_warning", "native record belongs to another provider session"
		return
	}
	event.Timestamp = record.Timestamp
	if (record.Type == "turn_context" || record.Type == "event_msg" && record.Payload.Type == "task_started" || record.Type == "response_item") && validLifecycleID(record.Payload.TurnID) {
		event.TurnID = record.Payload.TurnID
	}
	if record.Type != "response_item" || record.Payload.Type != "message" || (record.Payload.Role != "user" && record.Payload.Role != "assistant") {
		return
	}
	var text []string
	for _, block := range record.Payload.Content {
		if block.Type == "input_text" || block.Type == "output_text" {
			text = append(text, block.Text)
		}
	}
	event.Type, event.Role, event.Text = "message", record.Payload.Role, strings.Join(text, "\n")
}
