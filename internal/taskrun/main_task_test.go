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
	if err != nil || stored.Error == nil || !strings.Contains(*stored.Error, "sandbox_explain") {
		t.Fatalf("durable denial = %#v, %v", stored, err)
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

func TestRunTaskLargeOutputRedactsWithoutRescanningTranscript(t *testing.T) {
	const secret = "review-only-fake-token-7Qn3"
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\ncat large.jsonl\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENAI_API_KEY", secret)
	t.Setenv("WT_PROVIDER_BASE_URL", "http://127.0.0.1")
	block := strings.Repeat("common provider output. ", 5500) + secret + " tail\n"
	line, err := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]string{"type": "agent_message", "text": block}})
	if err != nil {
		t.Fatal(err)
	}
	fixture := strings.Repeat(string(line)+"\n", 64) + "{\"type\":\"turn.completed\"}\n"
	if err := os.WriteFile(filepath.Join(root, "large.jsonl"), []byte(fixture), 0600); err != nil {
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
	task := &store.Task{ID: "large", Type: "agent_run", Agent: "codex", What: "large fixture", CWD: root, Isolation: "privileged", RunAt: time.Now()}
	if err := db.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	// A generous bound catches the quadratic regression (68s on this fixture)
	// while leaving room above the roughly 5s pre-redaction process baseline.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	if err := RunTaskTo(ctx, cfg, db, task, io.Discard); err != nil {
		t.Fatalf("8 MiB run failed after %s: %v", time.Since(start), err)
	}
	stored, err := db.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat(strings.ReplaceAll(block, secret, "[redacted]"), 64)
	if stored.Status != "done" || stored.Output == nil || *stored.Output != want {
		t.Fatal("large run lost output or exposed a credential")
	}
	t.Logf("8 MiB run completed in %s", time.Since(start))
}
