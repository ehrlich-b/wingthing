package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestProviderFailureKindsDiscardMessages(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		kind          ErrorKind
	}{
		{"refusal", "This content was flagged for possible cybersecurity risk.", ProviderRefused},
		{"auth", "Invalid API key · Please run /login", AuthFailed},
		{"rate", "429 Too Many Requests: rate limit exceeded", RateLimited},
		{"context", "Your input exceeds the context window of this model.", ContextExhausted},
		{"sandbox", "sandbox-exec: Operation not permitted", SandboxDenied},
		{"unknown", "Unexpected upstream disconnect", ProviderError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := tc.message + " private-provider-canary"
			codex, _ := json.Marshal(map[string]any{"type": "turn.failed", "error": map[string]string{"message": message}})
			claude, _ := json.Marshal(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "errors": []string{message}})
			for _, event := range []string{string(codex), string(claude)} {
				var kind ErrorKind
				var ok bool
				if event == string(codex) {
					kind, ok = parseCodexFailure(event)
				} else {
					kind, ok = parseClaudeFailure(event)
				}
				if !ok || kind != tc.kind {
					t.Fatalf("classification = %q, %v; want %q", kind, ok, tc.kind)
				}
				if text := (&Failure{Kind: kind, Provider: "fixture"}).Error(); strings.Contains(text, message) || strings.Contains(text, "private-provider-canary") {
					t.Fatal("provider text escaped classification")
				}
			}
			cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", `printf '%s' "$DIAGNOSTIC" >&2; exit 1`)
			cmd.Env = append(os.Environ(), "DIAGNOSTIC="+message)
			diagnostics, err := startAgentCommand(cmd)
			if err != nil {
				t.Fatal(err)
			}
			err = waitAgentCommand(cmd, diagnostics, "fixture")
			want := tc.kind
			if want == ProviderError {
				want = ProviderExit // No failure event, only an unknown nonzero exit.
			}
			if FailureKind(err) != want || strings.Contains(err.Error(), "private-provider-canary") || diagnostics.stderr.String() != "" {
				t.Fatalf("stderr classification = %v; want %q", err, want)
			}
		})
	}
}

func TestClaudeFailureEventsAreNotAssistantOutput(t *testing.T) {
	for _, event := range []string{
		`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["Invalid API key private-provider-canary"]}`,
		`{"type":"error","error":{"type":"authentication_error","message":"Invalid API key private-provider-canary"}}`,
		`{"type":"assistant","error":"authentication_failed","message":{"content":[{"type":"text","text":"Invalid API key private-provider-canary"}]}}`,
	} {
		stream := runFakeProvider(t, NewClaude(0), event)
		if FailureKind(stream.Err()) != AuthFailed || stream.Text() != "" || strings.Contains(stream.Err().Error(), "private-provider-canary") {
			t.Fatalf("Claude failure escaped: %q %v", stream.Text(), stream.Err())
		}
	}
}

func TestCodexFailureEnvelopeDoesNotDependOnMessageShape(t *testing.T) {
	for _, event := range []string{
		`{"type":"turn.failed","error":{"message":{"unexpected":"private-provider-canary"}}}`,
		`{"type":"error","message":123}`,
		`{"type":"item.completed","item":{"type":"error","message":null}}`,
	} {
		if kind, failed := parseCodexFailure(event); !failed || kind != ProviderError {
			t.Fatalf("failure envelope lost: kind=%q, failed=%v", kind, failed)
		}
	}
	for _, event := range []string{
		`{"type":"turn.failed","error":{"message":"This content was flagged for possible cybersecurit\u0079 risk."}}`,
		`{"type":"error","message":"content\u005fpolicy\u005fviolation"}`,
	} {
		if kind, failed := parseCodexFailure(event); !failed || kind != ProviderRefused {
			t.Fatalf("escaped refusal lost: kind=%q, failed=%v", kind, failed)
		}
	}
}

func TestCodexOversizedFailureKeepsOnlyType(t *testing.T) {
	for _, shape := range [][2]string{
		{`{"type":"turn.failed","error":{"message":"private-provider-canary`, `"}}`},
		{`{"type":"error","message":"private-provider-canary`, `"}`},
		{`{"type":"item.completed","item":{"type":"error","message":"private-provider-canary`, `"}}`},
		{`{"error":{"message":"private-provider-canary`, `"},"type":"turn.failed"}`},
	} {
		stream := runFakeProvider(t, NewCodex(0), sizedEvent(shape[0], shape[1], oversizedTestLine))
		if FailureKind(stream.Err()) != ProviderError || stream.Text() != "" || strings.Contains(stream.Err().Error(), "private-provider-canary") {
			t.Fatalf("oversized failure = %q %v", stream.Text(), stream.Err())
		}
	}
}

func TestCodexFailureKindSurvivesGenericErrorAndExit(t *testing.T) {
	stream, err := NewCodex(0).Run(context.Background(), "review", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%s\n' '{"type":"turn.failed","error":{"message":"This content was flagged for possible cybersecurity risk. private-provider-canary"}}' '{"type":"error","message":"private-provider-canary"}'; echo private-provider-canary >&2; exit 1`), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	if FailureKind(stream.Err()) != ProviderRefused || strings.Contains(stream.Err().Error(), "private-provider-canary") {
		t.Fatalf("failure = %v", stream.Err())
	}
}
