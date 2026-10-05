package egg

// Interactive lifecycle is independent of PTY activity. The journal belongs to
// Wingthing; provider inputs are exact-session native records, never screens.
import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const maxLifecycleRecord = 1 << 20
const maxLifecycleResponse = 2 << 20

type SessionEvent struct {
	Sequence          int64           `json:"sequence"`
	Type              string          `json:"type"`
	Source            string          `json:"source"`
	State             string          `json:"state,omitempty"`
	Role              string          `json:"role,omitempty"`
	Text              string          `json:"text,omitempty"`
	Raw               json.RawMessage `json:"raw,omitempty"`
	ProviderSessionID string          `json:"provider_session_id,omitempty"`
	Timestamp         string          `json:"timestamp,omitempty"`
	Reason            string          `json:"reason,omitempty"`
	Truncated         bool            `json:"truncated,omitempty"`
	// Import positions are persisted with each event in the same transaction.
	// This prevents duplicates if the reader or wing exits between imports.
	SourceKey    string `json:"source_key,omitempty"`
	SourceOffset int64  `json:"source_offset,omitempty"`
}

type SessionView struct {
	SessionID         string `json:"session_id"`
	Agent             string `json:"agent"`
	ProviderSessionID string `json:"provider_session_id,omitempty"`
	State             string `json:"state"`
	Status            string `json:"status"`
	StateSource       string `json:"state_source"`
	StateCursor       int64  `json:"state_cursor"`
	Reason            string `json:"reason,omitempty"`
	Ready             bool   `json:"ready"`
	ProcessAlive      bool   `json:"process_alive"`
	// Cursor is the last delivered event, not the journal head when paginated.
	Cursor     int64          `json:"cursor"`
	HeadCursor int64          `json:"head_cursor"`
	HasMore    bool           `json:"has_more"`
	Events     []SessionEvent `json:"events"`
}

type lifecycleJournal struct {
	root       *os.Root
	lock, file *os.File
	events     []SessionEvent
	pending    bool
}

func openLifecycleJournal(dir string) (*lifecycleJournal, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	j := &lifecycleJournal{root: root}
	fail := func(err error) (*lifecycleJournal, error) { j.close(); return nil, err }
	j.lock, err = root.OpenFile("lifecycle.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return fail(err)
	}
	if err = unix.Flock(int(j.lock.Fd()), unix.LOCK_EX); err != nil {
		return fail(err)
	}
	j.file, err = root.OpenFile("lifecycle.jsonl", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return fail(err)
	}
	r := bufio.NewReader(j.file)
	var offset int64
	for {
		line, readErr := readLifecycleLine(r)
		if readErr == io.EOF {
			// A crash during the final append must not hide earlier committed events.
			if len(line) > 0 {
				if err = j.file.Truncate(offset); err != nil {
					return fail(err)
				}
			}
			break
		}
		if readErr != nil {
			return fail(readErr)
		}
		var event SessionEvent
		if err = json.Unmarshal(line, &event); err != nil {
			return fail(fmt.Errorf("invalid lifecycle journal at %d: %w", offset, err))
		}
		if event.Sequence != int64(len(j.events))+1 {
			return fail(errors.New("invalid lifecycle event sequence"))
		}
		j.events = append(j.events, event)
		offset += int64(len(line))
	}
	_, err = j.file.Seek(0, io.SeekEnd)
	if err != nil {
		return fail(err)
	}
	return j, nil
}

func readLifecycleLine(r *bufio.Reader) ([]byte, error) {
	var data []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(data)+len(chunk) > maxLifecycleRecord {
			return nil, errors.New("lifecycle record exceeds 1 MiB")
		}
		data = append(data, chunk...)
		if err != bufio.ErrBufferFull {
			return data, err
		}
	}
}

func (j *lifecycleJournal) close() {
	if j.file != nil {
		_ = j.file.Close()
	}
	if j.lock != nil {
		_ = unix.Flock(int(j.lock.Fd()), unix.LOCK_UN)
		_ = j.lock.Close()
	}
	if j.root != nil {
		_ = j.root.Close()
	}
}

func (j *lifecycleJournal) append(event SessionEvent) error {
	event.Sequence = int64(len(j.events)) + 1
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(data)+1 > maxLifecycleRecord {
		return errors.New("lifecycle record exceeds 1 MiB")
	}
	if _, err = j.file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err = j.file.Sync(); err != nil {
		return err
	}
	j.events = append(j.events, event)
	return nil
}

// RecordSessionProcessEvent persists termination even when the transcript is
// absent. A killed/crashed provider is never reported as a completed turn.
func RecordSessionProcessEvent(dir, eventType, state, reason string) error {
	j, err := openLifecycleJournal(dir)
	if err != nil {
		return err
	}
	defer j.close()
	return j.append(SessionEvent{Type: eventType, State: state, Source: "egg_process", Reason: reason})
}

func lifecycleHookDir(home, sessionID string) string {
	return filepath.Join(home, ".claude", "wingthing-events", sessionID)
}

func shellQuoteLifecycle(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// lifecycleStatus is the shared inventory vocabulary. Process/transcript
// activity alone cannot establish agent status, including for legacy eggs.
func lifecycleStatus(state string, hookEvidence, processAlive, sessionEnded bool) string {
	if !hookEvidence {
		return "unknown"
	}
	if sessionEnded && state == "completed" {
		return "done"
	}
	if !processAlive {
		return "exited"
	}
	switch state {
	case "working":
		return "working"
	case "needs_input":
		return "blocked"
	case "idle", "completed":
		return "idle"
	default:
		return "unknown"
	}
}

func lifecycleHookCommand(spool string) string {
	// Publish atomically in sequence order, including concurrent hook writers.
	return "umask 077; wt_hook_dir=" + shellQuoteLifecycle(spool) + "; wt_hook_file=$(mktemp \"$wt_hook_dir/event.XXXXXX\") || exit 0; cat > \"$wt_hook_file\" || exit 0; " +
		"while :; do " +
		"wt_hook_seq=$(LC_ALL=C ls \"$wt_hook_dir\" 2>/dev/null | sed -n 's/^seq\\.\\([0-9]\\{20\\}\\)\\.json$/\\1/p' | tail -n 1); " +
		"wt_hook_seq=$(printf '%020d' \"$(expr \"${wt_hook_seq:-0}\" + 1)\"); " +
		"ln \"$wt_hook_file\" \"$wt_hook_dir/seq.$wt_hook_seq.json\" 2>/dev/null && { rm -f \"$wt_hook_file\"; exit 0; }; " +
		"[ -e \"$wt_hook_dir/seq.$wt_hook_seq.json\" ] || break; done; mv \"$wt_hook_file\" \"$wt_hook_file.json\"; exit 0"
}

// ClaudeLifecycleArgs adds observational native hooks to this invocation only.
// It preserves supplied settings and leaves disableAllHooks effective. The spool
// is inside the provider's existing writable directory; no permissions change.
func ClaudeLifecycleArgs(args []string, home, sessionID, providerID string) ([]string, error) {
	if home == "" || !validLifecycleID(sessionID) || !validLifecycleID(providerID) {
		return nil, errors.New("exact session identity and provider home required for lifecycle hooks")
	}
	settings := map[string]any{}
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		value := ""
		if args[i] == "--settings" {
			if i+1 >= len(args) {
				return nil, errors.New("--settings requires a value")
			}
			i++
			value = args[i]
		} else if strings.HasPrefix(args[i], "--settings=") {
			value = strings.TrimPrefix(args[i], "--settings=")
		} else {
			out = append(out, args[i])
			continue
		}
		data := []byte(value)
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			f, err := openBoundRegularFile(value)
			if err != nil {
				return nil, fmt.Errorf("read lifecycle settings: %w", err)
			}
			data, err = io.ReadAll(io.LimitReader(f, maxLifecycleRecord+1))
			_ = f.Close()
			if err != nil {
				return nil, err
			}
		}
		if len(data) > maxLifecycleRecord {
			return nil, errors.New("lifecycle settings exceeds 1 MiB")
		}
		var next map[string]any
		if err := json.Unmarshal(data, &next); err != nil {
			return nil, errors.New("invalid Claude settings for lifecycle hooks")
		}
		for k, v := range next {
			settings[k] = v
		}
	}
	if disabled, _ := settings["disableAllHooks"].(bool); !disabled {
		spool := lifecycleHookDir(home, sessionID)
		if err := os.MkdirAll(spool, 0700); err != nil {
			return nil, err
		}
		hooks, _ := settings["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		// No stdout, decisions, permission changes, or provider-hook input logs.
		// Publication is one atomic exclusive link to the next sequence name, so a
		// record is visible only with an order greater than every record already
		// visible; equal mtimes cannot reorder it. A collision means a newer record
		// won the name, so retry (the hook timeout bounds this; an unpublished temp
		// is never imported). Only a failure without collision, e.g. a filesystem
		// without links, falls back to the legacy mtime-ordered name.
		command := lifecycleHookCommand(spool)
		for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Notification", "Stop", "StopFailure", "SessionEnd"} {
			entries, _ := hooks[event].([]any)
			if event == "Notification" {
				entries = append(entries, map[string]any{"matcher": "permission_prompt|elicitation_dialog|elicitation_url_dialog|idle_prompt", "hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}})
			} else {
				entries = append(entries, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}})
			}
			hooks[event] = entries
		}
		settings["hooks"] = hooks
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	return append(out, "--settings", string(encoded)), nil
}

func validLifecycleID(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.Base(id) == id && !strings.ContainsAny(id, "\x00\r\n") && len(id) <= 240
}

// ReadSessionLifecycle imports exact native records into a durable journal and
// returns bounded cursor replay. State completion means the foreground turn;
// process_alive separately identifies whether this conversation can accept work.
func ReadSessionLifecycle(eggDir, agent, cwd, providerHome, exactProviderID string, processAlive bool, after int64, limit int) (SessionView, error) {
	view := SessionView{SessionID: filepath.Base(eggDir), Agent: agent, ProviderSessionID: exactProviderID, State: "unknown", Status: "unknown", StateSource: "unsupported", ProcessAlive: processAlive, Events: []SessionEvent{}, Cursor: after}
	if after < 0 || limit < 1 || limit > 200 {
		return view, errors.New("after_cursor must be non-negative and limit between 1 and 200")
	}
	j, err := openLifecycleJournal(eggDir)
	if err != nil {
		return view, err
	}
	defer j.close()
	if agent == "claude" && validLifecycleID(exactProviderID) {
		if err = j.importTranscript(eggDir, cwd, providerHome, exactProviderID); err != nil {
			return view, err
		}
		if err = j.importHooks(providerHome, view.SessionID, exactProviderID); err != nil {
			return view, err
		}
	} else if agent == "codex" {
		view.ProviderSessionID, err = j.importCodexHooks(providerHome, view.SessionID, exactProviderID)
		if err != nil {
			return view, err
		}
	}
	var responseBytes int
	processEnded := false
	var processEnd SessionEvent
	var hookState SessionEvent
	hookEvidence, sessionEnded := false, false
	for _, event := range j.events {
		if event.State != "" {
			view.State = event.State
			view.StateSource = event.Source
			view.Reason = event.Reason
			view.StateCursor = event.Sequence
		}
		if event.Source == "claude_hook" || event.Source == "codex_hook" {
			if event.State != "" {
				hookEvidence = true
				hookState = event
				sessionEnded = event.Type == "provider_session_end"
			}
		}
		if event.Type == "session_ready" || event.Type == "prompt_submitted" {
			view.Ready = true
		}
		if event.Type == "session_exit" || event.Type == "session_failed" {
			processEnded = true
			processEnd = event
		}
		if event.Sequence > after {
			encoded, _ := json.Marshal(event)
			if !view.HasMore && len(view.Events) < limit && responseBytes+len(encoded) <= maxLifecycleResponse {
				view.Events = append(view.Events, event)
				view.Cursor = event.Sequence
				responseBytes += len(encoded)
			} else {
				view.HasMore = true
			}
		}
		view.HeadCursor = event.Sequence
	}
	if j.pending {
		view.HasMore = true
	}
	// Native hooks report provider transitions directly. Transcript flush may
	// arrive later; historical assistant/tool rows must not roll state back.
	if hookState.Sequence > 0 {
		view.State = hookState.State
		view.StateSource = hookState.Source
		view.Reason = hookState.Reason
		view.StateCursor = hookState.Sequence
	}
	if processEnded {
		view.State = processEnd.State
		view.StateSource = processEnd.Source
		view.Reason = processEnd.Reason
		view.Ready = false
		view.StateCursor = processEnd.Sequence
		sessionEnded = true
	}
	view.Status = lifecycleStatus(view.State, hookEvidence, processAlive && !processEnded, sessionEnded)
	if sessionEnded {
		view.Ready = false
	}
	if after > view.HeadCursor {
		return view, errors.New("after_cursor is ahead of this session's event history")
	}
	if agent != "claude" && hookState.Sequence == 0 && !processEnded {
		view.State = "unknown"
		view.StateSource = "unsupported"
		view.Ready = false
		view.Reason = "provider has no native interactive lifecycle adapter"
	}
	if !processAlive {
		view.Ready = false
		if !processEnded && !sessionEnded {
			view.State = "unknown"
			view.StateSource = "egg_process"
			view.Reason = "provider process unavailable without a recorded exit"
		}
	}
	return view, nil
}

func (j *lifecycleJournal) importTranscript(eggDir, cwd, home, id string) error {
	var offset int64
	for _, e := range j.events {
		if e.SourceKey == "transcript" && e.SourceOffset > offset {
			offset = e.SourceOffset
		}
	}
	path, _, err := findClaudeSession(cwd, home, Profile("claude").SessionDir, time.Time{}, id)
	if err != nil {
		return err
	}
	var reader io.Reader
	var source *os.File
	var gz *gzip.Reader
	if path != "" {
		source, err = openBoundRegularFile(path)
		if err != nil {
			return err
		}
		defer func() { _ = source.Close() }()
		reader = source
	} else {
		meta, metaErr := os.ReadFile(filepath.Join(eggDir, "chat.meta"))
		if metaErr != nil || ParseChatMeta(string(meta))["agent_session_id"] != id {
			return nil
		}
		source, err = openBoundRegularFile(filepath.Join(eggDir, "chat.jsonl.gz"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer func() { _ = source.Close() }()
		gz, err = gzip.NewReader(source)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	}
	if offset > 0 {
		if _, err = io.CopyN(io.Discard, reader, offset); err != nil {
			return fmt.Errorf("provider transcript was truncated: %w", err)
		}
	}
	r := bufio.NewReader(reader)
	for count := 0; count < 500; count++ {
		line, readErr := readLifecycleLine(r)
		if readErr == io.EOF {
			return nil
		} // incomplete trailing JSON is retried
		if readErr != nil {
			return readErr
		}
		offset += int64(len(line))
		event := SessionEvent{Type: "provider_event", Source: "claude_transcript", SourceKey: "transcript", SourceOffset: offset, ProviderSessionID: id}
		var record struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Role       string          `json:"role"`
				Content    json.RawMessage `json:"content"`
				StopReason string          `json:"stop_reason"`
			} `json:"message"`
		}
		if err = json.Unmarshal(line, &record); err != nil {
			event.Type = "provider_warning"
			event.Reason = "invalid native transcript record"
		} else if record.SessionID != "" && record.SessionID != id {
			event.Type = "provider_warning"
			event.Reason = "native record belongs to another provider session"
		} else {
			event.Raw = bytes.TrimSpace(line)
			event.Timestamp = record.Timestamp
			event.Role = record.Message.Role
			if record.Type == "user" || record.Type == "assistant" {
				event.Type = "message"
				event.Text = lifecycleMessageText(record.Message.Content)
				event.State = "working"
				if record.Type == "assistant" && record.Message.StopReason == "end_turn" {
					event.State = "completed"
					event.Reason = "foreground response ended"
				}
			}
			// Bound response data independently from native transcript size.
			if len(event.Raw) > 512<<10 {
				event.Raw = nil
				event.Truncated = true
			}
			if len(event.Text) > 64<<10 {
				event.Text = string([]rune(event.Text)[:min(len([]rune(event.Text)), 16000)])
				event.Truncated = true
			}
		}
		if err = j.append(event); err != nil {
			return err
		}
	}
	j.pending = true
	return nil
}

func lifecycleMessageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var result []string
	for _, b := range blocks {
		if b.Type == "text" {
			result = append(result, b.Text)
		}
	}
	return strings.Join(result, "\n")
}

func sequencedLifecycleHook(name string) bool {
	digits, ok := strings.CutSuffix(strings.TrimPrefix(name, "seq."), ".json")
	if !ok || len(digits) != 20 || !strings.HasPrefix(name, "seq.") {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (j *lifecycleJournal) importHooks(home, sessionID, providerID string) error {
	_, err := j.importProviderHooks(lifecycleHookDir(home, sessionID), providerID, "claude")
	return err
}

func (j *lifecycleJournal) importCodexHooks(home, sessionID, providerID string) (string, error) {
	return j.importProviderHooks(filepath.Join(home, ".codex", "wingthing-events", sessionID), providerID, "codex")
}

func (j *lifecycleJournal) importProviderHooks(spool, providerID, agent string) (string, error) {
	source := agent + "_hook"
	if providerID == "" && agent == "codex" {
		for _, e := range j.events {
			if e.Source == source && e.Type == "session_ready" {
				providerID = e.ProviderSessionID
				break
			}
		}
	}
	entries, err := os.ReadDir(spool)
	if errors.Is(err, os.ErrNotExist) {
		return providerID, nil
	}
	if err != nil {
		return providerID, err
	}
	seen := map[string]bool{}
	for _, e := range j.events {
		if e.Source == source {
			seen[e.SourceKey] = true
		}
	}
	type hookFile struct {
		name     string
		modified time.Time
	}
	var files []hookFile
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && !seen["hook:"+entry.Name()] {
			info, e := entry.Info()
			if e == nil {
				files = append(files, hookFile{entry.Name(), info.ModTime()})
			}
		}
	}
	// Sequenced names order by fixed-width publish sequence. Legacy names come
	// only from older producers or link-less filesystems and keep mtime order.
	sort.Slice(files, func(a, b int) bool {
		sa, sb := sequencedLifecycleHook(files[a].name), sequencedLifecycleHook(files[b].name)
		if sa || sb {
			if sa && sb {
				return files[a].name < files[b].name
			}
			return sb
		}
		if files[a].modified.Equal(files[b].modified) {
			return files[a].name < files[b].name
		}
		return files[a].modified.Before(files[b].modified)
	})
	for _, file := range files[:min(len(files), 500)] {
		f, err := openBoundRegularFile(filepath.Join(spool, file.name))
		if err != nil {
			return providerID, err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxLifecycleRecord+1))
		_ = f.Close()
		if err != nil {
			return providerID, err
		}
		if len(data) > maxLifecycleRecord {
			return providerID, errors.New("native hook exceeds 1 MiB")
		}
		var hook struct {
			SessionID    string            `json:"session_id"`
			Event        string            `json:"hook_event_name"`
			Prompt       string            `json:"prompt"`
			Notification string            `json:"notification_type"`
			Background   []json.RawMessage `json:"background_tasks"`
			ToolName     string            `json:"tool_name"`
		}
		if json.Unmarshal(data, &hook) != nil {
			return providerID, errors.New("invalid published native lifecycle hook")
		}
		// A fresh Codex thread chooses its own ID. Bind once from SessionStart
		// in this egg's private spool, then reject other threads (and subagents).
		if agent == "codex" && providerID == "" && hook.Event == "SessionStart" && validLifecycleID(hook.SessionID) {
			providerID = hook.SessionID
		}
		e := SessionEvent{Source: source, SourceKey: "hook:" + file.name, ProviderSessionID: providerID, Timestamp: file.modified.UTC().Format(time.RFC3339Nano)}
		if providerID == "" || hook.SessionID != providerID {
			e.Type = "provider_warning"
			e.Reason = "native hook belongs to another provider session"
		} else {
			switch hook.Event {
			case "SessionStart":
				e.Type = "session_ready"
				e.State = "idle"
			case "UserPromptSubmit":
				e.Type = "prompt_submitted"
				e.State = "working"
				e.Text = hook.Prompt
			case "PreToolUse", "PostToolUse":
				e.Type = "tool_activity"
				e.State = "working"
				if agent == "codex" && hook.Event == "PreToolUse" && hook.ToolName == "request_user_input" {
					e.Type, e.State, e.Reason = "input_requested", "needs_input", "provider user input requested"
				}
			case "PreCompact", "PostCompact":
				e.Type, e.State = "provider_compaction", "working"
			case "Interrupt":
				e.Type, e.State = "turn_interrupted", "idle"
			case "PermissionRequest":
				e.Type = "input_requested"
				e.State = "needs_input"
				e.Reason = "provider permission decision requested"
			case "Notification":
				e.Type = "notification"
				if hook.Notification == "idle_prompt" {
					e.State = "idle"
				} else if hook.Notification == "permission_prompt" || hook.Notification == "elicitation_dialog" || hook.Notification == "elicitation_url_dialog" {
					e.State = "needs_input"
					e.Reason = hook.Notification
				}
			case "Stop":
				e.Type = "turn_completed"
				e.State = "completed"
				e.Reason = "native Stop observed for foreground response; other Stop hooks or later prompts may continue the conversation"
				if len(hook.Background) > 0 {
					e.State = "idle"
					e.Reason = "foreground response ended with background tasks pending"
				}
			case "StopFailure":
				e.Type = "turn_failed"
				e.State = "failed"
				e.Reason = "native provider reported a failed turn"
			case "SessionEnd":
				e.Type = "provider_session_end"
				e.State = "completed"
			default:
				e.Type = "provider_event"
			}
		}
		if len(e.Text) > 64<<10 {
			e.Text = ""
			e.Truncated = true
		}
		if err = j.append(e); err != nil {
			return providerID, err
		}
	}
	if len(files) > 500 {
		j.pending = true
	}
	return providerID, nil
}
