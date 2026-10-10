package egg

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

type codexTurnCompletion struct {
	Text    *string
	Failure agent.ErrorKind
}

func codexBoundTranscriptPath(home, providerID, path string) (string, error) {
	base := filepath.Join(home, ".codex", "sessions")
	rel, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsAbs(path) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !strings.HasSuffix(filepath.Base(path), "-"+providerID+".jsonl") {
		return "", errors.New("native Codex transcript must belong to the exact thread in its provider home")
	}
	return filepath.Join(".codex", "sessions", rel), nil
}

// Read only the transcript named by this egg's exact native hook. Never search
// rollouts or infer completion from an assistant message or file recency.
func readCodexTurnCompletion(home, path, providerID, turnID string, offset *int64, bound *bool) (completion codexTurnCompletion, flushed bool, err error) {
	root, err := openProviderHome(home)
	if err != nil {
		return completion, false, err
	}
	defer root.Close()
	f, err := openProviderPath(root, path, false)
	if err != nil {
		return completion, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return completion, false, err
	}
	if info.Size() < *offset {
		return completion, false, errors.New("native Codex transcript was truncated")
	}
	if _, err := f.Seek(*offset, io.SeekStart); err != nil {
		return completion, false, err
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), maxInitialRunWire)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, data[:i], nil
		}
		return 0, nil, nil // Leave incomplete native records for the next scan.
	})
	for scanner.Scan() {
		line := scanner.Bytes()
		var record struct {
			Type    string `json:"type"`
			Payload struct {
				ID     string          `json:"id"`
				Type   string          `json:"type"`
				TurnID string          `json:"turn_id"`
				Last   *string         `json:"last_agent_message"`
				Error  json.RawMessage `json:"error"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			return completion, false, errors.New("invalid native Codex transcript record")
		}
		if *offset == 0 {
			if record.Type != "session_meta" || record.Payload.ID != providerID {
				return completion, false, errors.New("native Codex transcript thread identity mismatch")
			}
			*bound = true
		}
		*offset += int64(len(line) + 1)
		if *bound && record.Type == "event_msg" && record.Payload.Type == "task_complete" && record.Payload.TurnID == turnID {
			completion.Text = record.Payload.Last
			if len(record.Payload.Error) > 0 && string(record.Payload.Error) != "null" {
				completion.Text = nil
				completion.Failure = agent.ClassifyProviderDiagnostic(string(record.Payload.Error))
			}
		}
	}
	return completion, *offset == info.Size(), scanner.Err()
}
