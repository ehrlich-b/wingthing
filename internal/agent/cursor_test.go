package agent

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
)

func TestNewCursorDefaults(t *testing.T) {
	c := NewCursor(0)
	if c.ContextWindow() != 128000 {
		t.Errorf("context window = %d, want 128000", c.ContextWindow())
	}
	if c.command != "agent" {
		t.Errorf("command = %q, want %q", c.command, "agent")
	}
}

func TestNewCursorCustomWindow(t *testing.T) {
	c := NewCursor(64000)
	if c.ContextWindow() != 64000 {
		t.Errorf("context window = %d, want 64000", c.ContextWindow())
	}
}

func TestCursorImplementsAgent(t *testing.T) {
	var _ Agent = (*Cursor)(nil)
}

func TestCursorRunCommandContract(t *testing.T) {
	cursor := NewCursor(0)
	var gotName string
	var gotArgs []string
	stream, err := cursor.Run(context.Background(), "hello cursor", RunOpts{
		CmdFactory: func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			gotName = name
			gotArgs = append([]string(nil), args...)
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"cursor output"}]}}'`), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-p", "hello cursor", "--output-format", "stream-json"}
	if gotName != "agent" || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("invocation = %q %q, want agent %q", gotName, gotArgs, wantArgs)
	}
	if got := stream.Text(); got != "cursor output" {
		t.Fatalf("output = %q", got)
	}
}

// Cursor reuses Claude's parseStreamEvent — verify it works for cursor-format events too.
func TestCursorParsesClaudeStreamFormat(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hello from Cursor"}]}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}`,
	}
	expected := []string{"Hello from Cursor", " world"}
	for i, line := range lines {
		text, ok := parseStreamEvent(line)
		if !ok {
			t.Fatalf("line %d: expected ok", i)
		}
		if text != expected[i] {
			t.Errorf("line %d: text = %q, want %q", i, text, expected[i])
		}
	}
}
