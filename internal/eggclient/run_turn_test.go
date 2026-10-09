package eggclient

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

func TestArchivedRunTurnMethodsKeepFullResultAndRejectTraversal(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	session := LocalSession{ID: "fixture", Agent: "claude"}
	dir := filepath.Join(cfg.Dir, "eggs", session.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("native Ω final", 100000)
	result := egg.RunTurnResult{RunID: "run-exact", SessionID: session.ID, Status: "done", Text: text, ProviderSessionID: "provider-exact", StartedAt: time.Now().UTC(), EndedAt: time.Now().UTC()}
	wire, _ := json.Marshal(map[string]any{"version": 1, "result": result})
	path := filepath.Join(dir, fmt.Sprintf("run.%x.json", sha256.Sum256([]byte(result.RunID))))
	if err := os.WriteFile(path, wire, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	read, err := ReadRunTurnResult(ctx, cfg, session, result.RunID)
	if err != nil || read.Text != text || read.ProviderSessionID != result.ProviderSessionID {
		t.Fatalf("archive read: bytes=%d %v", len(read.Text), err)
	}
	for _, method := range []func(context.Context, *config.Config, LocalSession, string) (egg.RunTurnResult, error){RunTurnStatus, WaitRunTurn} {
		status, err := method(ctx, cfg, session, result.RunID)
		if err != nil || status.Status != "done" || status.Text != "" {
			t.Fatalf("archive observation: %s %v", status.Status, err)
		}
	}
	if _, err := StopRunTurn(ctx, cfg, session, result.RunID); err == nil {
		t.Fatal("archived session accepted stop")
	}
	if _, err := SubmitRunTurn(ctx, cfg, session, egg.RunTurnRequest{RunID: "another", Prompt: "fixture", Deadline: time.Now().Add(time.Hour)}); err == nil {
		t.Fatal("archive spawned or accepted execution")
	}
	session.ID = "../fixture"
	if _, err := ReadRunTurnResult(ctx, cfg, session, result.RunID); err == nil {
		t.Fatal("archive fallback accepted session path traversal")
	}
}
