package agent

import (
	"testing"
)

func TestNewGeminiDefaults(t *testing.T) {
	g := NewGemini("", 0)
	if g.model != "gemini-2.5-pro" {
		t.Errorf("model = %q, want %q", g.model, "gemini-2.5-pro")
	}
	if g.ContextWindow() != 1000000 {
		t.Errorf("context window = %d, want 1000000", g.ContextWindow())
	}
	if g.command != "gemini" {
		t.Errorf("command = %q, want %q", g.command, "gemini")
	}
}

func TestNewGeminiCustomModel(t *testing.T) {
	g := NewGemini("gemini-2.0-flash", 0)
	if g.model != "gemini-2.0-flash" {
		t.Errorf("model = %q, want %q", g.model, "gemini-2.0-flash")
	}
}

func TestNewGeminiCustomWindow(t *testing.T) {
	g := NewGemini("", 500000)
	if g.ContextWindow() != 500000 {
		t.Errorf("context window = %d, want 500000", g.ContextWindow())
	}
}

func TestGeminiImplementsAgent(t *testing.T) {
	var _ Agent = (*Gemini)(nil)
}
