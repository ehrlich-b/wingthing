package egg

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

// nativeFailureKind inspects transient provider records. Only the enum crosses
// the lifecycle/archive boundary; authorized nonfailure conversation remains.
func nativeFailureKind(provider string, line []byte) (agent.ErrorKind, bool) {
	if kind, failed := agent.ClassifyProviderFailure(provider, line); failed {
		return kind, true
	}
	if provider == "codex" {
		var r struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &r) == nil && r.Type == "event_msg" {
			return agent.ClassifyProviderFailure(provider, r.Payload)
		}
	}
	var r struct {
		Type    string          `json:"type"`
		Failure agent.ErrorKind `json:"failure_kind"`
	}
	if json.Unmarshal(line, &r) == nil && r.Type == "wingthing_failure" {
		if r.Failure == "" {
			r.Failure = agent.UnknownOutcome
		}
		return r.Failure, true
	}
	return "", false
}

// Preserve byte offsets when redacting failures: lifecycle replay can switch
// from the live native file to this archive without replaying another turn.
func copyConversationArchive(dst io.Writer, src io.Reader, provider string) error {
	reader := bufio.NewReader(src)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			kind, failed := nativeFailureKind(provider, line)
			if failed {
				replacement, _ := json.Marshal(map[string]any{"type": "wingthing_failure", "failure_kind": kind})
				// Even unusually small malformed/failure records preserve offsets.
				if len(replacement) > len(bytes.TrimSuffix(line, []byte{'\n'})) {
					replacement = []byte(`{}`)
				}
				newline := len(line) > 0 && line[len(line)-1] == '\n'
				padding := len(line) - len(replacement)
				if newline {
					padding--
				}
				if _, writeErr := dst.Write(replacement); writeErr != nil {
					return writeErr
				}
				spaces := bytes.Repeat([]byte{' '}, 4096)
				for padding > 0 {
					n := min(padding, len(spaces))
					if _, writeErr := dst.Write(spaces[:n]); writeErr != nil {
						return writeErr
					}
					padding -= n
				}
				if newline {
					if _, writeErr := dst.Write([]byte{'\n'}); writeErr != nil {
						return writeErr
					}
				}
			} else if _, writeErr := dst.Write(line); writeErr != nil {
				return writeErr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
