package egg

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

func TestRunRecoveryProviderFailureKeepsSafePartialResult(t *testing.T) {
	for _, test := range []struct {
		message string
		kind    agent.ErrorKind
	}{
		{"Invalid API key", agent.AuthFailed},
		{"429 Too Many Requests: rate limit exceeded", agent.RateLimited},
		{"content_policy_violation", agent.ProviderRefused},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			f := newRunFixture(t)
			if _, err := f.runtime.submit(f.request); err != nil {
				t.Fatal(err)
			}
			<-f.sent
			const partial = "Completed the first safe step Ω."
			const secret = "private-failure-credential-canary"
			failure, _ := json.Marshal(map[string]any{"type": "assistant", "sessionId": "ours", "error": test.message, "message": map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "text", "text": test.message + " " + secret}}}})
			records := append(assistantRecord(partial, ""), failure...)
			appendNative(t, f.path, append(records, '\n'))
			result := waitRunFixture(t, f)
			data, err := os.ReadFile(runTurnPath(f.runtime.dir, f.request.RunID))
			if err != nil || result.Status != "failed" || result.FailureKind != test.kind || result.Text != partial || strings.Contains(string(data), secret) {
				t.Fatalf("failed run lost safe partial output or exposed diagnostics: %+v, %v", result, err)
			}
		})
	}
}
