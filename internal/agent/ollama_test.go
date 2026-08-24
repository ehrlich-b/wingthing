package agent

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
)

func TestNewOllamaDefaults(t *testing.T) {
	o := NewOllama("", 0)
	if o.model != DefaultOllamaModel {
		t.Errorf("model = %q, want %q", o.model, DefaultOllamaModel)
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

func TestOllamaRunCommandContract(t *testing.T) {
	ollama := NewOllama("mistral", 0)
	var gotName string
	var gotArgs []string
	stream, err := ollama.Run(context.Background(), "hello ollama", RunOpts{
		CmdFactory: func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			gotName = name
			gotArgs = append([]string(nil), args...)
			return exec.CommandContext(ctx, "sh", "-c", "cat"), nil
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
	if gotName != "ollama" || !reflect.DeepEqual(gotArgs, []string{"run", "mistral"}) {
		t.Fatalf("invocation = %q %q", gotName, gotArgs)
	}
	if got := stream.Text(); got != "hello ollama\n" {
		t.Fatalf("output = %q", got)
	}
}
