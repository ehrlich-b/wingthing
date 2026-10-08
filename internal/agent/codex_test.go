package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
	wantArgs := []string{"exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-m", "gpt-5.6-terra", "--json", "--", "hello codex"}
	if gotName != "codex" || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("invocation = %q %q, want codex %q", gotName, gotArgs, wantArgs)
	}
	if got := stream.Text(); got != "codex output" {
		t.Fatalf("output = %q", got)
	}
}

func TestCodexRunUsesExplicitPromptAndNullStdin(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
if [ ! /dev/fd/0 -ef /dev/null ]; then
    echo 'stdin is not /dev/null' >&2
    exit 1
fi
if IFS= read -r input || [ -n "$input" ]; then
    echo 'stdin contains input' >&2
    exit 1
fi
previous=
last=
for arg do
    previous=$last
    last=$arg
done
if [ "$previous" != -- ] || [ -z "$last" ] || [ "$last" = - ] || [ "$last" != "$WT_TEST_CODEX_PROMPT" ]; then
    echo 'prompt was not passed explicitly' >&2
    exit 1
fi
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"review complete"}}'
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"Review this branch without editing.\nReport findings only.", "--review without editing", "-"} {
		for _, mode := range []string{"direct", "empty-pipe", "nonempty-pipe"} {
			t.Run(prompt+"/"+mode, func(t *testing.T) {
				wantPrompt := prompt
				if wantPrompt == "-" {
					wantPrompt += "\n"
				}
				t.Setenv("WT_TEST_CODEX_PROMPT", wantPrompt)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var opts RunOpts
				if mode != "direct" {
					reader, writer, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					defer reader.Close()
					defer writer.Close()
					if mode == "nonempty-pipe" {
						if _, err := writer.WriteString("coordinator input\n"); err != nil {
							t.Fatal(err)
						}
					}
					opts.CmdFactory = func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
						cmd := exec.CommandContext(ctx, name, args...)
						cmd.Stdin = reader
						return cmd, nil
					}
				}
				codex := NewCodex(0)
				codex.command = binary
				stream, err := codex.Run(ctx, prompt, opts)
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
				if got := stream.Text(); got != "review complete" {
					t.Fatalf("output = %q", got)
				}
			})
		}
	}
}

func TestCodexRunRejectsEmptyPrompt(t *testing.T) {
	for _, prompt := range []string{"", " \n\t"} {
		_, err := NewCodex(0).Run(context.Background(), prompt, RunOpts{
			CmdFactory: func(context.Context, string, []string) (*exec.Cmd, error) {
				t.Fatal("empty prompt reached command factory")
				return nil, nil
			},
		})
		if err == nil || err.Error() != "prompt is required" {
			t.Fatalf("empty prompt error = %v", err)
		}
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

func TestCodexFinalProviderError(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		exit         int
		message      string
	}{
		{"turn-failed", `{"type":"turn.failed","error":{"message":"This content was flagged for possible cybersecurity risk."}}`, 1, "This content was flagged for possible cybersecurity risk."},
		{"error-event", `{"type":"error","message":"Provider refused the request."}`, 1, "Provider refused the request."},
		{"error-item", `{"type":"item.completed","item":{"type":"error","message":"Provider exhausted the quota."}}`, 1, "Provider exhausted the quota."},
		{"zero-exit-refusal", `{"type":"turn.failed","error":{"message":"Provider refused the request."}}`, 0, "Provider refused the request."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := NewCodex(0).Run(context.Background(), "review", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"last agent message"}}' "$EVENTS"; echo 'Reading additional input from stdin...' >&2; exit "$EXIT"`)
				cmd.Env = append(os.Environ(), "EVENTS="+tc.events, fmt.Sprintf("EXIT=%d", tc.exit))
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
			if stream.Err() == nil || !strings.Contains(stream.Err().Error(), tc.message) {
				t.Fatalf("provider error = %v", stream.Err())
			}
			if stream.Text() != "last agent message" {
				t.Fatalf("last message = %q", stream.Text())
			}
			if tc.exit != 0 {
				var exitErr *exec.ExitError
				if !errors.As(stream.Err(), &exitErr) || exitErr.ExitCode() != tc.exit {
					t.Fatalf("provider exit lost: %v", stream.Err())
				}
			}
		})
	}
}

func TestCodexRecoveredErrorDoesNotFailCompletedTurn(t *testing.T) {
	stream := runFakeProvider(t, NewCodex(0), `{"type":"error","message":"Reconnecting 1/5"}`+"\n"+`{"type":"item.completed","item":{"type":"agent_message","text":"recovered"}}`+"\n"+`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`)
	if err := stream.Err(); err != nil || stream.Text() != "recovered" {
		t.Fatalf("recovered turn: %q %v", stream.Text(), err)
	}
}

func TestCodexRedactsProviderSecrets(t *testing.T) {
	const canary = "opaque-provider-canary-7Qn3"
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			stream, err := NewCodex(0).Run(context.Background(), "review", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
				cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `printf '{"type":"item.completed","item":{"type":"agent_message","text":"diagnostic %s"}}\n' "$CUSTOM_PASSWORD"; if [ "$FAIL" = true ]; then printf '{"type":"turn.failed","error":{"message":"provider refused %s"}}\n' "$CUSTOM_PASSWORD"; printf 'stderr %s' "$CUSTOM_PASSWORD" >&2; exit 7; fi`)
				cmd.Env = []string{"CUSTOM_PASSWORD=" + canary, fmt.Sprintf("FAIL=%t", failure)}
				return cmd, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			for {
				chunk, ok := stream.Next()
				if !ok {
					break
				}
				if strings.Contains(chunk.Text, canary) || !strings.Contains(chunk.Text, "[redacted]") {
					t.Error("provider chunk exposed a credential")
				}
			}
			if strings.Contains(stream.Text(), canary) {
				t.Error("provider transcript exposed a credential")
			}
			if failure {
				if stream.Err() == nil || strings.Contains(stream.Err().Error(), canary) || !strings.Contains(stream.Err().Error(), "provider refused [redacted]") || !strings.Contains(stream.Err().Error(), "stderr [redacted]") {
					t.Error("provider failure exposed a credential or lost diagnostics")
				}
				var exitErr *exec.ExitError
				if !errors.As(stream.Err(), &exitErr) || exitErr.ExitCode() != 7 {
					t.Error("redaction lost the provider exit code")
				}
			} else if stream.Err() != nil {
				t.Fatal(stream.Err())
			}
		})
	}
}
