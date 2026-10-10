package wingsession

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func recoveryRun(t *testing.T, id, phase string) *Run {
	t.Helper()
	yaml, err := egg.UnsandboxedEggConfig().TaskYAML()
	if err != nil {
		t.Fatal(err)
	}
	return &Run{ID: id, SessionID: "session-" + id, Agent: "claude", CWD: t.TempDir(), Phase: phase, Prompt: "request", OriginalPrompt: "request", TimeoutSeconds: 60, CreatedAt: time.Now().UTC(), Launch: RunLaunch{Authority: Authority{Principal: "owner", UserID: "owner", Unsandboxed: true}, ConfigYAML: yaml, Options: StartOptions{SessionID: "session-" + id, Agent: "claude"}}, Result: egg.RunTurnResult{RunID: id, SessionID: "session-" + id, Status: "pending", Deadline: time.Now().UTC().Add(time.Minute)}}
}

func persistRecoveryRuns(t *testing.T, cfg *config.Config, runs ...*Run) {
	t.Helper()
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, r := range runs {
		wire, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		row, _, err := db.AdmitAgentRun(&store.AgentRun{ID: r.ID, SessionID: r.SessionID, Principal: r.Launch.Authority.Principal, SpecHash: r.ID, Record: wire}, r.task())
		if err != nil {
			t.Fatal(err)
		}
		r.revision = row.Revision
	}
}

func TestRunRecoveryPersistedBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, phase       string
		eggExists, native bool
		accepted          bool
		spawns, submits   int32
		status            string
	}{
		{name: "queued", phase: "queued", spawns: 1, submits: 1, status: "done"},
		{name: "admitted", phase: "admitted", spawns: 1, submits: 1, status: "done"},
		{name: "spawn_intent_without_egg", phase: "spawning", status: "failed"},
		{name: "spawn_intent_with_egg", phase: "spawning", eggExists: true, submits: 1, status: "done"},
		{name: "spawned", phase: "spawned", eggExists: true, submits: 1, status: "done"},
		{name: "submit_intent_without_receipt", phase: "submitting", eggExists: true, status: "failed"},
		{name: "submit_intent_with_receipt", phase: "submitting", eggExists: true, accepted: true, status: "done"},
		{name: "observing", phase: "observing", eggExists: true, accepted: true, status: "done"},
		{name: "initial_native_turn", phase: "spawning", eggExists: true, native: true, accepted: true, status: "done"},
		{name: "terminal", phase: "terminal", status: "done"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			r := recoveryRun(t, "child", test.phase)
			parent := recoveryRun(t, "parent", "terminal")
			parent.Result.Status, parent.Result.Text = "timeout", "safe prior partial Ω"
			parent.Result.FailureKind = agent.Timeout
			r.ParentID, r.Direction, r.QueueExpiresAt = parent.ID, "finish", time.Now().Add(time.Hour)
			full := r.Result
			full.Status, full.Text, full.ProviderSessionID, full.TurnID = "done", "exact child Ω", "native-thread", "native-turn"
			full.EndedAt = time.Now().UTC()
			if test.phase == "terminal" {
				r.Result = full
			}
			dir := filepath.Join(cfg.Dir, "eggs", r.SessionID)
			if test.eggExists {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if test.native {
					if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("initial_run_id="+r.ID+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			persistRecoveryRuns(t, cfg, parent, r)
			var spawns, submits atomic.Int32
			accepted := test.accepted
			read := func(context.Context, *config.Config, eggclient.LocalSession, string) (egg.RunTurnResult, error) {
				if !accepted {
					return egg.RunTurnResult{}, os.ErrNotExist
				}
				return full, nil
			}
			s := &Service{Config: cfg, Register: func(string) error { return nil }, RunBackend: &RunBackend{
				Ready: func(context.Context, *config.Config, eggclient.LocalSession) error { return nil },
				Submit: func(_ context.Context, _ *config.Config, session eggclient.LocalSession, request egg.RunTurnRequest) (egg.RunTurnResult, error) {
					submits.Add(1)
					if accepted || request.RunID != r.ID || session.ID != r.SessionID {
						t.Error("duplicate execution or changed native identity")
					}
					if test.phase == "queued" && request.Prompt != SteerPrompt(parent.OriginalPrompt, parent.Result.Text, parent.Result.Error, r.Direction) {
						t.Error("queued restart lost prior context")
					}
					accepted = true
					return full, nil
				}, Status: read, Wait: read, Result: read,
			}}
			s.Spawn = func(*Launch, StartOptions) (*egg.Client, error) {
				spawns.Add(1)
				return nil, os.MkdirAll(dir, 0700)
			}
			t.Cleanup(func() {
				if s.RunManager != nil {
					_ = s.RunManager.Close()
				}
			})
			for restart := 0; restart < 2; restart++ {
				if err := s.StartRuns(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := s.RunManager.Wait(t.Context(), r.Launch.Authority, []string{r.ID}); err != nil {
					t.Fatal(err)
				}
				got, err := s.RunManager.Get(r.Launch.Authority, r.ID)
				if err != nil || got.Result.Status != test.status || got.ParentID != parent.ID || got.Phase != "terminal" {
					t.Fatalf("restart %d: %+v, %v", restart, got, err)
				}
				if test.status == "failed" && got.Result.FailureKind != agent.UnknownOutcome {
					t.Fatalf("ambiguous boundary lost explicit reason: %+v", got.Result)
				}
				if test.status == "done" && !reflect.DeepEqual(got.Result, full) {
					t.Fatalf("terminal truth changed: %+v", got.Result)
				}
				if err := s.RunManager.Close(); err != nil {
					t.Fatal(err)
				}
				s.RunManager = nil
			}
			if spawns.Load() != test.spawns || submits.Load() != test.submits {
				t.Fatalf("spawns=%d submits=%d, want %d/%d", spawns.Load(), submits.Load(), test.spawns, test.submits)
			}
		})
	}
}

func TestRunRecoveryStopTerminalParentCancelsUnstartedChildren(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	parent := recoveryRun(t, "parent", "terminal")
	parent.Result.Status, parent.Result.Text = "done", "immutable parent"
	queued, admitted := recoveryRun(t, "queued", "queued"), recoveryRun(t, "admitted", "admitted")
	queued.ParentID, admitted.ParentID = parent.ID, parent.ID
	persistRecoveryRuns(t, cfg, parent, queued, admitted)
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	m := &Runs{db: db, ctx: ctx, cancel: cancel, records: map[string]*Run{parent.ID: parent, queued.ID: queued, admitted.ID: admitted}, changed: make(chan struct{})}
	before := parent.Result
	if got, err := m.Stop(parent.Launch.Authority, parent.ID); err != nil || !reflect.DeepEqual(got.Result, before) {
		t.Fatalf("terminal parent stop rewrote history: %+v, %v", got, err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	s := &Service{Config: cfg, Spawn: func(*Launch, StartOptions) (*egg.Client, error) {
		t.Error("cancelled child spawned after restart")
		return nil, os.ErrInvalid
	}}
	if err := s.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer s.RunManager.Close()
	for _, r := range []*Run{queued, admitted} {
		got, err := s.RunManager.Get(parent.Launch.Authority, r.ID)
		if err != nil || got.ParentID != parent.ID || got.Result.Status != "stopped" || got.Result.FailureKind != agent.Stopped || got.Result.Error == "" {
			t.Fatalf("restart lost accepted child's cancellation: %+v, %v", got, err)
		}
	}
}
