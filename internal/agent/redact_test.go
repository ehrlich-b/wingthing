package agent

import (
	"context"
	"encoding/base64"
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
		"CUSTOM_AUTH=auth-canary",
		"DB_PASS=pass-canary",
		"DATABASE_URL=postgres://user:database-canary@localhost/db?token=query-canary",
		"CONNECTION=postgres://user:encoded%3Fcanary@localhost/db?token=url%2Bcanary",
		"PATH=/safe/bin",
	}, "helper-canary", "escaped\"canary")
	for _, secret := range []string{
		"opaque-env-canary", "short-longer", "short", "session-canary", "auth-canary", "pass-canary", "database-canary", "query-canary", "helper-canary", "escaped\"canary", `escaped\"canary`, "encoded?canary", "url+canary", "postgres://user:encoded%3Fcanary@localhost/db?token=url%2Bcanary",
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

func encodedCredentialCases() []struct{ name, secret, encoded string } {
	return []struct{ name, secret, encoded string }{
		{"percent", "opaque+credential/7Qn3?value=9&more!", "opaque%2Bcredential%2F7Qn3%3Fvalue%3D9%26more%21"},
		{"percent-lower", "opaque+credential/7Qn3?value=9&more!", "opaque%2bcredential%2f7Qn3%3fvalue%3d9%26more%21"},
		{"query", "opaque credential/7Qn3", "opaque+credential%2F7Qn3"},
		{"json", "opaque\"credential\\7Qn3\nvalue", `opaque\"credential\\7Qn3\nvalue`},
		{"json-ascii", "opäque\"credential\\7Qn3", `op\u00e4que\"credential\\7Qn3`},
		{"json-ascii-upper", "opäque\"credential\\7Qn3", `op\u00E4que\"credential\\7Qn3`},
		{"json-surrogate", "opaque🔐credential7Qn3", `opaque\ud83d\udd10credential7Qn3`},
		{"unicode-all", "opaque-canary", `\u006f\u0070\u0061\u0071\u0075\u0065\u002d\u0063\u0061\u006e\u0061\u0072\u0079`},
		{"percent-all", "opaque-canary", `%6F%70%61%71%75%65%2D%63%61%6E%61%72%79`},
		{"json-solidus", "opaque/credential7Qn3", `opaque\/credential7Qn3`},
		{"base64", "opaque+credential/7Qn3", base64.StdEncoding.EncodeToString([]byte("opaque+credential/7Qn3"))},
		{"base64-raw", "opaque+credential/7Qn3", base64.RawStdEncoding.EncodeToString([]byte("opaque+credential/7Qn3"))},
		{"base64-url", "opaque🔐credential7Qn3", base64.URLEncoding.EncodeToString([]byte("opaque🔐credential7Qn3"))},
	}
}

func TestRedactorEncodedCredentials(t *testing.T) {
	for _, tc := range encodedCredentialCases() {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRedactor([]string{"CUSTOM_PASSWORD=" + tc.secret})
			text := "before " + tc.encoded + " after"
			if got := r.Text(text); got != "before [redacted] after" {
				t.Fatalf("encoded credential survived: %q", got)
			}
			cause := errors.New(text)
			if err := r.Error(cause); err.Error() != "before [redacted] after" || !errors.Is(err, cause) {
				t.Fatal("encoded error exposed a credential or lost its identity")
			}
			for split := 1; split < len(tc.encoded); split++ {
				s := newStream(context.Background())
				s.redactor = r
				s.send(Chunk{Text: "before " + tc.encoded[:split]})
				s.send(Chunk{Text: tc.encoded[split:] + " after"})
				s.close(nil)
				var chunks strings.Builder
				for c, ok := s.Next(); ok; c, ok = s.Next() {
					chunks.WriteString(c.Text)
				}
				if chunks.String() != "before [redacted] after" {
					t.Fatalf("split %d exposed encoded credential: %q", split, chunks.String())
				}
			}
		})
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

func TestStreamRedactsCredentialsSplitAcrossChunks(t *testing.T) {
	const canary = "7Qn3-split-provider-credential"
	t.Setenv("OPENAI_API_KEY", canary)
	t.Setenv("CUSTOM_PASSWORD", canary[:12])
	s := newStream(context.Background())
	s.send(Chunk{Text: "provider key " + canary[:5]})
	s.send(Chunk{Text: canary[5:20]})
	s.send(Chunk{Text: canary[20:] + " safe tail"})
	s.close(nil)
	var returned strings.Builder
	for {
		chunk, ok := s.Next()
		if !ok {
			break
		}
		returned.WriteString(chunk.Text)
	}
	if got := returned.String(); got != "provider key [redacted] safe tail" || s.Text() != got {
		t.Fatal("split credential reached returned chunks or the transcript")
	}
}

func TestStreamFlushesHeldTailOnCancellationWithFullQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newStream(ctx)
	s.redactor = NewRedactor([]string{"OPENAI_API_KEY=7Qn3-provider-credential"})
	for i := 0; i < cap(s.ch); i++ {
		s.send(Chunk{Text: "safe "})
	}
	s.send(Chunk{Text: "7Qn3"})
	cancel()
	// close must neither block on the full queue nor discard the held tail.
	s.close(context.Canceled)
	var output strings.Builder
	for {
		c, ok := s.Next()
		if !ok {
			break
		}
		output.WriteString(c.Text)
	}
	want := strings.Repeat("safe ", cap(s.ch)) + "7Qn3"
	if output.String() != want || s.Text() != want {
		t.Fatal("cancellation discarded already received partial output")
	}
	if !errors.Is(s.Err(), context.Canceled) {
		t.Fatal("tail flush lost cancellation identity")
	}
}
