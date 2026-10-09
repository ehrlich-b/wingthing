package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

const maxProviderLine = 1024 * 1024

const (
	responseEvent = iota
	resultEventKind
)

// readProviderLines retains at most one capped line. Oversized lines are drained
// before parsing resumes, so a noisy tool cannot truncate the rest of a run.
func readProviderLines(r io.Reader, provider string, handle func(string)) error {
	reader := bufio.NewReader(r)
	line := make([]byte, 0, maxProviderLine)
	jsonEvents := provider == "codex" || provider == "claude" || provider == "cursor"
	var missing [2]error
	for {
		line = line[:0]
		var size int64
		var last byte
		var metadata eventMetadata
		var inspecting bool
		var readErr error
		for {
			part, err := reader.ReadSlice('\n')
			readErr = err
			if len(part) > 0 && part[len(part)-1] == '\n' {
				part = part[:len(part)-1]
			}
			if len(part) > 0 {
				last = part[len(part)-1]
			}
			size += int64(len(part))
			remaining := maxProviderLine - len(line)
			if remaining > len(part) {
				remaining = len(part)
			}
			line = append(line, part[:remaining]...)
			if jsonEvents && size > maxProviderLine {
				if !inspecting {
					metadata.read(line)
					inspecting = true
				}
				metadata.read(part[remaining:])
			}
			if err != bufio.ErrBufferFull {
				break
			}
		}
		// Match Scanner's ScanLines behavior for CRLF and a final unterminated line.
		if size > 0 && last == '\r' {
			size--
		}
		if size > maxProviderLine {
			slog.Warn("agent event skipped", "provider", provider, "byte_length", size, "limit", maxProviderLine)
			// Preserve failure detection without retaining any diagnostic text.
			if event, failed := metadata.failure(provider); failed {
				handle(event)
			}
			for kind, required := range metadata.required(provider, true) {
				if required {
					missing[kind] = fmt.Errorf("%s final %s event skipped: %d bytes exceeds %d-byte limit", provider, [...]string{"response", "result"}[kind], size, maxProviderLine)
				}
			}
		} else if size > 0 || readErr == nil {
			line = line[:int(size)]
			text := string(line)
			handle(text)
			// A later complete event of the same kind supplies the skipped result.
			if missing[responseEvent] != nil || missing[resultEventKind] != nil {
				for kind, present := range receivedProviderEvents(provider, text) {
					if present {
						missing[kind] = nil
					}
				}
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return readErr
			}
			for _, err := range missing {
				if err != nil {
					return err
				}
			}
			return nil
		}
	}
}

func receivedProviderEvents(provider, line string) (received [2]bool) {
	switch provider {
	case "codex":
		_, received[responseEvent] = parseCodexEvent(line)
		_, _, received[resultEventKind] = parseCodexUsage(line)
	case "claude", "cursor":
		_, received[responseEvent] = parseStreamEvent(line)
		_, _, received[resultEventKind] = parseResultTokens(line)
	default:
		received[responseEvent] = line != ""
	}
	return received
}

// eventMetadata reads only small JSON names and type values, including those
// after a large payload. It never retains the payload while the line is drained.
type eventMetadata struct {
	depth        int
	itemDepth    int
	messageDepth int
	contentDepth int
	blockDepth   int
	deltaDepth   int
	inString     bool
	escaped      bool
	stringSize   int
	stringBuf    [256]byte
	field        string
	lastString   string
	eventType    string
	itemType     string
	hasText      bool
	literal      string
	isError      bool
	hasError     bool
}

func (m *eventMetadata) read(part []byte) {
	for _, c := range part {
		if m.inString {
			if !m.escaped && c == '"' {
				m.inString = false
				m.finishString()
				continue
			}
			if m.stringSize < len(m.stringBuf) {
				m.stringBuf[m.stringSize] = c
			}
			if m.stringSize <= len(m.stringBuf) {
				m.stringSize++
			}
			if m.escaped {
				m.escaped = false
			} else if c == '\\' {
				m.escaped = true
			}
			continue
		}
		switch c {
		case '"':
			if m.depth == 1 && m.field == "error" {
				m.hasError = true
			}
			m.inString = true
			m.stringSize = 0
		case ':':
			m.field = m.lastString
			m.lastString = ""
		case '{', '[':
			if m.depth == 1 && m.field == "error" {
				m.hasError = true
			}
			if c == '{' && m.depth == 1 {
				switch m.field {
				case "item":
					m.itemDepth = m.depth + 1
				case "message":
					m.messageDepth = m.depth + 1
				case "delta":
					m.deltaDepth = m.depth + 1
				}
			} else if c == '[' && m.messageDepth > 0 && m.depth == m.messageDepth && m.field == "content" {
				m.contentDepth = m.depth + 1
			} else if c == '{' && m.contentDepth > 0 && m.depth == m.contentDepth {
				m.blockDepth = m.depth + 1
			}
			m.depth++
			m.field, m.lastString = "", ""
		case '}', ']':
			m.finishLiteral()
			for _, depth := range []*int{&m.itemDepth, &m.messageDepth, &m.contentDepth, &m.blockDepth, &m.deltaDepth} {
				if *depth == m.depth {
					*depth = 0
				}
			}
			m.depth--
			m.field, m.lastString = "", ""
		case ',':
			m.finishLiteral()
			m.field, m.lastString = "", ""
		case ' ', '\t', '\r', '\n':
			m.finishLiteral()
		default:
			if len(m.literal) < 5 {
				m.literal += string(c)
			}
		}
	}
}

func (m *eventMetadata) finishLiteral() {
	if m.literal == "" {
		return
	}
	if m.depth == 1 && m.field == "is_error" {
		m.isError = m.literal == "true"
	}
	if m.depth == 1 && m.field == "error" {
		m.hasError = m.literal != "null"
	}
	m.literal = ""
	m.field, m.lastString = "", ""
}

func (m *eventMetadata) finishString() {
	var value string
	if m.stringSize <= len(m.stringBuf) {
		// Decode escapes in short keys/types without decoding any large strings.
		quoted := append([]byte{'"'}, m.stringBuf[:m.stringSize]...)
		quoted = append(quoted, '"')
		_ = json.Unmarshal(quoted, &value)
	}
	if m.field == "type" {
		switch {
		case m.depth == 1:
			m.eventType = value
		case m.itemDepth > 0 && m.depth == m.itemDepth:
			m.itemType = value
		case m.blockDepth > 0 && m.depth == m.blockDepth && value == "text":
			m.hasText = true
		case m.deltaDepth > 0 && m.depth == m.deltaDepth && value == "text_delta":
			m.hasText = true
		}
	}
	m.lastString = value
	m.field = ""
}

func (m *eventMetadata) failure(provider string) (string, bool) {
	switch provider {
	case "codex":
		switch {
		case m.eventType == "turn.failed":
			return `{"type":"turn.failed"}`, true
		case m.eventType == "error":
			return `{"type":"error"}`, true
		case m.eventType == "item.completed" && m.itemType == "error":
			return `{"type":"item.completed","item":{"type":"error"}}`, true
		}
	case "claude", "cursor":
		if m.eventType == "error" {
			return `{"type":"error"}`, true
		}
		if m.eventType == "result" && m.isError {
			return `{"type":"result","is_error":true}`, true
		}
		if m.eventType == "assistant" && m.hasError {
			return `{"type":"assistant","error":true}`, true
		}
	}
	return "", false
}

func (m *eventMetadata) required(provider string, nonempty bool) (required [2]bool) {
	switch provider {
	case "codex":
		required[responseEvent] = m.eventType == "item.completed" && m.itemType == "agent_message"
		required[resultEventKind] = m.eventType == "turn.completed"
	case "claude", "cursor":
		required[responseEvent] = m.hasText && (m.eventType == "assistant" || m.eventType == "content_block_delta")
		required[resultEventKind] = m.eventType == "result"
	default:
		required[responseEvent] = nonempty
	}
	return required
}
