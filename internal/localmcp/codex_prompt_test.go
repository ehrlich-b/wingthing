package localmcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestMCPInitialCodexPromptLimit(t *testing.T) {
	f := newRunWingFixture(t, nil)
	for _, prompt := range []string{strings.Repeat("x", 140*1024), strings.Repeat("Ω", egg.MaxInitialCodexPromptBytes/2+1)} {
		wire, err := json.Marshal(map[string]any{"agent": "codex", "prompt": prompt, "cwd": f.server.Cfg.Dir, "idempotency_key": "retry"})
		if err != nil {
			t.Fatal(err)
		}
		// Exercise plain MCP and task-augmented MCP, including retries.
		for _, task := range []*control.MCPTaskParams{nil, {TTL: new(int64(60000))}, nil} {
			data, err := f.server.toolAgentRunTask(wire, task)
			var sizeErr *egg.InitialCodexPromptTooLargeError
			if data != nil || !errors.As(err, &sizeErr) || sizeErr.SizeBytes != len(prompt) || sizeErr.LimitBytes != egg.MaxInitialCodexPromptBytes {
				t.Fatalf("MCP rejection: %v", err)
			}
		}
	}
	select {
	case <-f.spawned:
		t.Fatal("MCP rejection spawned a session")
	default:
	}
	if _, err := os.Stat(filepath.Join(f.server.Cfg.Dir, "eggs")); !os.IsNotExist(err) {
		t.Fatalf("MCP rejection created session state: %v", err)
	}
	db, err := store.Open(f.server.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"tasks", "agent_runs", "task_log"} {
		var count int
		if err := db.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("MCP rejection retained %s: %d %v", table, count, err)
		}
	}
	// A rejected key can be corrected, and accepted retries reuse its identity.
	wire, _ := json.Marshal(map[string]any{"agent": "codex", "prompt": strings.Repeat("Ω", egg.MaxInitialCodexPromptBytes/2), "cwd": f.server.Cfg.Dir, "idempotency_key": "retry"})
	a, err := f.server.toolAgentRun(wire)
	if err != nil {
		t.Fatal(err)
	}
	id := <-f.submitted
	b, err := f.server.toolAgentRun(wire)
	if err != nil || a["run_id"] != b["run_id"] || a["session_id"] != b["session_id"] {
		t.Fatalf("MCP boundary retry: %v", err)
	}
	<-f.spawned
	select {
	case <-f.spawned:
		t.Fatal("MCP retry spawned a duplicate")
	default:
	}
	f.finish(t, id, "done", "")
	f.wait(t, id)
	for _, table := range []string{"tasks", "agent_runs"} {
		var count int
		if err := db.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("MCP retry duplicated %s: %d %v", table, count, err)
		}
	}
}

func TestMCPClaudePromptUnaffectedByInitialCodexLimit(t *testing.T) {
	f := newRunWingFixture(t, nil)
	id := f.admit(t, strings.Repeat("x", 140*1024))
	if submitted := <-f.submitted; submitted != id {
		t.Fatal("Claude prompt was not submitted")
	}
	f.finish(t, id, "done", "")
	f.wait(t, id)
}
