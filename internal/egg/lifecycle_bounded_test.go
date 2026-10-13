package egg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleBoundedReadBudgetCancellationAndJournalIntegrity(t *testing.T) {
	dir, home, cwd, path := lifecycleFixture(t)
	lifecycleWrite(t, path, `{"type":"assistant","sessionId":"ours","message":{"role":"assistant","content":[{"type":"text","text":"native DNS helper"}],"stop_reason":"end_turn"}}`+"\n")
	budget := int64(32)
	if _, err := TryReadSessionLifecycleBounded(t.Context(), dir, "claude", cwd, home, "ours", true, 0, 10, &budget); !errors.Is(err, errLifecycleReadBudget) {
		t.Fatalf("read bound: %v", err)
	}
	if budget != 0 {
		t.Fatalf("budget=%d", budget)
	}
	budget = 32 << 20
	view, err := TryReadSessionLifecycleBounded(t.Context(), dir, "claude", cwd, home, "ours", true, 0, 10, &budget)
	if err != nil || len(view.Events) != 1 || !strings.Contains(view.Events[0].Text, "DNS helper") {
		t.Fatalf("bounded retry: %+v %v", view, err)
	}
	journal, err := os.ReadFile(filepath.Join(dir, "lifecycle.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	budget = 16
	if _, err := TryReadSessionLifecycleBounded(t.Context(), dir, "claude", cwd, home, "ours", true, 0, 10, &budget); !errors.Is(err, errLifecycleReadBudget) {
		t.Fatal("journal budget ignored", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "lifecycle.jsonl"))
	if string(journal) != string(after) {
		t.Fatal("budget expiration truncated the journal")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	budget = 32 << 20
	if _, err := TryReadSessionLifecycleBounded(ctx, dir, "claude", cwd, home, "ours", true, 0, 10, &budget); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}
