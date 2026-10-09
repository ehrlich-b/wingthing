package egg

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

func codexNotifySpool(home, sessionID string) string {
	return filepath.Join(home, ".codex", "wingthing-run-notify", sessionID)
}

// CodexRunArgs keeps Codex's TUI and embedded runtime in the egg's process
// group. Native notifications publish atomically through a short-lived shell
// callback owned by Codex; no daemon, provider restart or rollout search exists.
func CodexRunArgs(args []string, home, sessionID string) ([]string, bool, error) {
	hooked, err := CodexLifecycleArgs(args, home, sessionID)
	if err != nil {
		return nil, false, err
	}
	for _, arg := range args[:providerOptionsEnd(args)] {
		if arg == "--remote" || strings.HasPrefix(arg, "--remote=") {
			return nil, false, errors.New("egg Codex TUI must use its own local runtime")
		}
	}
	// Explicit hooks/notify overrides retain their existing behavior and cannot
	// claim native run support without Wingthing's exact generated observers.
	configuredNotify := false
	for i, arg := range args[:providerOptionsEnd(args)] {
		value := arg
		if (arg == "-c" || arg == "--config") && i+1 < len(args) {
			value = args[i+1]
		}
		value = strings.TrimPrefix(strings.TrimPrefix(value, "--config="), "-c")
		key, _, _ := strings.Cut(value, "=")
		if strings.TrimSpace(key) == "notify" {
			configuredNotify = true
		}
	}
	if len(hooked) == len(args) || configuredNotify {
		return append([]string{"--no-daemon"}, hooked...), false, nil
	}
	spool := codexNotifySpool(home, sessionID)
	if err = os.MkdirAll(spool, 0700); err != nil {
		return nil, false, err
	}
	command := "printf '%s\\n' \"$1\" | (" + lifecycleHookCommand(spool) + ")"
	// notify appends its one native JSON payload after these argv, as $1.
	values := []string{"/bin/sh", "-c", command, "wt-codex-notify"}
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	// This overrides config.toml notify; no effective-config reader exists to chain it.
	prefix := []string{"--no-daemon", "-c", "notify=[" + strings.Join(quoted, ",") + "]"}
	return append(prefix, hooked...), true, nil
}

type codexTurnNotification struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread-id"`
	TurnID   string          `json:"turn-id"`
	Inputs   []string        `json:"input-messages"`
	Last     *string         `json:"last-assistant-message"`
	Error    json.RawMessage `json:"error"`
}

// Read only our egg's native notification spool. Thread identity comes from
// SessionStart and turn identity from UserPromptSubmit, never file recency.
func codexRunScanner(home, sessionID, providerID, prompt string, read func(context.Context, int64, int) (SessionView, error), readFile runFileReader) (func() (turnEvidence, error), error) {
	if !validCodexNativeID(providerID) {
		return nil, errors.New("exact native Codex thread required")
	}
	root, err := openProviderHome(home)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	spoolPath := filepath.Join(".codex", "wingthing-run-notify", sessionID)
	initialFiles, err := codexRunNotifications(root, spoolPath, nil, readFile)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for name := range initialFiles {
		seen[name] = true
	}
	initial, err := read(context.Background(), 0, 1)
	if err != nil {
		return nil, err
	}
	cursor := initial.HeadCursor
	var evidence turnEvidence
	var receiptKey string
	pending := make(map[string]codexTurnNotification)
	return func() (turnEvidence, error) {
		for {
			view, err := read(context.Background(), cursor, 200)
			if err != nil {
				return evidence, err
			}
			if view.ProviderSessionID != providerID {
				evidence.Conflict = true
			}
			for _, event := range view.Events {
				if event.Source != "codex_hook" {
					continue
				}
				data, err := readRunHook(home, "codex", sessionID, event.SourceKey, readFile)
				if err != nil {
					return evidence, err
				}
				var hook struct {
					SessionID string `json:"session_id"`
					TurnID    string `json:"turn_id"`
					Event     string `json:"hook_event_name"`
					Prompt    string `json:"prompt"`
				}
				if json.Unmarshal(data, &hook) != nil || hook.SessionID != providerID {
					continue
				}
				if hook.Event == "UserPromptSubmit" {
					if receiptKey == event.SourceKey {
						continue
					}
					if evidence.Receipt || hook.Prompt != prompt {
						evidence.Conflict = true
						continue
					}
					if !validCodexNativeID(hook.TurnID) {
						continue
					}
					evidence.Receipt, evidence.TurnID, receiptKey = true, hook.TurnID, event.SourceKey
				}
				if hook.Event == "Interrupt" && hook.TurnID == evidence.TurnID {
					evidence.Failure = agent.Stopped
				}
			}
			advanced := view.Cursor > cursor
			cursor = view.Cursor
			if !view.HasMore || !advanced {
				break
			}
		}
		root, err := openProviderHome(home)
		if err != nil {
			return evidence, err
		}
		files, err := codexRunNotifications(root, spoolPath, seen, readFile)
		root.Close()
		if err != nil {
			return evidence, err
		}
		for name, data := range files {
			if seen[name] {
				continue
			}
			seen[name] = true
			var notify codexTurnNotification
			if json.Unmarshal(data, &notify) != nil {
				return evidence, errors.New("invalid native Codex completion notification")
			}
			if notify.Type != "agent-turn-complete" || notify.ThreadID != providerID || !validCodexNativeID(notify.TurnID) {
				continue
			}
			if previous, exists := pending[notify.TurnID]; exists {
				old, _ := json.Marshal(previous)
				fresh, _ := json.Marshal(notify)
				if string(old) != string(fresh) {
					return evidence, errors.New("conflicting native Codex completion replay")
				}
			} else {
				pending[notify.TurnID] = notify
			}
		}
		if notify, exists := pending[evidence.TurnID]; exists && evidence.Receipt {
			if len(notify.Inputs) != 1 || notify.Inputs[0] != prompt {
				evidence.Conflict = true
				return evidence, nil
			}
			if len(notify.Error) != 0 && string(notify.Error) != "null" {
				evidence.Failure = agent.ClassifyProviderDiagnostic(string(notify.Error))
			} else if notify.Last != nil {
				evidence.Complete, evidence.Text = true, *notify.Last
			}
		}
		return evidence, nil
	}, nil
}

func codexRunNotifications(root *os.File, path string, seen map[string]bool, readFile runFileReader) (map[string][]byte, error) {
	out := make(map[string][]byte)
	dir, err := openProviderPath(root, path, true)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || seen[entry.Name()] {
			continue
		}
		data, err := readFile(dir, entry.Name())
		if err != nil {
			return nil, err
		}
		out[entry.Name()] = data
	}
	return out, nil
}
