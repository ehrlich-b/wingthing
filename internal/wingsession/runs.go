package wingsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
)

// RunLaunch is captured at admission. Reconciliation uses this exact launch,
// never a reconnecting client's arguments or credentials.
type RunLaunch struct {
	Authority  Authority
	Identity   eggclient.EggIdentity
	ConfigYAML string
	Options    StartOptions
	Actor      string
}

type Run struct {
	ID             string            `json:"run_id"`
	SessionID      string            `json:"session_id"`
	RequestKey     string            `json:"idempotency_key,omitempty"`
	SpecHash       string            `json:"spec_hash"`
	Prompt         string            `json:"prompt"`
	OriginalPrompt string            `json:"original_prompt"`
	Agent          string            `json:"agent"`
	Model          string            `json:"model"`
	CWD            string            `json:"cwd"`
	Label          string            `json:"label,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Isolation      string            `json:"isolation"`
	CreatedAt      time.Time         `json:"created_at"`
	ParentID       string            `json:"parent_id,omitempty"`
	Direction      string            `json:"direction,omitempty"`
	QueueExpiresAt time.Time         `json:"queue_expires_at,omitempty"`
	Launch         RunLaunch         `json:"launch"`
	Phase          string            `json:"phase"`
	Cancelled      bool              `json:"cancelled,omitempty"`
	Result         egg.RunTurnResult `json:"result"`
	revision       int64
}

type RunRequest struct {
	Prompt, Agent, Model, CWD, Label, RequestKey, ParentID, Direction string
	TimeoutSeconds                                                    int
}

// RunBackend only substitutes egg transport in isolated protocol fixtures.
// Production execution is exclusively the egg RunTurns RPC service.
type RunBackend struct {
	Reserve func(context.Context, *config.Config, eggclient.LocalSession, egg.RunTurnRequest) (egg.RunTurnResult, error)
	Ready   func(context.Context, *config.Config, eggclient.LocalSession) error
	Submit  func(context.Context, *config.Config, eggclient.LocalSession, egg.RunTurnRequest) (egg.RunTurnResult, error)
	Status  func(context.Context, *config.Config, eggclient.LocalSession, string) (egg.RunTurnResult, error)
	Wait    func(context.Context, *config.Config, eggclient.LocalSession, string) (egg.RunTurnResult, error)
	Result  func(context.Context, *config.Config, eggclient.LocalSession, string) (egg.RunTurnResult, error)
	Stop    func(context.Context, *config.Config, eggclient.LocalSession, string) (egg.RunTurnResult, error)
}

type Runs struct {
	service *Service
	db      *store.Store
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	records map[string]*Run
	changed chan struct{}
	workers sync.WaitGroup
	backend RunBackend
}

// StartRuns is called by the wing, including while its relay is disconnected.
// Closing it cancels observations only; admitted eggs keep their own deadlines.
func (s *Service) StartRuns(ctx context.Context) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.RunManager != nil {
		return nil
	}
	db, err := store.Open(s.Config.DBPath())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &Runs{service: s, db: db, ctx: ctx, cancel: cancel, records: map[string]*Run{}, changed: make(chan struct{}), backend: RunBackend{Reserve: eggclient.ReserveRunTurn, Ready: awaitNativeRunReady, Submit: eggclient.SubmitRunTurn, Status: eggclient.RunTurnStatus, Wait: eggclient.WaitRunTurn, Result: eggclient.ReadRunTurnResult, Stop: eggclient.StopRunTurn}}
	if s.RunBackend != nil {
		m.backend = *s.RunBackend
	}
	rows, err := db.ListAgentRuns()
	if err != nil {
		cancel()
		db.Close()
		return err
	}
	for _, row := range rows {
		var run Run
		if err = json.Unmarshal(row.Record, &run); err != nil {
			cancel()
			db.Close()
			return err
		}
		run.revision = row.Revision
		m.records[run.ID] = &run
	}
	if _, err = db.DB().Exec("UPDATE tasks SET status='failed',error='Wingthing legacy run ended: unknown_outcome.',finished_at=CURRENT_TIMESTAMP WHERE type='agent_run' AND status IN ('pending','running') AND id NOT IN (SELECT id FROM agent_runs)"); err != nil {
		cancel()
		db.Close()
		return err
	}
	s.RunManager = m
	m.mu.Lock()
	for _, run := range m.records {
		if !run.Result.Terminal() {
			m.start(run.ID)
		}
	}
	m.mu.Unlock()
	return nil
}

func (m *Runs) Close() error {
	m.mu.Lock()
	m.cancel()
	m.signal()
	m.mu.Unlock()
	m.workers.Wait()
	return m.db.Close()
}

func (m *Runs) signal() { close(m.changed); m.changed = make(chan struct{}) }

func (r *Run) task() *store.Task {
	task := &store.Task{ID: r.ID, Type: "agent_run", What: r.Prompt, Agent: r.Agent, Model: r.Model, CWD: r.CWD, Principal: r.Launch.Authority.Principal, TimeoutSeconds: r.TimeoutSeconds, Isolation: r.Isolation, CreatedAt: r.CreatedAt, RunAt: r.CreatedAt, Status: r.Result.Status, EggConfigYAML: r.Launch.ConfigYAML}
	if r.ParentID != "" {
		parent := r.ParentID
		task.ParentID = &parent
	}
	if !r.Result.StartedAt.IsZero() {
		v := r.Result.StartedAt
		task.StartedAt = &v
	}
	if !r.Result.EndedAt.IsZero() {
		v := r.Result.EndedAt
		task.FinishedAt = &v
	}
	if r.Result.Terminal() {
		text := r.Result.Text
		task.Output = &text
	}
	if r.Result.Error != "" {
		text := r.Result.Error
		task.Error = &text
	}
	return task
}

func (m *Runs) save(run *Run, event string) error {
	wire, err := json.Marshal(run)
	if err != nil {
		return err
	}
	row := &store.AgentRun{ID: run.ID, Record: wire, Revision: run.revision}
	if err = m.db.SaveAgentRun(row, run.task(), event, string(run.Result.FailureKind)); err != nil {
		return err
	}
	run.revision = row.Revision
	m.records[run.ID] = run
	m.signal()
	return nil
}

func cloneRun(r *Run) *Run {
	wire, _ := json.Marshal(r)
	var copy Run
	_ = json.Unmarshal(wire, &copy)
	copy.revision = r.revision
	return &copy
}

func (m *Runs) owned(a Authority, r *Run) bool {
	if r == nil {
		return false
	}
	owner := r.Launch.Authority
	if a.UserID != "" && owner.UserID != a.UserID {
		return false
	}
	if a.Principal != owner.Principal && !(a.LegacyLocalDefault && (owner.Principal == "default" || owner.Principal == "")) {
		return false
	}
	return !a.EnforcePaths || wingRunPathAllowed(r.CWD, a.AllowedPaths)
}

func wingRunPathAllowed(cwd string, paths []string) bool {
	for _, p := range paths {
		rel, err := filepath.Rel(p, cwd)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func (m *Runs) Get(a Authority, id string) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records[id] == nil {
		return m.legacy(a, id)
	}
	if !m.owned(a, m.records[id]) {
		return nil, fmt.Errorf("agent run %q not found or not owned by caller", id)
	}
	return cloneRun(m.records[id]), nil
}

func (m *Runs) Admit(launch *Launch, request RunRequest, options StartOptions, actor string) (*Run, bool, error) {
	if launch == nil || launch.service != m.service {
		return nil, false, errors.New("run launch was not prepared by this wing")
	}
	if request.Agent != "claude" && request.Agent != "codex" {
		return nil, false, errors.New("provider has no native run-turn adapter; use agent_start")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, false, errors.New("prompt is required")
	}
	if request.TimeoutSeconds < 10 || request.TimeoutSeconds > 7200 {
		return nil, false, errors.New("timeout_seconds must be between 10 and 7200")
	}
	if len(request.RequestKey) > 200 {
		return nil, false, errors.New("idempotency_key must have at most 200 bytes")
	}
	yaml, err := launch.Config.TaskYAML()
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	run := &Run{ID: cmdutil.GenTaskID(), SessionID: cmdutil.NewRuntimeID(), RequestKey: request.RequestKey, SpecHash: runRequestHash(launch, request), Prompt: request.Prompt, OriginalPrompt: request.Prompt, Agent: request.Agent, Model: request.Model, CWD: launch.CWD, Label: request.Label, TimeoutSeconds: request.TimeoutSeconds, CreatedAt: now, ParentID: request.ParentID, Direction: request.Direction, Phase: "admitted", Isolation: "sandbox"}
	if launch.authority.Unsandboxed {
		run.Isolation = "privileged"
	}
	options.SessionID = run.SessionID
	options.Agent = run.Agent
	options.Egg.Kind = "agent"
	options.Egg.Principal = launch.authority.Principal
	options.Egg.Label = request.Label
	// Passkey tokens authorize admission only and are never archived.
	authority := launch.authority
	authority.PublicKey = ""
	authority.AuthToken = ""
	run.Launch = RunLaunch{Authority: authority, Identity: launch.Identity, ConfigYAML: yaml, Options: options, Actor: actor}
	run.Result = egg.RunTurnResult{RunID: run.ID, SessionID: run.SessionID, Status: "pending"}
	if run.ParentID != "" {
		run.Phase = "queued"
		run.QueueExpiresAt = now.Add(2 * time.Hour)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = m.ctx.Err(); err != nil {
		return nil, false, err
	}
	if run.ParentID != "" {
		if parent := m.records[run.ParentID]; parent != nil {
			if !m.owned(authority, parent) {
				return nil, false, errors.New("agent run not found or not owned by caller")
			}
		} else {
			parent, err := m.legacy(authority, run.ParentID)
			if err != nil {
				return nil, false, err
			}
			m.records[parent.ID] = parent
		}
	}
	wire, err := json.Marshal(run)
	if err != nil {
		return nil, false, err
	}
	row, fresh, err := m.db.AdmitAgentRun(&store.AgentRun{ID: run.ID, SessionID: run.SessionID, Principal: authority.Principal, RequestKey: run.RequestKey, SpecHash: run.SpecHash, Record: wire}, run.task())
	if err != nil {
		return nil, false, err
	}
	if !fresh {
		var saved Run
		if err = json.Unmarshal(row.Record, &saved); err != nil {
			return nil, false, err
		}
		if !m.owned(authority, &saved) {
			return nil, false, errors.New("idempotency_key belongs to another owner")
		}
		saved.revision = row.Revision
		return &saved, false, nil
	}
	run.revision = row.Revision
	m.records[run.ID] = run
	m.signal()
	m.start(run.ID)
	return cloneRun(run), true, nil
}

func (m *Runs) start(id string) {
	m.workers.Add(1)
	go func() { defer m.workers.Done(); m.reconcile(id) }()
}

func (m *Runs) update(id, event string, change func(*Run)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := cloneRun(m.records[id])
	if r.Result.Terminal() {
		return nil
	}
	change(r)
	return m.save(r, event)
}

func (m *Runs) fail(id string, kind agent.ErrorKind) {
	_ = m.update(id, string(kind), func(r *Run) {
		r.Phase = "terminal"
		r.Result.Status = "failed"
		if kind == agent.Timeout {
			r.Result.Status = "timeout"
		}
		r.Result.FailureKind = kind
		r.Result.Error = "Wingthing run ended: " + string(kind) + "."
		r.Result.EndedAt = time.Now().UTC()
	})
}

func (m *Runs) snapshot(id string) *Run {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneRun(m.records[id])
}

func (m *Runs) reconcile(id string) {
	r := m.snapshot(id)
	if r.Result.Terminal() {
		return
	}
	if r.Phase == "queued" {
		ctx, cancel := context.WithDeadline(m.ctx, r.QueueExpiresAt)
		defer cancel()
		if err := m.Wait(ctx, r.Launch.Authority, []string{r.ParentID}); err != nil {
			if m.ctx.Err() == nil {
				m.fail(id, agent.Timeout)
			}
			return
		}
		parent, err := m.Get(r.Launch.Authority, r.ParentID)
		if err != nil {
			m.fail(id, agent.UnknownOutcome)
			return
		}
		if err := m.update(id, "dequeued", func(r *Run) {
			r.Prompt = SteerPrompt(parent.OriginalPrompt, parent.Result.Text, parent.Result.Error, r.Direction)
			r.Phase = "admitted"
		}); err != nil {
			return
		}
		r = m.snapshot(id)
		if r.Result.Terminal() {
			return
		}
	}
	if r.Cancelled {
		m.cancelEgg(r)
		return
	}
	session := eggclient.LocalSession{ID: r.SessionID, Agent: r.Agent, CWD: r.CWD, Principal: r.Launch.Authority.Principal}
	if r.Phase == "admitted" {
		if err := m.update(id, "launching", func(r *Run) {
			r.Phase = "spawning"
			r.Result.Deadline = time.Now().UTC().Add(time.Duration(r.TimeoutSeconds) * time.Second)
		}); err != nil {
			return
		}
		r = m.snapshot(id)
		if r.Result.Terminal() {
			return
		}
		cfg, err := egg.LoadTaskEggConfigFromYAML(r.Launch.ConfigYAML)
		if err != nil {
			m.fail(id, agent.UnknownOutcome)
			return
		}
		launch := &Launch{Config: cfg, CWD: r.CWD, Identity: r.Launch.Identity, authority: r.Launch.Authority, service: m.service}
		options := r.Launch.Options
		if r.Agent == "codex" {
			options.Egg.InitialRun = &egg.RunTurnRequest{RunID: id, Prompt: r.Prompt, Deadline: r.Result.Deadline}
		}
		client, err := m.service.Start(m.ctx, launch, options)
		if client != nil {
			client.Close()
		}
		if err != nil {
			if m.ctx.Err() == nil {
				m.fail(id, agent.ProviderError)
			}
			return
		}
		if err = m.update(id, "spawned", func(r *Run) {
			r.Phase = "spawned"
			meta := eggclient.ReadEggMetaValues(filepath.Join(m.service.Config.Dir, "eggs", r.SessionID))
			if meta["initial_run_id"] == r.ID {
				return // The egg reserved the supplied absolute deadline before launch.
			}
			if nanos, err := strconv.ParseInt(meta["started_at_nanos"], 10, 64); err == nil {
				r.Result.Deadline = time.Unix(0, nanos).UTC().Add(time.Duration(r.TimeoutSeconds) * time.Second)
			} else if sec, err := strconv.ParseInt(meta["started_at"], 10, 64); err == nil {
				r.Result.Deadline = time.Unix(sec, 0).UTC().Add(time.Duration(r.TimeoutSeconds) * time.Second)
			}
		}); err != nil {
			return
		}
		r = m.snapshot(id)
	}
	if r.Phase == "spawning" {
		// The spawn intent was committed but no receipt survived. Never relaunch.
		// An existing egg may still be observed; absent evidence is unknown.
		if _, err := os.Stat(filepath.Join(m.service.Config.Dir, "eggs", r.SessionID)); err != nil {
			m.fail(id, agent.UnknownOutcome)
			return
		}
		if err := m.service.Register(r.SessionID); err != nil {
			m.fail(id, agent.UnknownOutcome)
			return
		}
		if err := m.update(id, "reconciled", func(r *Run) { r.Phase = "spawned" }); err != nil {
			return
		}
		r = m.snapshot(id)
	}
	if r.Cancelled {
		m.cancelEgg(r)
		return
	}
	if r.Phase == "spawned" {
		meta := eggclient.ReadEggMetaValues(filepath.Join(m.service.Config.Dir, "eggs", r.SessionID))
		if meta["initial_run_id"] == id {
			// The durable egg reservation owns Codex's initial argv turn. Observe
			// it directly, including after a wing crash during spawn; never resend.
			if err := m.update(id, "observing_initial_turn", func(r *Run) { r.Phase = "observing" }); err != nil {
				return
			}
			r = m.snapshot(id)
		}
	}
	if r.Phase == "spawned" {
		if m.backend.Reserve != nil {
			if _, err := m.backend.Reserve(m.ctx, m.service.Config, session, egg.RunTurnRequest{RunID: id, Prompt: r.Prompt, Deadline: r.Result.Deadline}); err != nil {
				if m.ctx.Err() == nil {
					m.fail(id, agent.UnknownOutcome)
				}
				return
			}
		}
		ctx, cancel := context.WithDeadline(m.ctx, r.Result.Deadline)
		err := m.backend.Ready(ctx, m.service.Config, session)
		cancel()
		if err != nil {
			if m.ctx.Err() == nil {
				result, readErr := m.backend.Wait(m.ctx, m.service.Config, session, id)
				if readErr == nil && result.Terminal() {
					full, readErr := m.backend.Result(m.ctx, m.service.Config, session, id)
					if readErr == nil {
						_ = m.accept(id, full)
						return
					}
				}
				m.fail(id, agent.UnknownOutcome)
			}
			return
		}
		// Persist the submission intent before RPC. A lost acknowledgement is
		// recovered by reading the exact run ID; ambiguous input is never resent.
		if err = m.update(id, "submitting", func(r *Run) { r.Phase = "submitting" }); err != nil {
			return
		}
		r = m.snapshot(id)
		if r.Cancelled {
			m.cancelEgg(r)
			return
		}
		result, err := m.backend.Submit(m.ctx, m.service.Config, session, egg.RunTurnRequest{RunID: id, Prompt: r.Prompt, Deadline: r.Result.Deadline})
		if err != nil {
			if m.ctx.Err() != nil {
				return
			}
			result, err = m.backend.Status(m.ctx, m.service.Config, session, id)
			if err != nil {
				m.fail(id, agent.UnknownOutcome)
				return
			}
		}
		if result.Terminal() {
			// Status/Submit receipts can omit text. Publish ready only with the
			// complete artifact, including when submission lost its acknowledgement.
			result, err = m.backend.Result(m.ctx, m.service.Config, session, id)
			if err != nil {
				if m.ctx.Err() == nil {
					m.fail(id, agent.UnknownOutcome)
				}
				return
			}
		}
		if err = m.accept(id, result); err != nil {
			return
		}
	}
	r = m.snapshot(id)
	if r.Result.Terminal() {
		return
	}
	if r.Cancelled {
		m.cancelEgg(r)
		return
	}
	result, err := m.backend.Wait(m.ctx, m.service.Config, session, id)
	if err != nil {
		if m.ctx.Err() == nil {
			m.fail(id, agent.UnknownOutcome)
		}
		return
	}
	if !result.Terminal() {
		m.fail(id, agent.UnknownOutcome)
		return
	}
	full, err := m.backend.Result(m.ctx, m.service.Config, session, id)
	if err != nil {
		if m.ctx.Err() == nil {
			m.fail(id, agent.UnknownOutcome)
		}
		return
	}
	_ = m.accept(id, full)
}

func (m *Runs) accept(id string, result egg.RunTurnResult) error {
	if result.RunID != id || result.SessionID != m.snapshot(id).SessionID {
		m.fail(id, agent.UnknownOutcome)
		return errors.New("egg result identity mismatch")
	}
	return m.update(id, result.Status, func(r *Run) {
		if r.Result.Terminal() {
			return
		}
		r.Result = result
		r.Phase = "observing"
		if result.Terminal() {
			r.Phase = "terminal"
		}
	})
}

// Wait uses one subscription for all selected IDs. Client cancellation only
// removes this observer and never cancels accepted execution.
func (m *Runs) Wait(ctx context.Context, a Authority, ids []string) error {
	for {
		m.mu.Lock()
		pending := false
		finished := false
		for _, id := range ids {
			r := m.records[id]
			if !m.owned(a, r) {
				continue
			}
			if r.Result.Terminal() {
				finished = true
			} else {
				pending = true
			}
		}
		changed := m.changed
		m.mu.Unlock()
		if finished || !pending {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-changed:
		}
	}
}

func (m *Runs) cancelEgg(r *Run) {
	session := eggclient.LocalSession{ID: r.SessionID, Agent: r.Agent, CWD: r.CWD}
	result, err := m.backend.Stop(m.ctx, m.service.Config, session, r.ID)
	if err == nil {
		if result.Terminal() {
			full, readErr := m.backend.Result(m.ctx, m.service.Config, session, r.ID)
			if readErr != nil {
				if m.ctx.Err() == nil {
					m.fail(r.ID, agent.UnknownOutcome)
				}
				return
			}
			result = full
			_ = m.accept(r.ID, result)
			return
		}
	}
	if m.ctx.Err() != nil {
		return
	}
	// Stop-before-submit has no turn record. Stop the ordinary session through
	// the same session primitive before recording a cancellation outcome.
	if err := eggclient.KillOrphanEggContext(m.ctx, m.service.Config, r.SessionID); err != nil {
		return
	}
	_ = m.update(r.ID, "stopped", func(r *Run) {
		r.Result.Status = "stopped"
		r.Result.FailureKind = agent.Stopped
		r.Result.Error = "Wingthing run ended: stopped."
		r.Result.EndedAt = time.Now().UTC()
		r.Phase = "terminal"
	})
}

func markRunStopped(r *Run) {
	r.Cancelled = true
	r.Phase = "terminal"
	r.Result.Status = "stopped"
	r.Result.FailureKind = agent.Stopped
	r.Result.Error = "Wingthing run ended: stopped."
	r.Result.EndedAt = time.Now().UTC()
}

func (m *Runs) Stop(a Authority, id string) (*Run, error) {
	m.mu.Lock()
	if m.records[id] == nil {
		legacy, err := m.legacy(a, id)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		m.records[id] = legacy
	}
	if !m.owned(a, m.records[id]) {
		m.mu.Unlock()
		return nil, errors.New("agent run not found or not owned by caller")
	}
	r := cloneRun(m.records[id])
	updates := []store.AgentRunUpdate{}
	copies := []*Run{}
	add := func(run *Run, event string) error {
		wire, err := json.Marshal(run)
		if err != nil {
			return err
		}
		updates = append(updates, store.AgentRunUpdate{Run: &store.AgentRun{ID: run.ID, Record: wire, Revision: run.revision}, Task: run.task(), Event: event, Detail: string(run.Result.FailureKind)})
		copies = append(copies, run)
		return nil
	}
	if !r.Result.Terminal() {
		r.Cancelled = true
		if r.Phase == "queued" || r.Phase == "admitted" {
			markRunStopped(r)
		}
		if err := add(r, "stop_requested"); err != nil {
			m.mu.Unlock()
			return nil, err
		}
	}
	for _, child := range m.records {
		if child.ParentID == id && (child.Phase == "queued" || child.Phase == "admitted") {
			next := cloneRun(child)
			markRunStopped(next)
			if err := add(next, "stopped"); err != nil {
				m.mu.Unlock()
				return nil, err
			}
		}
	}
	if len(updates) > 0 {
		if err := m.db.SaveAgentRuns(updates); err != nil {
			m.mu.Unlock()
			return nil, err
		}
		for i, copy := range copies {
			copy.revision = updates[i].Run.Revision
			m.records[copy.ID] = copy
		}
		m.signal()
	}
	m.mu.Unlock()
	if !r.Result.Terminal() {
		m.cancelEgg(r)
		if err := m.Wait(m.ctx, a, []string{id}); err != nil {
			return nil, err
		}
	}
	return m.Get(a, id)
}

type RunEvent struct {
	Cursor                   int64
	Timestamp, Event, Detail string
}

func (m *Runs) Events(a Authority, id string, cursor int64, limit int) ([]RunEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	legacy := m.records[id] == nil || m.records[id].SessionID == ""
	if m.records[id] == nil {
		if _, err := m.legacy(a, id); err != nil {
			return nil, err
		}
	} else if !m.owned(a, m.records[id]) {
		return nil, fmt.Errorf("agent run %q not found or not owned by caller", id)
	}
	rows, err := m.db.DB().Query("SELECT id,timestamp,event,COALESCE(detail,'') FROM task_log WHERE task_id=? AND (?=0 OR id<?) ORDER BY id DESC LIMIT ?", id, cursor, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunEvent{}
	for rows.Next() {
		var e RunEvent
		if err := rows.Scan(&e.Cursor, &e.Timestamp, &e.Event, &e.Detail); err != nil {
			return nil, err
		}
		if legacy {
			e.Detail = ""
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func SteerPrompt(request, result, priorError, direction string) string {
	runes := []rune(result)
	if len(runes) > 200000 {
		result = string(runes[:200000]) + "\n\n[Wingthing truncated the prior result for this follow-up.]"
	}
	prompt := "Prior request:\n" + request + "\n\nPrior result:\n" + result
	if priorError != "" {
		prompt += "\n\nPrior error:\n" + priorError
	}
	return prompt + "\n\nNew direction:\n" + direction
}

func awaitNativeRunReady(ctx context.Context, cfg *config.Config, session eggclient.LocalSession) error {
	meta := eggclient.ReadEggMetaValues(filepath.Join(cfg.Dir, "eggs", session.ID))
	if meta["native_run"] != "true" {
		return errors.New("egg has no native run-turn adapter")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		view, err := eggclient.LifecycleViewForSession(cfg, session, 0, 1)
		if err != nil {
			return err
		}
		if egg.NativePromptReady(view) {
			return nil
		}
		if !view.ProcessAlive {
			return errors.New("egg exited before native readiness")
		}
		if _, _, err = eggclient.WaitSessionLifecycle(ctx, cfg, session, view.HeadCursor, ""); err != nil {
			return err
		}
	}
}

func runRequestHash(launch *Launch, request RunRequest) string {
	request.RequestKey = ""
	request.CWD = launch.CWD
	spec, _ := json.Marshal(struct {
		Request     RunRequest
		Principal   string
		Unsandboxed bool
	}{request, launch.authority.Principal, launch.authority.Unsandboxed})
	digest := sha256.Sum256(spec)
	return hex.EncodeToString(digest[:])
}

// Retry resolves accepted work before charging admission bounds again.
func (m *Runs) Retry(launch *Launch, request RunRequest) (*Run, error) {
	if request.RequestKey == "" {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.records {
		if r.RequestKey == request.RequestKey && m.owned(launch.authority, r) {
			if r.SpecHash != runRequestHash(launch, request) {
				return nil, errors.New("idempotency_key already has a different run request")
			}
			return cloneRun(r), nil
		}
	}
	return nil, nil
}

// ReservedSessions includes durable queue slots not yet represented in egg
// inventory, so async launches cannot race through a principal's last slot.
func (m *Runs) ReservedSessions(a Authority) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.records {
		if m.owned(a, r) && !r.Result.Terminal() {
			if _, alive := eggclient.ReadAliveEggPID(filepath.Join(m.service.Config.Dir, "eggs", r.SessionID)); !alive {
				n++
			}
		}
	}
	return n
}

func (m *Runs) legacy(a Authority, id string) (*Run, error) {
	if id == "" {
		return nil, errors.New("run_id is required")
	}
	t, err := m.db.GetTask(id)
	if err != nil {
		return nil, err
	}
	if t == nil || t.Type != "agent_run" || !(t.Principal == a.Principal || a.LegacyLocalDefault && (t.Principal == "" || t.Principal == "default")) || (a.EnforcePaths && !wingRunPathAllowed(t.CWD, a.AllowedPaths)) {
		return nil, fmt.Errorf("agent run %q not found or not owned by caller", id)
	}
	r := &Run{ID: t.ID, Prompt: t.What, OriginalPrompt: t.What, Agent: t.Agent, Model: t.Model, CWD: t.CWD, Isolation: t.Isolation, TimeoutSeconds: t.TimeoutSeconds, CreatedAt: t.CreatedAt, Phase: "terminal", Launch: RunLaunch{Authority: a}, Result: egg.RunTurnResult{RunID: t.ID, Status: t.Status}}
	if t.StartedAt != nil {
		r.Result.StartedAt = *t.StartedAt
	}
	if t.FinishedAt != nil {
		r.Result.EndedAt = *t.FinishedAt
	}
	if t.Output != nil {
		r.Result.Text = *t.Output
	}
	if t.Status != "done" {
		r.Result.Text = ""
		r.Result.FailureKind = agent.UnknownOutcome
		r.Result.Error = "Wingthing legacy run ended: unknown_outcome."
	}
	return r, nil
}
