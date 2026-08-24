package agent

import (
	"context"
	"os/exec"
	"reflect"
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

func TestOpenCodeRunCommandContract(t *testing.T) {
	o := NewOpenCode(0)
	var gotName string
	var gotArgs []string
	var command *exec.Cmd
	workDir := t.TempDir()

	stream, err := o.Run(context.Background(), "hello opencode", RunOpts{
		WorkDir: workDir,
		CmdFactory: func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			gotName = name
			gotArgs = append([]string(nil), args...)
			command = exec.CommandContext(ctx, "sh", "-c", "printf 'opencode output\\n'")
			return command, nil
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

	wantArgs := []string{"run", "--auto", "--dir", workDir, "hello opencode"}
	if gotName != "opencode" || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("command = %q %q, want opencode %q", gotName, gotArgs, wantArgs)
	}
	if command.Dir != workDir {
		t.Fatalf("working directory = %q, want %q", command.Dir, workDir)
	}
	if got := stream.Text(); got != "opencode output\n" {
		t.Fatalf("text = %q", got)
	}
}
