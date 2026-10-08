package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"
)

const maxProviderLine = 1024 * 1024
const maxProviderDiagnostic = 4096

const (
	responseEvent = iota
	resultEventKind
)

// readProviderLines retains a capped line and a bounded failure diagnostic.
// Oversized lines are drained before parsing resumes, so a noisy tool cannot
// truncate the rest of a run.
func readProviderLines(r io.Reader, provider string, handle func(string), redactors ...*Redactor) error {
	reader := bufio.NewReader(r)
	line := make([]byte, 0, maxProviderLine)
	jsonEvents := provider == "codex" || provider == "claude" || provider == "cursor"
	var missing [2]error
	var redactor *Redactor
	if len(redactors) > 0 {
		redactor = redactors[0]
	}
	for {
		line = line[:0]
		var size int64
		var last byte
		var metadata eventMetadata
		metadata.redactor = redactor
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
			// Draining an oversized failure must not turn a zero-exit refusal
			// into success. Reconstruct just its type and capped diagnostic.
			if event, ok := metadata.failure(provider); ok {
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

// eventMetadata reads small JSON names/types and a bounded failure diagnostic,
// including fields after a large payload, while the rest of the line is drained.
type eventMetadata struct {
	depth        int
	itemDepth    int
	messageDepth int
	contentDepth int
	blockDepth   int
	deltaDepth   int
	errorDepth   int
	inString     bool
	escaped      bool
	stringSize   int
	stringBuf    [256]byte
	field        string
	lastString   string
	eventType    string
	itemType     string
	hasText      bool
	redactor     *Redactor
	messageKind  int
	messageBuf   []byte
	messages     [3]string
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
			if m.messageKind >= 0 && len(m.messageBuf) < m.diagnosticCaptureLimit() {
				m.messageBuf = append(m.messageBuf, c)
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
			m.inString = true
			m.stringSize = 0
			m.messageKind = -1
			m.messageBuf = m.messageBuf[:0]
			if m.field == "message" {
				switch {
				case m.depth == 1:
					m.messageKind = 0
				case m.errorDepth > 0 && m.depth == m.errorDepth:
					m.messageKind = 1
				case m.itemDepth > 0 && m.depth == m.itemDepth:
					m.messageKind = 2
				}
			}
		case ':':
			m.field = m.lastString
			m.lastString = ""
		case '{', '[':
			if c == '{' && m.depth == 1 {
				switch m.field {
				case "item":
					m.itemDepth = m.depth + 1
				case "message":
					m.messageDepth = m.depth + 1
				case "delta":
					m.deltaDepth = m.depth + 1
				case "error":
					m.errorDepth = m.depth + 1
				}
			} else if c == '[' && m.messageDepth > 0 && m.depth == m.messageDepth && m.field == "content" {
				m.contentDepth = m.depth + 1
			} else if c == '{' && m.contentDepth > 0 && m.depth == m.contentDepth {
				m.blockDepth = m.depth + 1
			}
			m.depth++
			m.field, m.lastString = "", ""
		case '}', ']':
			for _, depth := range []*int{&m.itemDepth, &m.messageDepth, &m.contentDepth, &m.blockDepth, &m.deltaDepth, &m.errorDepth} {
				if *depth == m.depth {
					*depth = 0
				}
			}
			m.depth--
			m.field, m.lastString = "", ""
		case ',':
			m.field, m.lastString = "", ""
		}
	}
}

func (m *eventMetadata) finishString() {
	if m.messageKind >= 0 {
		// A prefix may end inside a JSON escape. Drop only that incomplete
		// escape, then decode and redact before applying the display cap.
		quoted := append([]byte{'"'}, m.messageBuf...)
		for dropped := 0; dropped <= 6 && len(quoted) > 0; dropped++ {
			var message string
			if json.Unmarshal(append(quoted, '"'), &message) == nil {
				message = m.redactor.Text(message)
				if len(message) > maxProviderDiagnostic {
					message = message[:maxProviderDiagnostic]
					for !utf8.ValidString(message) {
						message = message[:len(message)-1]
					}
				}
				m.messages[m.messageKind] = message
				break
			}
			quoted = quoted[:len(quoted)-1]
		}
	}
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

func (m *eventMetadata) diagnosticCaptureLimit() int {
	// Retain enough lookahead to redact a known credential crossing the display
	// boundary, even if every byte in it is encoded as a six-byte JSON escape.
	limit := 6*maxProviderDiagnostic + 8
	if m.redactor != nil && len(m.redactor.secrets) > 0 {
		limit += 6 * len(m.redactor.secrets[0])
	}
	return limit
}

func (m *eventMetadata) failure(provider string) (string, bool) {
	if provider != "codex" {
		return "", false
	}
	event := codexEvent{Type: m.eventType}
	message := func(kind int) string {
		text := strings.TrimSpace(m.messages[kind])
		if text == "" {
			text = "provider failure"
		}
		return text + " [oversized event truncated]"
	}
	switch {
	case m.eventType == "turn.failed":
		event.Error = &codexError{Message: message(1)}
	case m.eventType == "error":
		event.Message = message(0)
	case m.eventType == "item.completed" && m.itemType == "error":
		event.Item = &codexItem{Type: "error", Message: message(2)}
	default:
		return "", false
	}
	data, _ := json.Marshal(event)
	return string(data), true
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
