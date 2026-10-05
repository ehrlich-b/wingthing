package egg

// This is a native-protocol building block, not an enabled provider adapter.
// The interactive Codex profile remains unsupported until an egg owns both the
// app-server and remote TUI, including auth, input ownership and reconnect.
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

const maxCodexNativeWire = 256 << 10

// CodexNativeEvent retains native correlation independently of Wingthing's
// delivery sequence. Event.Raw also retains these IDs when journaled today.
type CodexNativeEvent struct {
	Event             SessionEvent    `json:"event"`
	TurnID            string          `json:"turn_id,omitempty"`
	ItemID            string          `json:"item_id,omitempty"`
	ProviderRequestID json.RawMessage `json:"provider_request_id,omitempty"`
}

type codexNativeStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags"`
}

// CodexNativeThreadResult reads only the exact thread returned by a caller's
// thread/start or thread/resume RPC. It never selects from an inventory/path.
// The caller must associate the response with its outstanding RPC request ID.
func CodexNativeThreadResult(wire []byte, expectedID string) (string, error) {
	if len(wire) > maxCodexNativeWire || !json.Valid(wire) {
		return "", errors.New("invalid or oversized Codex thread response")
	}
	var response struct {
		Result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(wire, &response); err != nil {
		return "", err
	}
	if len(response.Error) != 0 && string(response.Error) != "null" {
		return "", errors.New("Codex native thread operation failed")
	}
	id := response.Result.Thread.ID
	if !validCodexNativeID(id) || (expectedID != "" && id != expectedID) {
		return "", errors.New("Codex native thread identity missing or mismatched")
	}
	return id, nil
}

// CodexNativeCommandPlan describes argv only; it never starts a provider.
// Both commands MUST execute inside the same existing egg sandbox. A private
// provider home is an input, not a request to copy authentication or grant access.
func CodexNativeCommandPlan(binary, home, socket, providerID string) (server, tui []string, err error) {
	if !filepath.IsAbs(binary) || !filepath.IsAbs(home) || !filepath.IsAbs(socket) || !validCodexNativeID(providerID) {
		return nil, nil, errors.New("absolute executable, provider home, private socket and exact native thread ID required")
	}
	relative, err := filepath.Rel(filepath.Clean(home), filepath.Clean(socket))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, nil, errors.New("Codex native socket must be inside the selected provider home")
	}
	// The integration must separately validate filesystem ownership/symlinks and
	// socket length, then set CODEX_HOME for these child processes only.
	endpoint := "unix://" + filepath.Clean(socket)
	server = []string{binary, "app-server", "--listen", endpoint, "--disable", "plugins", "-c", "forced_login_method=chatgpt"}
	tui = []string{binary, "--remote", endpoint, "resume", providerID}
	return server, tui, nil
}

// ParseCodexNativeEvent accepts the v2 app-server wire protocol for one exact
// thread. Foreign threads and unsupported methods are ignored. It emits no
// decisions/replies; every server request remains under the approval owner's
// control. Native runtime idle does not alone prove interactive TUI readiness.
func ParseCodexNativeEvent(providerID string, wire []byte) (CodexNativeEvent, bool, error) {
	var out CodexNativeEvent
	if !validCodexNativeID(providerID) || len(wire) == 0 || len(wire) > maxCodexNativeWire || !json.Valid(wire) {
		return out, false, errors.New("exact thread ID and bounded valid Codex native message required")
	}
	var probe struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(wire, &probe); err != nil {
		return out, false, err
	}
	switch probe.Method {
	case "thread/started", "thread/status/changed", "turn/started", "turn/completed", "item/started", "item/completed", "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput", "tool/requestUserInput", "mcpServer/elicitation/request", "item/tool/call", "serverRequest/resolved":
	default:
		return out, false, nil
	}
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			ItemID   string `json:"itemId"`
			Thread   struct {
				ID string `json:"id"`
			} `json:"thread"`
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
			Status codexNativeStatus `json:"status"`
			Item   struct {
				ID      string `json:"id"`
				Type    string `json:"type"`
				Text    string `json:"text"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"item"`
			RequestID  json.RawMessage `json:"requestId"`
			IsBlocking *bool           `json:"isBlocking"`
		} `json:"params"`
	}
	if err := json.Unmarshal(wire, &message); err != nil {
		return out, false, err
	}
	p := message.Params
	id := p.ThreadID
	if message.Method == "thread/started" {
		id = p.Thread.ID
	}
	if id != providerID {
		return out, false, nil
	}
	if (p.Thread.ID != "" && p.Thread.ID != providerID) || (p.Thread.ID != "" && p.ThreadID != "" && p.Thread.ID != p.ThreadID) {
		return out, false, errors.New("conflicting native thread identities")
	}
	out.TurnID, out.ItemID = p.TurnID, p.ItemID
	if p.Turn.ID != "" {
		if out.TurnID != "" && out.TurnID != p.Turn.ID {
			return out, false, errors.New("conflicting native turn identities")
		}
		out.TurnID = p.Turn.ID
	}
	if p.Item.ID != "" {
		if out.ItemID != "" && out.ItemID != p.Item.ID {
			return out, false, errors.New("conflicting native item identities")
		}
		out.ItemID = p.Item.ID
	}
	for _, value := range []string{out.TurnID, out.ItemID} {
		if value != "" && !validCodexNativeID(value) {
			return out, false, errors.New("invalid native turn or item identity")
		}
	}
	out.Event = SessionEvent{Source: "codex_app_server", ProviderSessionID: providerID, Raw: append(json.RawMessage(nil), bytes.TrimSpace(wire)...)}
	e := &out.Event
	switch message.Method {
	case "thread/started":
		e.Type, e.State = "provider_bound", "starting"
		e.Reason = "exact native thread bound; awaiting authenticated interactive readiness"
	case "thread/status/changed":
		e.Type = "provider_status"
		switch p.Status.Type {
		case "idle":
			e.State, e.Reason = "idle", "native runtime idle; TUI readiness must be verified separately"
		case "active":
			e.State = "working"
			for _, flag := range p.Status.ActiveFlags {
				if flag == "waitingOnApproval" || flag == "waitingOnUserInput" {
					e.Type, e.State, e.Reason = "needs_input", "needs_input", flag
				}
			}
		case "notLoaded":
			e.State, e.Reason = "unknown", "native thread is not loaded"
		case "systemError":
			e.State, e.Reason = "failed", "native thread system error"
		default:
			e.State, e.Reason = "unknown", "unrecognized native thread status"
		}
	case "turn/started", "turn/completed":
		if !validCodexNativeID(out.TurnID) {
			return out, false, errors.New("native turn event requires its exact turn ID")
		}
		if message.Method == "turn/started" {
			if p.Turn.Status != "inProgress" {
				return out, false, errors.New("native started turn must be in progress")
			}
			e.Type, e.State = "turn_started", "working"
		} else {
			switch p.Turn.Status {
			case "completed":
				e.Type, e.State = "turn_completed", "completed"
			case "interrupted":
				e.Type, e.State, e.Reason = "turn_interrupted", "idle", "native turn interrupted"
			case "failed":
				e.Type, e.State, e.Reason = "turn_failed", "failed", "native turn failed; inspect provider event"
			default:
				return out, false, errors.New("unrecognized native completed turn status")
			}
		}
		e.SourceKey, e.SourceOffset = "codex_native/"+providerID+"/"+out.TurnID+"/"+message.Method, 1
	case "item/started", "item/completed":
		if !validCodexNativeID(out.TurnID) || !validCodexNativeID(out.ItemID) || p.Item.Type == "" {
			return out, false, errors.New("native item requires exact turn, item ID and type")
		}
		e.Type = "provider_item"
		// Only complete items are authoritative transcript records. An assistant
		// item finishing never means the containing turn has finished.
		if message.Method == "item/completed" {
			switch p.Item.Type {
			case "userMessage":
				e.Type, e.Role, e.State = "prompt_submitted", "user", "working"
				var texts []string
				for _, content := range p.Item.Content {
					if content.Type == "text" {
						texts = append(texts, content.Text)
					}
				}
				e.Text = strings.Join(texts, "\n")
			case "agentMessage":
				e.Type, e.Role, e.Text = "message", "assistant", p.Item.Text
			}
		}
		e.SourceKey, e.SourceOffset = "codex_native/"+providerID+"/"+out.TurnID+"/"+out.ItemID+"/"+message.Method, 1
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput", "tool/requestUserInput", "mcpServer/elicitation/request", "item/tool/call":
		if (message.Method != "mcpServer/elicitation/request" && !validCodexNativeID(out.TurnID)) || !validNativeRequestID(message.ID) {
			return out, false, errors.New("native attention request requires exact turn and JSON-RPC request ID")
		}
		out.ProviderRequestID = append(json.RawMessage(nil), message.ID...)
		e.Type, e.State, e.Reason = "needs_input", "needs_input", message.Method
		if (message.Method == "item/tool/requestUserInput" || message.Method == "tool/requestUserInput") && p.IsBlocking != nil && !*p.IsBlocking {
			e.Type, e.State = "provider_input_requested", ""
		}
	case "serverRequest/resolved":
		if !validNativeRequestID(p.RequestID) {
			return out, false, errors.New("native request resolution requires request ID")
		}
		out.ProviderRequestID = append(json.RawMessage(nil), p.RequestID...)
		e.Type, e.Reason = "provider_request_resolved", "request answered or cleared; no approval decision inferred"
	default:
		return CodexNativeEvent{}, false, nil
	}
	if len(e.Text) > 64<<10 {
		e.Text = string([]rune(e.Text)[:min(len([]rune(e.Text)), 16000)])
		e.Truncated = true
	}
	return out, true, nil
}

func validCodexNativeID(value string) bool {
	return validLifecycleID(value) && !strings.HasPrefix(value, "-") && strings.IndexFunc(value, unicode.IsSpace) < 0
}

func validNativeRequestID(value json.RawMessage) bool {
	if len(value) == 0 || len(value) > 512 {
		return false
	}
	var id any
	d := json.NewDecoder(bytes.NewReader(value))
	d.UseNumber()
	if d.Decode(&id) != nil {
		return false
	}
	switch id.(type) {
	case string:
		return true
	case json.Number:
		_, err := id.(json.Number).Int64()
		return err == nil
	}
	return false
}

// RecordCodexNativeEvent lets a future in-sandbox protocol consumer persist an
// already bound thread's event. Source keys deduplicate final turn/item replay;
// stream deltas are deliberately unsupported because they have no replay cursor.
func RecordCodexNativeEvent(eggDir, providerID string, wire []byte) (CodexNativeEvent, bool, error) {
	event, accepted, err := ParseCodexNativeEvent(providerID, wire)
	if err != nil || !accepted {
		return event, accepted, err
	}
	j, err := openLifecycleJournal(eggDir)
	if err != nil {
		return event, false, fmt.Errorf("open native journal: %w", err)
	}
	defer j.close()
	if event.Event.SourceKey != "" {
		for _, existing := range j.events {
			if existing.Source == event.Event.Source && existing.SourceKey == event.Event.SourceKey && existing.SourceOffset >= event.Event.SourceOffset {
				return event, true, nil
			}
		}
	}
	if err = j.append(event.Event); err != nil {
		return event, false, err
	}
	return event, true, nil
}
