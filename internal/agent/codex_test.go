package agent

import (
	"testing"
)

func TestNewCodexDefaults(t *testing.T) {
	c := NewCodex(0)
	if c.ContextWindow() != 192000 {
		t.Errorf("context window = %d, want 192000", c.ContextWindow())
	}
	if c.command != "codex" {
		t.Errorf("command = %q, want %q", c.command, "codex")
	}
}

func TestNewCodexCustomWindow(t *testing.T) {
	c := NewCodex(64000)
	if c.ContextWindow() != 64000 {
		t.Errorf("context window = %d, want 64000", c.ContextWindow())
	}
}

func TestCodexImplementsAgent(t *testing.T) {
	var _ Agent = (*Codex)(nil)
}

func TestParseCodexEvent(t *testing.T) {
	line := `{"type":"item.completed","item":{"type":"agent_message","text":"Hello from Codex"}}`
	text, ok := parseCodexEvent(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if text != "Hello from Codex" {
		t.Errorf("text = %q, want %q", text, "Hello from Codex")
	}
}

func TestParseCodexEventNonMessage(t *testing.T) {
	line := `{"type":"item.completed","item":{"type":"tool_call","text":""}}`
	_, ok := parseCodexEvent(line)
	if ok {
		t.Error("expected not ok for non-agent_message")
	}
}

func TestParseCodexEventGarbage(t *testing.T) {
	_, ok := parseCodexEvent("not json")
	if ok {
		t.Error("expected not ok for garbage")
	}
}

func TestParseCodexUsage(t *testing.T) {
	line := `{"type":"turn.completed","usage":{"input_tokens":1000,"output_tokens":500}}`
	input, output, ok := parseCodexUsage(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if input != 1000 {
		t.Errorf("input = %d, want 1000", input)
	}
	if output != 500 {
		t.Errorf("output = %d, want 500", output)
	}
}

func TestParseCodexUsageNonTurn(t *testing.T) {
	line := `{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`
	_, _, ok := parseCodexUsage(line)
	if ok {
		t.Error("expected not ok for non-turn event")
	}
}
