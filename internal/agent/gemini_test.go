package agent

import (
	"context"
	"os/exec"
	"reflect"
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

func TestGeminiRunCommandContract(t *testing.T) {
	g := NewGemini("gemini-test-model", 0)
	var gotName string
	var gotArgs []string

	stream, err := g.Run(context.Background(), "hello gemini", RunOpts{
		CmdFactory: func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			gotName = name
			gotArgs = append([]string(nil), args...)
			return exec.CommandContext(ctx, "sh", "-c", "printf 'gemini output\\n'"), nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream: %v", err)
	}

	wantArgs := []string{"-p", "hello gemini", "--model", "gemini-test-model", "--yolo"}
	if gotName != "gemini" || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("command = %q %q, want gemini %q", gotName, gotArgs, wantArgs)
	}
	if got := stream.Text(); got != "gemini output\n" {
		t.Fatalf("text = %q", got)
	}
}
