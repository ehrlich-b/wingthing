package agent

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
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

func TestCodexFinalOutputRequiresSuccessfulCompletedTurn(t *testing.T) {
	progress := `{"type":"item.completed","item":{"type":"agent_message","text":"checking"}}`
	final := `{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"pass\"}"}}`
	completed := `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`
	for _, tt := range []struct {
		name, events, want string
		exit               int
	}{
		{"complete", progress + "\n" + final + "\n" + completed, `{"verdict":"pass"}`, 0},
		{"partial", progress + "\n" + final, "", 0},
		{"failed process", final + "\n" + completed, "", 1},
		{"failed turn", final + "\n" + `{"type":"turn.failed"}`, "", 0},
		{"new incomplete turn", final + "\n" + completed + "\n" + `{"type":"turn.started"}`, "", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stream, err := NewCodex(0).Run(context.Background(), "review", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
				cmd := exec.CommandContext(ctx, "sh", "-c", "printf '%s\\n' \"$1\"; exit \"$2\"", "fixture", tt.events, fmt.Sprint(tt.exit))
				return cmd, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			for {
				if _, ok := stream.Next(); !ok {
					break
				}
			}
			if got := stream.FinalOutput(); got != tt.want {
				t.Fatalf("final = %q, want %q", got, tt.want)
			}
			if strings.Contains(tt.events, "checking") && !strings.Contains(stream.Text(), "checking") {
				t.Fatal("transcript lost progress")
			}
		})
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

func TestCodexRunCommandContract(t *testing.T) {
	codex := NewCodex(0)
	var gotName string
	var gotArgs []string
	stream, err := codex.Run(context.Background(), "hello codex", RunOpts{
		Model: "gpt-5.6-terra",
		CmdFactory: func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			gotName = name
			gotArgs = append([]string(nil), args...)
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"codex output"}}'`), nil
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
	wantArgs := []string{"exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-m", "gpt-5.6-terra", "hello codex", "--json"}
	if gotName != "codex" || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("invocation = %q %q, want codex %q", gotName, gotArgs, wantArgs)
	}
	if got := stream.Text(); got != "codex output" {
		t.Fatalf("output = %q", got)
	}
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
