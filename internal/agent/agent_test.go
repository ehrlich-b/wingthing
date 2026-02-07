package agent

import (
	"context"
	"testing"
)

func TestParseStreamEventAssistant(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}`
	text, ok := parseStreamEvent(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if text != "Hello" {
		t.Errorf("text = %q, want %q", text, "Hello")
	}
}

func TestParseStreamEventDelta(t *testing.T) {
	line := `{"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}`
	text, ok := parseStreamEvent(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if text != " world" {
		t.Errorf("text = %q, want %q", text, " world")
	}
}

func TestParseStreamEventStop(t *testing.T) {
	cases := []string{
		`{"type":"content_block_stop"}`,
		`{"type":"message_stop"}`,
	}
	for _, line := range cases {
		_, ok := parseStreamEvent(line)
		if ok {
			t.Errorf("expected no text for %s", line)
		}
	}
}

func TestParseStreamEventGarbage(t *testing.T) {
	_, ok := parseStreamEvent("not json at all")
	if ok {
		t.Error("expected no text for garbage input")
	}
}

func TestParseStreamEventEmptyText(t *testing.T) {
	line := `{"type":"content_block_delta","delta":{"type":"text_delta","text":""}}`
	_, ok := parseStreamEvent(line)
	if ok {
		t.Error("expected no text for empty delta")
	}
}

func TestParseStreamEventNonTextDelta(t *testing.T) {
	line := `{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"foo\""}}`
	_, ok := parseStreamEvent(line)
	if ok {
		t.Error("expected no text for non-text delta")
	}
}

func TestParseStreamEventAssistantMultipleBlocks(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","text":""},{"type":"text","text":"Found it"}]}}`
	text, ok := parseStreamEvent(line)
	if !ok {
		t.Fatal("expected ok")
	}
	if text != "Found it" {
		t.Errorf("text = %q, want %q", text, "Found it")
	}
}

func TestStreamCollectsText(t *testing.T) {
	ctx := context.Background()
	s := newStream(ctx)

	go func() {
		s.send(Chunk{Text: "Hello"})
		s.send(Chunk{Text: " "})
		s.send(Chunk{Text: "world"})
		s.close(nil)
	}()

	for {
		_, ok := s.Next()
		if !ok {
			break
		}
	}

	if got := s.Text(); got != "Hello world" {
		t.Errorf("text = %q, want %q", got, "Hello world")
	}
	if err := s.Err(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStreamContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newStream(ctx)

	cancel()

	// send should not block when context is cancelled
	s.send(Chunk{Text: "dropped"})
	s.close(ctx.Err())

	if err := s.Err(); err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestNewClaudeDefaults(t *testing.T) {
	c := NewClaude(0)
	if c.ContextWindow() != 200000 {
		t.Errorf("context window = %d, want 200000", c.ContextWindow())
	}
}

func TestNewClaudeCustomWindow(t *testing.T) {
	c := NewClaude(128000)
	if c.ContextWindow() != 128000 {
		t.Errorf("context window = %d, want 128000", c.ContextWindow())
	}
}

func TestParseStreamFullSequence(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"!"}}`,
		`{"type":"content_block_stop"}`,
		`{"type":"message_stop"}`,
	}

	var collected string
	for _, line := range lines {
		if text, ok := parseStreamEvent(line); ok {
			collected += text
		}
	}

	if collected != "Hello world!" {
		t.Errorf("collected = %q, want %q", collected, "Hello world!")
	}
}
