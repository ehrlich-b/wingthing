package egg

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

// Read the exact transcript directly: lifecycle pages intentionally truncate
// records and cannot supply a complete run result. Only newline-terminated
// native records advance the offset, including when Stop precedes the flush.
func claudeRunScanner(home, cwd, providerID, prompt string, read func(context.Context, int64, int) (SessionView, error)) (func() (turnEvidence, error), error) {
	open := func() (io.ReadCloser, error) {
		root, err := openProviderHome(home)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		file, _, err := openAgentSession(root, "claude", cwd, Profile("claude").SessionDir, time.Time{}, providerID)
		if file == nil {
			return nil, err
		}
		return file, err
	}
	// Reserve the native byte boundary before sending, so old identical prompts
	// and old Stop hooks cannot become this run's evidence.
	var offset int64
	f, err := open()
	if err != nil {
		return nil, err
	}
	if f != nil {
		offset, err = io.Copy(io.Discard, f)
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	view, err := read(context.Background(), 0, 1)
	if err != nil {
		return nil, err
	}
	reserved := view.HeadCursor
	var evidence turnEvidence
	var messageID string
	var lastText string
	return func() (turnEvidence, error) {
		f, err := open()
		if err != nil {
			return evidence, err
		}
		if f == nil {
			return evidence, nil
		}
		defer f.Close()
		if _, err = io.CopyN(io.Discard, f, offset); err != nil {
			return evidence, errors.New("exact native transcript was truncated")
		}
		r := bufio.NewReader(f)
		flushed := true
		for {
			line, err := r.ReadBytes('\n')
			if err == io.EOF {
				flushed = len(line) == 0
				break
			}
			if err != nil {
				return evidence, err
			}
			offset += int64(len(line))
			var record struct {
				Type      string `json:"type"`
				SessionID string `json:"sessionId"`
				Sidechain bool   `json:"isSidechain"`
				Message   struct {
					ID         string          `json:"id"`
					Role       string          `json:"role"`
					Content    json.RawMessage `json:"content"`
					StopReason string          `json:"stop_reason"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &record) != nil {
				return evidence, errors.New("invalid exact native transcript record")
			}
			if record.SessionID != providerID || record.Sidechain {
				continue
			}
			if kind, failed := agent.ClassifyProviderFailure("claude", line); failed {
				evidence.Failure = kind
				continue
			}
			if record.Type == "user" {
				event := SessionEvent{Source: "claude_transcript", ProviderSessionID: providerID, Type: "message", Raw: line}
				// Tool results, summaries and synthetic records aren't human input.
				if !nativeHumanPromptReceipt(event, providerID, lifecycleMessageText(record.Message.Content)) {
					continue
				}
				if evidence.Receipt || !nativeHumanPromptReceipt(event, providerID, prompt) {
					evidence.Conflict = true
				} else {
					evidence.Receipt = true
				}
			}
			if evidence.Receipt && record.Type == "assistant" && record.Message.Role == "assistant" {
				text := lifecycleMessageText(record.Message.Content)
				if record.Message.ID != "" && messageID != record.Message.ID {
					messageID, evidence.Text = record.Message.ID, ""
				}
				if text != "" {
					evidence.Text += text
					lastText = text
				}
				if record.Message.StopReason == "end_turn" {
					evidence.Complete = true
				}
			}
		}
		// Stop alone cannot prove that a delayed final message has flushed.
		// Its native last_assistant_message must match the receipt-scoped text.
		cursor := reserved
		background := false
		for {
			view, err := read(context.Background(), cursor, 200)
			if err != nil {
				return evidence, err
			}
			if view.ProviderSessionID != providerID {
				evidence.Conflict = true
			}
			for _, event := range view.Events {
				if event.Source != "claude_hook" {
					continue
				}
				data, err := readRunHook(home, "claude", view.SessionID, event.SourceKey)
				if err != nil {
					return evidence, err
				}
				var hook struct {
					SessionID string `json:"session_id"`
					Event     string `json:"hook_event_name"`
				}
				if json.Unmarshal(data, &hook) != nil || hook.SessionID != providerID {
					continue
				}
				if hook.Event == "StopFailure" {
					evidence.Failure = agent.ClassifyProviderDiagnostic(string(data))
				}
				if hook.Event == "Stop" {
					var stop struct {
						Last       string            `json:"last_assistant_message"`
						Background []json.RawMessage `json:"background_tasks"`
					}
					if json.Unmarshal(data, &stop) == nil {
						background = len(stop.Background) > 0
						if !background && stop.Last != "" && (stop.Last == evidence.Text || stop.Last == lastText) {
							evidence.Complete = true
						}
					}
				}
			}
			if !view.HasMore || view.Cursor <= cursor {
				break
			}
			cursor = view.Cursor
		}
		out := evidence
		out.Complete = evidence.Complete && flushed && !background
		return out, nil
	}, nil
}

// Hook lifecycle pages omit payloads. Open only the already-journaled exact
// spool entry; preserve full last_assistant_message for flush correlation.
func readRunHook(home, provider, sessionID, key string) ([]byte, error) {
	name, ok := strings.CutPrefix(key, "hook:")
	if !ok || !validLifecycleID(name) || !validLifecycleID(sessionID) {
		return nil, errors.New("invalid native hook identity")
	}
	root, err := openProviderHome(home)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := openProviderPath(root, filepath.Join("."+provider, "wingthing-events", sessionID, name), false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
