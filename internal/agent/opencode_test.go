package agent

import (
	"testing"
)

func TestNewOpenCodeDefaults(t *testing.T) {
	o := NewOpenCode(0)
	if o.ContextWindow() != 200000 {
		t.Errorf("context window = %d, want 200000", o.ContextWindow())
	}
	if o.command != "opencode" {
		t.Errorf("command = %q, want %q", o.command, "opencode")
	}
}

func TestNewOpenCodeCustomWindow(t *testing.T) {
	o := NewOpenCode(128000)
	if o.ContextWindow() != 128000 {
		t.Errorf("context window = %d, want 128000", o.ContextWindow())
	}
}

func TestOpenCodeImplementsAgent(t *testing.T) {
	var _ Agent = (*OpenCode)(nil)
}
