package agent

import (
	"testing"
)

func TestNewOllamaDefaults(t *testing.T) {
	o := NewOllama("", 0)
	if o.model != "llama3.2" {
		t.Errorf("model = %q, want %q", o.model, "llama3.2")
	}
	if o.ContextWindow() != 128000 {
		t.Errorf("context window = %d, want 128000", o.ContextWindow())
	}
	if o.command != "ollama" {
		t.Errorf("command = %q, want %q", o.command, "ollama")
	}
}

func TestNewOllamaCustomModel(t *testing.T) {
	o := NewOllama("mistral", 0)
	if o.model != "mistral" {
		t.Errorf("model = %q, want %q", o.model, "mistral")
	}
}

func TestNewOllamaCustomWindow(t *testing.T) {
	o := NewOllama("", 64000)
	if o.ContextWindow() != 64000 {
		t.Errorf("context window = %d, want 64000", o.ContextWindow())
	}
}

func TestOllamaImplementsAgent(t *testing.T) {
	var _ Agent = (*Ollama)(nil)
}
