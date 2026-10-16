package localmcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
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

func TestMCPCodexSteerPromptBudget(t *testing.T) {
	const request = "review"
	const direction = "continue"
	const deferredContextBytes = 8 * 1024
	boundaryResultBytes := egg.MaxInitialCodexPromptBytes - len(wingsession.SteerPrompt(request, "", "", direction))
	boundaryDirectionBytes := egg.MaxInitialCodexPromptBytes - len(wingsession.SteerPrompt(request, "", "", "")) - deferredContextBytes
	for _, test := range []struct {
		name, result, priorError, direction string
		active, reject                      bool
	}{
		{name: "completed oversized result", result: strings.Repeat("x", 20480), direction: direction, reject: true},
		{name: "completed multibyte overflow", result: strings.Repeat("Ω", boundaryResultBytes/2+1), direction: direction, reject: true},
		{name: "completed oversized error", priorError: strings.Repeat("e", 20480), direction: direction, reject: true},
		{name: "completed oversized direction", result: "prior", direction: strings.Repeat("d", egg.MaxInitialCodexPromptBytes), reject: true},
		{name: "completed exact limit", result: strings.Repeat("x", boundaryResultBytes), direction: direction},
		{name: "active oversized result", active: true, result: strings.Repeat("x", 20480), direction: direction},
		{name: "active multibyte context", active: true, result: strings.Repeat("界", 10000), direction: direction},
		{name: "active oversized error", active: true, priorError: strings.Repeat("界", 10000), direction: direction},
		{name: "active exact reserved limit", active: true, result: strings.Repeat("x", 20480), direction: strings.Repeat("d", boundaryDirectionBytes)},
		{name: "active reservation overflow", active: true, direction: strings.Repeat("d", boundaryDirectionBytes+1), reject: true},
		{name: "active oversized direction", active: true, direction: strings.Repeat("d", egg.MaxInitialCodexPromptBytes), reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRunWingFixture(t, nil)
			wire, _ := json.Marshal(map[string]any{"agent": "codex", "prompt": request, "cwd": f.server.Cfg.Dir})
			parentData, err := f.server.toolAgentRun(wire)
			if err != nil {
				t.Fatal(err)
			}
			parent := parentData["run_id"].(string)
			if id := <-f.submitted; id != parent {
				t.Fatal("wrong parent submitted")
			}
			<-f.spawned
			finishParent := func() {
				f.mu.Lock()
				e := f.eggs[parent]
				f.mu.Unlock()
				e.mu.Lock()
				e.result.Error = test.priorError
				e.mu.Unlock()
				f.finish(t, parent, test.result, "")
				f.wait(t, parent)
			}
			if !test.active {
				finishParent()
			}
			wire, _ = json.Marshal(map[string]any{"run_id": parent, "prompt": test.direction, "idempotency_key": "steer-retry"})
			childData, err := f.server.toolAgentSteer(wire)
			if test.reject {
				wantSize := len(wingsession.SteerPrompt(request, test.result, test.priorError, test.direction))
				if test.active {
					wantSize = len(wingsession.SteerPrompt(request, "", "", test.direction)) + deferredContextBytes
				}
				for range 2 {
					var sizeErr *egg.InitialCodexPromptTooLargeError
					if childData != nil || !errors.As(err, &sizeErr) || sizeErr.SizeBytes != wantSize || sizeErr.LimitBytes != egg.MaxInitialCodexPromptBytes {
						t.Fatalf("steer rejection: %v, want size %d", err, wantSize)
					}
					childData, err = f.server.toolAgentSteer(wire)
				}
				db, err := store.Open(f.server.Cfg.DBPath())
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				for _, table := range []string{"tasks", "agent_runs"} {
					var count int
					if err := db.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
						t.Fatalf("rejected steer retained %s: %d %v", table, count, err)
					}
				}
				var count int
				if err := db.DB().QueryRow("SELECT COUNT(*) FROM task_log WHERE task_id<>?", parent).Scan(&count); err != nil || count != 0 {
					t.Fatalf("rejected steer retained logs: %d %v", count, err)
				}
				entries, err := os.ReadDir(filepath.Join(f.server.Cfg.Dir, "eggs"))
				if err != nil || len(entries) != 1 {
					t.Fatalf("rejected steer created egg state: %v", err)
				}
				if test.active {
					finishParent()
				}
				select {
				case <-f.spawned:
					t.Fatal("rejected steer spawned")
				default:
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			child := childData["run_id"].(string)
			checkRetry := func() {
				t.Helper()
				retry, err := f.server.toolAgentSteer(wire)
				if err != nil || retry["run_id"] != child || retry["session_id"] != childData["session_id"] {
					t.Fatalf("steer retry: %v %v", retry, err)
				}
			}
			checkRetry()
			if test.active {
				select {
				case <-f.submitted:
					t.Fatal("child submitted before parent finished")
				default:
				}
				finishParent()
			}
			if id := <-f.submitted; id != child {
				t.Fatal("wrong child submitted")
			}
			opts := <-f.spawned
			if opts.Egg.InitialRun == nil || len(opts.Egg.InitialRun.Prompt) > egg.MaxInitialCodexPromptBytes || !utf8.ValidString(opts.Egg.InitialRun.Prompt) {
				t.Fatal("child initial prompt exceeds budget or splits UTF-8")
			}
			prompt := opts.Egg.InitialRun.Prompt
			if !strings.Contains(prompt, "Prior request:\n"+request) || !strings.HasSuffix(prompt, "New direction:\n"+test.direction) {
				t.Fatal("follow-up lost request or direction")
			}
			if test.active && !strings.Contains(prompt, "[Wingthing truncated the prior context for this follow-up.]") {
				t.Fatal("missing deferred context truncation marker")
			}
			if !test.active && len(prompt) != egg.MaxInitialCodexPromptBytes {
				t.Fatal("exact-limit follow-up changed")
			}
			f.finish(t, child, "done", "")
			f.wait(t, child)
			checkRetry()
			select {
			case <-f.spawned:
				t.Fatal("retry spawned a duplicate child")
			default:
			}
		})
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
