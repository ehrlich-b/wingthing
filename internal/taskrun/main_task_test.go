package taskrun

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestAgentRunDeniedBinaryNamesFrozenPolicy(t *testing.T) {
	root := config.CanonicalProviderPath(t.TempDir())
	work, bin := filepath.Join(root, "work"), filepath.Join(root, "bin")
	for _, dir := range []string{work, bin} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(bin, "codex")
	marker := filepath.Join(work, "executed")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ntouch executed\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	policy := &egg.EggConfig{FS: []string{"ro:/", "rw:" + work, "deny:" + bin}}
	yaml, err := policy.TaskYAML()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "egg.yaml"), []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Dir: filepath.Join(root, "state"), DefaultAgent: "codex"}
	if err := os.Mkdir(cfg.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := &store.Task{ID: "denied", Type: "agent_run", Agent: "codex", What: "must not execute", CWD: work, Isolation: "standard", EggConfigYAML: yaml, RunAt: time.Now()}
	if err := db.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	err = RunTaskTo(context.Background(), cfg, db, task, io.Discard)
	if err == nil {
		t.Fatal("denied agent binary executed")
	}
	for _, want := range []string{"egg.yaml", "deny:" + bin, executable, "sandbox_explain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("denial missing %q: %v", want, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied binary created marker: %v", err)
	}
	stored, err := db.GetTask(task.ID)
	if err != nil || stored.Error == nil || !strings.Contains(*stored.Error, "sandbox_explain") || stored.ErrorKind != "sandbox_denied" {
		t.Fatalf("durable denial = %#v, %v", stored, err)
	}
}

func TestRunTaskLargeOutputPersistenceIsLinear(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// Deliver real-shaped events over time, as a streaming provider does. The
	// SQL trigger below independently rejects quadratic work even when writes
	// fit between provider chunks and their cost is hidden by emission latency.
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nawk '{ print; fflush(); system(\"sleep 0.05\") }' large.jsonl\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", root)
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("WT_PROVIDER_BASE_URL", "http://127.0.0.1")
	const chunks = 64
	block := strings.Repeat("x", 128*1024)
	line, err := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": block}})
	if err != nil {
		t.Fatal(err)
	}
	fixture := strings.Repeat(string(line)+"\n", chunks) + "{\"type\":\"turn.completed\"}\n"
	if err := os.WriteFile(filepath.Join(root, "large.jsonl"), []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	baseline := func() time.Duration {
		start := time.Now()
		stream, err := agent.NewCodex(0).Run(context.Background(), "large fixture", agent.RunOpts{WorkDir: root})
		if err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		for {
			chunk, ok := stream.Next()
			if !ok {
				break
			}
			output.WriteString(chunk.Text)
		}
		if stream.Err() != nil || output.Len() != 8*1024*1024 {
			t.Fatalf("baseline failed: bytes=%d, error=%v", output.Len(), stream.Err())
		}
		return time.Since(start)
	}
	// Warm the provider and filesystem before comparing equivalent streams.
	baseline()
	withoutPersistence := baseline()
	cfg := &config.Config{Dir: filepath.Join(root, "state"), DefaultAgent: "codex"}
	if err := os.Mkdir(cfg.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB().Exec(`CREATE TABLE snapshot_cost (bytes INTEGER);
		CREATE TRIGGER measure_output AFTER UPDATE OF output ON tasks
		BEGIN INSERT INTO snapshot_cost VALUES (length(CAST(NEW.output AS BLOB))); END`); err != nil {
		t.Fatal(err)
	}
	task := &store.Task{ID: "large", Type: "agent_run", Agent: "codex", What: "large fixture", CWD: root, Isolation: "privileged", RunAt: time.Now()}
	if err := db.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := RunTaskTo(context.Background(), cfg, db, task, io.Discard); err != nil {
		t.Fatal(err)
	}
	withPersistence := time.Since(start)
	stored, err := db.GetTask(task.ID)
	if err != nil || stored.Status != "done" || stored.Output == nil || *stored.Output != strings.Repeat(block, chunks) {
		t.Fatal("large run lost output")
	}
	var events int
	if err := db.DB().QueryRow("SELECT count(*) FROM task_log WHERE task_id = ? AND event = 'agent_message'", task.ID).Scan(&events); err != nil || events != chunks {
		t.Fatalf("chunk events = %d, %v", events, err)
	}
	var rewritten int
	if err := db.DB().QueryRow("SELECT sum(bytes) FROM snapshot_cost").Scan(&rewritten); err != nil || rewritten > 3*8*1024*1024 {
		t.Fatalf("transcript snapshots rewrote %d bytes for 8 MiB output: %v", rewritten, err)
	}
	t.Logf("8 MiB baseline=%s, persistence=%s", withoutPersistence, withPersistence)
	if withPersistence > withoutPersistence*11/10 {
		t.Fatalf("persistence took %s; baseline %s", withPersistence, withoutPersistence)
	}
}

func TestRunTaskPersistsFailureFromEveryEarlyExit(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "claude", WingID: "test-wing"}
	taskStore, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	task := &store.Task{
		ID:        "early-failure",
		Type:      "prompt",
		What:      "must not run",
		RunAt:     time.Now(),
		Agent:     "claude",
		Isolation: "privileged",
		CWD:       t.TempDir(),
	}
	if err := taskStore.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	err = RunTaskToWithOptions(context.Background(), cfg, taskStore, task, io.Discard, TaskRunOptions{SharedHost: true})
	if err == nil {
		t.Fatal("expected shared-host task to fail closed")
	}
	stored, getErr := taskStore.GetTask(task.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored == nil || stored.Status != "failed" || stored.Error == nil || *stored.Error == "" {
		t.Fatalf("failed task state was not persisted: %#v", stored)
	}
}

func TestRunTaskPersistsCodexThreadBeforeOutput(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	const thread = "01998952-827c-7000-8000-123456789abc"
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"" + thread + "\"}'\nwhile [ ! -f release ]; do sleep 0.01; done\nprintf '%s\\n' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", root)
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("WT_PROVIDER_BASE_URL", "http://127.0.0.1")
	cfg := &config.Config{Dir: root, DefaultAgent: "codex"}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	task := &store.Task{ID: "thread", Type: "agent_run", Agent: "codex", What: "wait", CWD: root, Isolation: "privileged", RunAt: time.Now()}
	if err := db.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = RunTaskTo(ctx, cfg, db, task, io.Discard)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
		if runErr != nil && !t.Failed() {
			t.Errorf("run task: %v", runErr)
		}
	}()
	for {
		stored, err := db.GetTask(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.ProviderThreadID == thread {
			if stored.Status != "running" || stored.Output != nil {
				t.Fatalf("thread identity was only persisted after completion: %#v", stored)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("thread.started was not persisted before assistant output")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Let the provider complete before the deferred cancellation and join.
	<-done
	if runErr != nil {
		t.Fatal(runErr)
	}
}
