package agent

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestRedactorCoversEnvironmentCredentialsAndTokenShapes(t *testing.T) {
	r := NewRedactor([]string{
		"OPENAI_API_KEY=opaque-env-canary",
		"CUSTOM_PASSWORD=short",
		"CUSTOM_TOKEN=short-longer",
		"COOKIE=session-canary",
		"DATABASE_URL=postgres://user:database-canary@localhost/db?token=query-canary",
		"PATH=/safe/bin",
	}, "helper-canary", "escaped\"canary")
	for _, secret := range []string{
		"opaque-env-canary", "short-longer", "short", "session-canary", "database-canary", "query-canary", "helper-canary", "escaped\"canary", `escaped\"canary`,
		"sk-proj-unknown-canary-token", "ghp_unknowncanarytoken", "github_pat_unknowncanarytoken", "xoxb-123456789-canary", "AIza012345678901234567890", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYW5hcnkifQ.signature", "Bearer unknown-canary",
	} {
		if got := r.Text("provider refused: " + secret); strings.Contains(got, secret) || !strings.Contains(got, "[redacted]") {
			t.Error("redactor exposed a known credential or token shape")
		}
	}
	if got := r.Text("provider refused /safe/bin: check your account"); got != "provider refused /safe/bin: check your account" {
		t.Fatalf("redaction erased useful diagnostics: %q", got)
	}
	if got := r.Text("short-longer"); got != "[redacted]" {
		t.Fatal("overlapping credentials left a suffix")
	}
}

func TestCommandStreamRedactsCredentialsOutsideEnvironment(t *testing.T) {
	const canary = "egg-helper-secret-canary"
	s := newCommandStream(context.Background(), exec.Command("unused"), RunOpts{Credentials: []string{canary}})
	s.send(Chunk{Text: "last message " + canary})
	cause := errors.New("provider refused " + canary)
	s.close(cause)
	chunk, ok := s.Next()
	if !ok || chunk.Text != "last message [redacted]" || s.Text() != chunk.Text || s.Err().Error() != "provider refused [redacted]" {
		t.Fatal("stream exposed an out-of-environment credential")
	}
	if !errors.Is(s.Err(), cause) {
		t.Fatal("redaction lost error identity")
	}
}
