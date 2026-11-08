package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

const headlessSecret = "private-provider-secret-canary"

func headlessProviders() []struct {
	name string
	a    Agent
} {
	return []struct {
		name string
		a    Agent
	}{
		{"claude", NewClaude(0)},
		{"codex", NewCodex(0)},
		{"cursor", NewCursor(0)},
	}
}

func headlessTextEvent(provider, text string) string {
	if provider == "codex" {
		return fmt.Sprintf(`{"type":"item.completed","item":{"type":"agent_message","text":%q}}`, text)
	}
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`, text)
}

func runHeadlessFixture(t *testing.T, a Agent, events, stderr string, exit int) *Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	stdoutPath := filepath.Join(testsocket.Dir(t), "events.jsonl")
	if err := os.WriteFile(stdoutPath, []byte(events+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stream, err := a.Run(ctx, "fixture", RunOpts{
		CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "sh", "-c", `cat "$1"; printf '%s' "$2" >&2; exit "$3"`, "fixture", stdoutPath, stderr, fmt.Sprint(exit)), nil
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
	return stream
}

func assertHeadlessFailure(t *testing.T, err error, provider string, kind ErrorKind, exit int) {
	t.Helper()
	if err == nil {
		t.Fatal("provider failure became a successful stream")
	}
	if got := FailureKind(err); got != kind {
		t.Fatalf("failure kind = %q, want %q (error: %v)", got, kind, err)
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Provider != provider {
		t.Fatalf("failure = %#v, want provider %q", failure, provider)
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), headlessSecret) {
			t.Fatalf("provider secret leaked in diagnostic: %T", cause)
		}
	}
	var exitErr *exec.ExitError
	if exit == 0 {
		if errors.As(err, &exitErr) {
			t.Fatal("zero-exit failure has an exit error")
		}
	} else if !errors.As(err, &exitErr) || exitErr.ExitCode() != exit || len(exitErr.Stderr) != 0 {
		t.Fatalf("exit cause = %#v, want sanitized exit %d", exitErr, exit)
	}
}

func TestHeadlessFailureEvents(t *testing.T) {
	for _, provider := range headlessProviders() {
		t.Run(provider.name, func(t *testing.T) {
			events := []struct {
				name, event string
				kind        ErrorKind
			}{
				{"result", `{"type":"result","is_error":true,"errors":["authentication failed ` + headlessSecret + `"],"input_tokens":7,"output_tokens":3}`, AuthFailed},
				{"error", `{"type":"error","error":{"type":"rate_limit_error","message":"` + headlessSecret + `"}}`, RateLimited},
				{"assistant", `{"type":"assistant","error":"authentication_error","message":{"content":[{"type":"text","text":"invalid API key ` + headlessSecret + `"}]}}`, AuthFailed},
			}
			if provider.name == "codex" {
				events = []struct {
					name, event string
					kind        ErrorKind
				}{
					{"turn", `{"type":"turn.failed","error":{"message":"authentication failed ` + headlessSecret + `"}}`, AuthFailed},
					{"error", `{"type":"error","message":"context window exceeded ` + headlessSecret + `"}`, ContextExhausted},
					{"item", `{"type":"item.completed","item":{"type":"error","message":"content_policy_violation ` + headlessSecret + `"}}`, ProviderRefused},
					{"unknown", `{"type":"turn.failed","error":{"message":{"unexpected":"` + headlessSecret + `"}}}`, ProviderError},
				}
			}
			for _, tc := range events {
				t.Run(tc.name, func(t *testing.T) {
					// Normal output on either side still streams, but diagnostics do not.
					output := headlessTextEvent(provider.name, "partial ") + "\n" + tc.event + "\n" + headlessTextEvent(provider.name, "answer")
					stream := runHeadlessFixture(t, provider.a, output, headlessSecret, 0)
					assertHeadlessFailure(t, stream.Err(), provider.name, tc.kind, 0)
					if got := stream.Text(); got != "partial answer" {
						t.Fatalf("partial text = %q, want partial answer", got)
					}
					if tc.name == "result" {
						if in, out := stream.Tokens(); in != 7 || out != 3 {
							t.Fatalf("tokens = %d/%d, want 7/3", in, out)
						}
					}
				})
			}
		})
	}
}

func TestHeadlessFailurePrecedenceAndExit(t *testing.T) {
	for _, provider := range headlessProviders() {
		for _, exit := range []int{0, 7} {
			t.Run(fmt.Sprintf("%s/exit%d", provider.name, exit), func(t *testing.T) {
				generic := `{"type":"error","message":"` + headlessSecret + `"}`
				specific := `{"type":"error","message":"rate limit exceeded ` + headlessSecret + `"}`
				stream := runHeadlessFixture(t, provider.a, generic+"\n"+specific+"\n"+generic, "authentication failed "+headlessSecret, exit)
				assertHeadlessFailure(t, stream.Err(), provider.name, RateLimited, exit)
			})
		}
	}
}

func TestHeadlessFailurePreservesReadError(t *testing.T) {
	for _, provider := range headlessProviders() {
		t.Run(provider.name, func(t *testing.T) {
			generic := `{"type":"error","message":"` + headlessSecret + `"}`
			oversized := headlessTextEvent(provider.name, strings.Repeat("x", maxProviderLine))
			stream := runHeadlessFixture(t, provider.a, headlessTextEvent(provider.name, "partial")+"\n"+generic+"\n"+oversized, headlessSecret, 7)
			assertHeadlessFailure(t, stream.Err(), provider.name, ProviderError, 7)
			if !strings.Contains(stream.Err().Error(), "event skipped") {
				t.Fatalf("missing read error: %v", stream.Err())
			}
			if stream.Text() != "partial" {
				t.Fatal("partial output lost on read failure")
			}
		})
	}
}

func TestHeadlessSuccessIgnoresNonFailureContent(t *testing.T) {
	for _, provider := range headlessProviders() {
		t.Run(provider.name, func(t *testing.T) {
			text := "Explaining authentication failed and rate limit errors"
			result := `{"type":"result","is_error":false,"result":"authentication failed","input_tokens":11,"output_tokens":5}`
			if provider.name == "codex" {
				result = `{"type":"turn.completed","usage":{"input_tokens":11,"output_tokens":5}}`
			}
			output := "{invalid JSON\n" + headlessTextEvent(provider.name, text) + "\n" + result + "\n" + `{"type":"unrecognized","error":"authentication failed"}`
			stream := runHeadlessFixture(t, provider.a, output, "authentication failed "+headlessSecret, 0)
			if err := stream.Err(); err != nil {
				t.Fatal(err)
			}
			if got := stream.Text(); got != text {
				t.Fatalf("text = %q, want %q", got, text)
			}
			if in, out := stream.Tokens(); in != 11 || out != 5 {
				t.Fatalf("tokens = %d/%d, want 11/5", in, out)
			}
		})
	}
}
