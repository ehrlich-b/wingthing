package wingsession

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestWingInitialCodexPromptAdmission(t *testing.T) {
	var spawned atomic.Int32
	s := &Service{Config: &config.Config{Dir: t.TempDir()}, Register: func(string) error { return nil }}
	s.Spawn = func(_ *Launch, o StartOptions) (*egg.Client, error) {
		spawned.Add(1)
		if o.Egg.InitialRun == nil || len(o.Egg.InitialRun.Prompt) != egg.MaxInitialCodexPromptBytes {
			t.Error("spawn lost admitted prompt")
		}
		return nil, nil
	}
	s.RunBackend = &RunBackend{
		Ready: func(context.Context, *config.Config, eggclient.LocalSession) error { return nil },
		Submit: func(_ context.Context, _ *config.Config, session eggclient.LocalSession, r egg.RunTurnRequest) (egg.RunTurnResult, error) {
			return egg.RunTurnResult{RunID: r.RunID, SessionID: session.ID, Status: "done"}, nil
		},
		Result: func(_ context.Context, _ *config.Config, session eggclient.LocalSession, id string) (egg.RunTurnResult, error) {
			return egg.RunTurnResult{RunID: id, SessionID: session.ID, Status: "done"}, nil
		},
	}
	if err := s.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := s.RunManager
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	a := Authority{Principal: "owner", UserID: "owner"}
	l := &Launch{service: s, authority: a, CWD: s.Config.Dir, Config: egg.UnsandboxedEggConfig()}
	for _, prompt := range []string{strings.Repeat("x", 140*1024), strings.Repeat("Ω", egg.MaxInitialCodexPromptBytes/2+1)} {
		req := RunRequest{Agent: "codex", Prompt: prompt, RequestKey: "retry"}
		for range 2 {
			run, fresh, err := m.Admit(l, req, StartOptions{}, "fixture")
			var sizeErr *egg.InitialCodexPromptTooLargeError
			if run != nil || fresh || !errors.As(err, &sizeErr) || sizeErr.SizeBytes != len(prompt) || sizeErr.LimitBytes != egg.MaxInitialCodexPromptBytes {
				t.Fatalf("admission: %v", err)
			}
			if saved, err := m.Retry(l, req); err != nil || saved != nil {
				t.Fatalf("rejected request retained: %v", err)
			}
		}
	}
	for _, table := range []string{"agent_runs", "tasks", "task_log"} {
		var count int
		if err := m.db.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("orphan rows in %s: %d %v", table, count, err)
		}
	}
	if spawned.Load() != 0 || len(m.records) != 0 {
		t.Fatal("rejection spawned or retained a run")
	}
	if _, err := os.Stat(filepath.Join(s.Config.Dir, "eggs")); !os.IsNotExist(err) {
		t.Fatalf("rejection created session state: %v", err)
	}
	req := RunRequest{Agent: "codex", Prompt: strings.Repeat("Ω", egg.MaxInitialCodexPromptBytes/2), RequestKey: "retry"}
	run, fresh, err := m.Admit(l, req, StartOptions{}, "fixture")
	if err != nil || !fresh {
		t.Fatalf("boundary admission: %v", err)
	}
	retry, fresh, err := m.Admit(l, req, StartOptions{}, "fixture")
	if err != nil || fresh || retry.ID != run.ID || retry.SessionID != run.SessionID {
		t.Fatalf("boundary retry: %v", err)
	}
	if err := m.Wait(t.Context(), a, []string{run.ID}); err != nil {
		t.Fatal(err)
	}
	if spawned.Load() != 1 {
		t.Fatalf("spawns = %d, want 1", spawned.Load())
	}
	for _, table := range []string{"agent_runs", "tasks"} {
		var count int
		if err := m.db.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("duplicate rows in %s: %d %v", table, count, err)
		}
	}
}
