package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

func TestSandboxLaunchDiagnosticRetainsRepairGuidance(t *testing.T) {
	cause := errors.New("private provider output and configuration")
	diagnostic := LaunchDiagnostic{Stage: "provider startup", Reason: "sandbox_unavailable"}
	original := WithLaunchDiagnostic(cause, diagnostic)
	remote := ToolError(ErrorResult(original))
	for _, message := range []string{original.Error(), remote.Error(), FormatLaunchDiagnostic(diagnostic)} {
		if !strings.Contains(message, "sandbox") || !strings.Contains(message, "wt doctor --fix") {
			t.Fatalf("sandbox failure lost repair instructions: %s", message)
		}
		if strings.Contains(message, cause.Error()) {
			t.Fatalf("sandbox diagnostic exposed private cause: %s", message)
		}
	}
	if !errors.Is(original, cause) {
		t.Fatal("diagnostic lost local cause")
	}
}

func TestDirectResponseRestoresSentinels(t *testing.T) {
	for _, tc := range []struct {
		kind ErrorKind
		err  error
	}{
		{ErrorSessionNameInUse, ErrSessionNameInUse},
		{ErrorNotFound, fs.ErrNotExist},
		{ErrorNoRows, sql.ErrNoRows},
		{ErrorCanceled, context.Canceled},
		{ErrorDeadlineExceeded, context.DeadlineExceeded},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			original := fmt.Errorf("tool context: %w", tc.err)
			for _, response := range []DirectResponse{
				{IsError: true, ErrorKind: ErrorKindOf(original), Result: ErrorResult(original)},
				{IsError: true, Result: ErrorResult(original)},
				{ErrorKind: ErrorKindOf(original), Error: original.Error()},
			} {
				payload, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				var received DirectResponse
				if err := json.Unmarshal(payload, &received); err != nil {
					t.Fatal(err)
				}
				if err := received.Err(); !errors.Is(err, tc.err) || err.Error() != original.Error() {
					t.Fatalf("decoded %s: %v; want text %q and sentinel %v", payload, err, original.Error(), tc.err)
				}
			}
		})
	}
}

func TestDirectResponseUnknownAndLegacyErrors(t *testing.T) {
	message := ErrSessionNameInUse.Error()
	for _, kind := range []ErrorKind{"", "future_kind"} {
		response := DirectResponse{Error: message, ErrorKind: kind}
		if err := response.Err(); err == nil || err.Error() != message || errors.Is(err, ErrSessionNameInUse) {
			t.Fatalf("kind %q: %v; unknown kinds must retain text without guessing identity", kind, err)
		}
	}
	legacy := DirectResponse{IsError: true, Result: map[string]any{"error": message}}
	if err := legacy.Err(); err != nil {
		t.Fatalf("legacy tool failure changed semantics: %v", err)
	}
	unknown := DirectResponse{IsError: true, Result: map[string]any{"error": message, "error_kind": "future_kind"}}
	if err := unknown.Err(); err == nil || err.Error() != message || errors.Is(err, ErrSessionNameInUse) {
		t.Fatalf("unknown tool failure: %v", err)
	}
	if kind := ErrorKindOf(errors.New(message)); kind != "" {
		t.Fatalf("classified plain text as %q", kind)
	}
}
