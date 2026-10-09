package agent

import (
	"encoding/json"
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
		{"codex usage limit", "You've hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro) or try again at 3:00 PM.", RateLimited},
		{"claude session limit", "You've hit your session limit · resets 3pm", RateLimited},
		{"expired login", "Your access token could not be refreshed because your refresh token has expired. Please log out and sign in again.", AuthFailed},
		{"ids are not status codes", "stream disconnected (request 4c401f2e-9b42-4291-8d4b-0a4290c1e401) at 2026-10-09T19:14:01.401429Z", ProviderError},
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
		})
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
