package egg

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

// RunTurnRequest binds one prompt to an egg-owned absolute execution deadline.
type RunTurnRequest struct {
	RunID    string    `json:"run_id"`
	Prompt   string    `json:"prompt"`
	Deadline time.Time `json:"deadline"`
}

type RunTurnResult struct {
	RunID                string          `json:"run_id"`
	SessionID            string          `json:"session_id"`
	Status               string          `json:"status"`
	Text                 string          `json:"text"`
	ProviderSessionID    string          `json:"provider_session_id,omitempty"`
	TurnID               string          `json:"turn_id,omitempty"`
	StartedAt            time.Time       `json:"started_at"`
	EndedAt              time.Time       `json:"ended_at,omitempty"`
	Deadline             time.Time       `json:"deadline"`
	FailureKind          agent.ErrorKind `json:"failure_kind,omitempty"`
	Error                string          `json:"error,omitempty"`
	SurvivingDescendants []RunDescendant `json:"surviving_descendants,omitempty"`
	ContainmentError     string          `json:"containment_error,omitempty"`
}

func (r RunTurnResult) Terminal() bool {
	return r.Status == "done" || r.Status == "failed" || r.Status == "timeout" || r.Status == "stopped"
}

type runTurnRecord struct {
	Version  int           `json:"version"`
	SpecHash string        `json:"spec_hash"`
	Result   RunTurnResult `json:"result"`
}

type runTurnBackend struct {
	Agent             string
	Read              func(context.Context, int64, int) (SessionView, error)
	Send              func(context.Context, string) (PromptDelivery, error)
	Prepare           func(string, string) (func() (turnEvidence, error), error)
	Kill              func() ([]RunDescendant, error)
	Done              <-chan struct{}
	Started           <-chan struct{}
	StartupDiagnostic func() string
}

type turnEvidence struct {
	Receipt           bool
	Complete          bool
	Conflict          bool
	Text              string
	TurnID            string
	Failure           agent.ErrorKind
	ProviderSessionID string
	Diagnostic        string
}

type ownedRunTurn struct {
	mu         sync.Mutex
	record     runTurnRecord
	done       chan struct{}
	workerDone chan struct{}
	finishing  bool
	err        error
	cancel     context.CancelFunc
	timer      *time.Timer
	started    bool
	initial    bool
	readyTimer *time.Timer
}

// runTurnRuntime lives only in the existing egg. Client contexts cancel RPC
// observations; they never own the accepted prompt, timer or provider process.
type runTurnRuntime struct {
	mu      sync.Mutex
	dir     string
	backend runTurnBackend
	runs    map[string]*ownedRunTurn
}

func newRunTurnRuntime(dir string, backend runTurnBackend) *runTurnRuntime {
	return &runTurnRuntime{dir: dir, backend: backend, runs: make(map[string]*ownedRunTurn)}
}

func runTurnPath(dir, id string) string {
	return filepath.Join(dir, fmt.Sprintf("run.%x.json", sha256.Sum256([]byte(id))))
}

func persistRunTurn(dir string, record runTurnRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err = atomicWritePrivate(runTurnPath(dir, record.Result.RunID), append(data, '\n')); err != nil {
		return err
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// ReadRunTurnResult also works after the egg exits. Nonterminal artifacts are
// evidence of admission only, never proof that an absent provider succeeded.
func ReadRunTurnResult(dir, runID string) (RunTurnResult, error) {
	record, err := readRunTurnRecord(dir, runID)
	return record.Result, err
}

func readRunTurnRecord(dir, id string) (runTurnRecord, error) {
	var record runTurnRecord
	f, err := openBoundRegularFile(runTurnPath(dir, id))
	if err != nil {
		return record, err
	}
	defer f.Close()
	err = json.NewDecoder(f).Decode(&record)
	if err == nil && (record.Version != 1 || record.Result.RunID != id) {
		err = errors.New("invalid run artifact")
	}
	return record, err
}

func (rt *runTurnRuntime) reserve(request RunTurnRequest) (RunTurnResult, error) {
	return rt.admit(request, false)
}

func (rt *runTurnRuntime) submit(request RunTurnRequest) (RunTurnResult, error) {
	return rt.admit(request, true)
}

func (rt *runTurnRuntime) admit(request RunTurnRequest, execute bool) (RunTurnResult, error) {
	options := SessionPromptOptions{RequestID: request.RunID, Input: request.Prompt, Timeout: time.Minute, Read: rt.backend.Read, Send: rt.backend.Send}
	if err := validateSessionPromptOptions(options); err != nil {
		return RunTurnResult{}, err
	}
	if request.Deadline.IsZero() {
		return RunTurnResult{}, errors.New("absolute deadline is required")
	}
	request.Deadline = request.Deadline.UTC()
	spec, _ := json.Marshal(request)
	hash := fmt.Sprintf("%x", sha256.Sum256(spec))
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if run := rt.runs[request.RunID]; run != nil {
		run.mu.Lock()
		defer run.mu.Unlock()
		if run.record.SpecHash != hash {
			return RunTurnResult{}, errors.New("run_id already has a different prompt or deadline")
		}
		if execute && !run.started && !run.finishing {
			if err := rt.begin(run, request, options); err != nil {
				return RunTurnResult{}, err
			}
		}
		return run.record.Result, run.err
	}
	if record, err := readRunTurnRecord(rt.dir, request.RunID); err == nil {
		if record.SpecHash != hash {
			return RunTurnResult{}, errors.New("run_id already has a different prompt or deadline")
		}
		// Never resend after an ambiguous egg restart/crash gap.
		if !record.Result.Terminal() {
			record.Result.Status, record.Result.FailureKind = "failed", agent.UnknownOutcome
			record.Result.Error, record.Result.EndedAt = "Egg execution was interrupted; turn outcome is unknown.", time.Now().UTC()
			if err = persistRunTurn(rt.dir, record); err != nil {
				return RunTurnResult{}, err
			}
		}
		return record.Result, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return RunTurnResult{}, err
	}
	if rt.backend.Agent != "claude" && rt.backend.Agent != "codex" {
		return RunTurnResult{}, errors.New("provider has no native run-turn adapter")
	}
	for _, run := range rt.runs {
		run.mu.Lock()
		busy := !run.record.Result.Terminal()
		run.mu.Unlock()
		if busy {
			return RunTurnResult{}, errors.New("an egg run turn is already active")
		}
	}
	result := RunTurnResult{RunID: request.RunID, SessionID: filepath.Base(rt.dir), Status: "pending", Deadline: request.Deadline.UTC()}
	run := &ownedRunTurn{record: runTurnRecord{Version: 1, SpecHash: hash, Result: result}, done: make(chan struct{}), workerDone: make(chan struct{})}
	if err := persistRunTurn(rt.dir, run.record); err != nil {
		return RunTurnResult{}, err
	}
	_, cancel := context.WithDeadline(context.Background(), request.Deadline)
	run.cancel = cancel
	rt.runs[request.RunID] = run
	// Admission is durable before arming execution, and the timer is armed before
	// the first byte can reach the PTY. It is independent of the RPC context.
	run.mu.Lock()
	run.timer = time.AfterFunc(time.Until(request.Deadline), func() { rt.expire(run, false) })
	if rt.backend.Agent == "codex" {
		run.readyTimer = time.AfterFunc(30*time.Second, func() { rt.expire(run, true) })
	}
	run.mu.Unlock()
	close(run.workerDone)
	if execute {
		run.mu.Lock()
		err := rt.begin(run, request, options)
		result = run.record.Result
		run.mu.Unlock()
		if err != nil {
			return RunTurnResult{}, err
		}
	}
	return result, nil
}

// begin is called with the runtime and run locks held. Reserving arms the
// deadline independently of readiness; only Submit authorizes prompt input.
func (rt *runTurnRuntime) begin(run *ownedRunTurn, request RunTurnRequest, options SessionPromptOptions) error {
	if run.finishing {
		return nil
	}
	view, err := rt.backend.Read(context.Background(), 0, 1)
	if err != nil {
		return err
	}
	if !NativePromptReady(view) {
		return errors.New("exact native foreground readiness is required")
	}
	scan, err := rt.backend.Prepare(request.Prompt, view.ProviderSessionID)
	if err != nil {
		return err
	}
	record := run.record
	record.Result.ProviderSessionID = view.ProviderSessionID
	record.Result.Status, record.Result.StartedAt = "running", time.Now().UTC()
	if err = persistRunTurn(rt.dir, record); err != nil {
		return err
	}
	run.record = record
	if run.readyTimer != nil {
		run.readyTimer.Stop()
	}
	run.started = true
	run.workerDone = make(chan struct{})
	run.cancel()
	ctx, cancel := context.WithDeadline(context.Background(), request.Deadline)
	run.cancel = cancel
	go rt.execute(ctx, run, options, scan)
	return nil
}

// startInitialCodex reserves and arms the deadline before the provider starts.
// Codex owns submission of its initial argv prompt; Wingthing never types it.
func (rt *runTurnRuntime) startInitialCodex(request RunTurnRequest) error {
	if rt.backend.Agent != "codex" {
		return errors.New("initial native turn requires Codex")
	}
	if _, err := rt.reserve(request); err != nil {
		return err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	run := rt.runs[request.RunID]
	if run == nil {
		return errors.New("initial turn cannot reuse a persisted run")
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.finishing || run.started {
		return errors.New("initial turn was already started or expired")
	}
	scan, err := rt.backend.Prepare(request.Prompt, "")
	if err != nil {
		return err
	}
	run.initial, run.started = true, true
	run.workerDone = make(chan struct{})
	run.cancel()
	ctx, cancel := context.WithDeadline(context.Background(), request.Deadline)
	run.cancel = cancel
	go rt.execute(ctx, run, SessionPromptOptions{}, scan)
	return nil
}

func (rt *runTurnRuntime) expire(run *ownedRunTurn, readinessOnly bool) {
	diagnostic := "Startup screen unavailable."
	if rt.backend.StartupDiagnostic != nil {
		diagnostic = rt.backend.StartupDiagnostic()
	}
	if readinessOnly {
		rt.finish(run, "failed", agent.ProviderNotReady, turnEvidence{Diagnostic: diagnostic}, true)
	} else {
		rt.finish(run, "timeout", agent.Timeout, turnEvidence{Diagnostic: diagnostic}, true)
	}
}

func (rt *runTurnRuntime) execute(ctx context.Context, run *ownedRunTurn, options SessionPromptOptions, scan func() (turnEvidence, error)) {
	defer close(run.workerDone)
	if rt.backend.Started != nil {
		select {
		case <-rt.backend.Started:
		case <-ctx.Done():
			return
		}
	}
	run.mu.Lock()
	if run.finishing {
		run.mu.Unlock()
		return
	}
	err := persistRunTurn(rt.dir, run.record)
	run.mu.Unlock()
	if err != nil {
		rt.finish(run, "failed", agent.UnknownOutcome, turnEvidence{}, false)
		return
	}
	// The existing reservation makes retries safe even across a lost receipt.
	var promptDone chan error
	if !run.initial {
		promptDone = make(chan error, 1)
		go func() { _, err := SubmitSessionPrompt(ctx, rt.dir, options); promptDone <- err }()
	}
	defer func() {
		if promptDone != nil {
			<-promptDone
		}
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	exited := false
	for {
		evidence, err := scan()
		if err != nil {
			rt.finish(run, "failed", agent.UnknownOutcome, turnEvidence{}, false)
			return
		}
		if evidence.Receipt && evidence.TurnID != "" {
			run.mu.Lock()
			if !run.finishing && run.record.Result.TurnID != evidence.TurnID {
				run.record.Result.TurnID = evidence.TurnID
				if run.initial {
					run.record.Result.ProviderSessionID = evidence.ProviderSessionID
					run.record.Result.Status, run.record.Result.StartedAt = "running", time.Now().UTC()
					run.readyTimer.Stop()
				}
				err = persistRunTurn(rt.dir, run.record)
			}
			run.mu.Unlock()
			if err != nil {
				rt.finish(run, "failed", agent.UnknownOutcome, turnEvidence{}, false)
				return
			}
		}
		if evidence.Conflict {
			rt.finish(run, "failed", agent.InputConflict, turnEvidence{}, false)
			return
		}
		if evidence.Failure != "" {
			// Native scanners exclude failure records from Text. Preserve earlier
			// ordinary assistant output without copying the provider diagnostic.
			rt.finish(run, "failed", evidence.Failure, turnEvidence{Text: evidence.Text}, false)
			return
		}
		if evidence.Receipt && evidence.Complete {
			rt.finish(run, "done", "", evidence, false)
			return
		}
		if exited {
			rt.finish(run, "failed", agent.ProviderExit, turnEvidence{}, false)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-rt.backend.Done:
			exited = true // scan once more after the native writer has exited
		case err := <-promptDone:
			if err != nil && ctx.Err() == nil {
				rt.finish(run, "failed", agent.UnknownOutcome, turnEvidence{}, false)
				return
			}
			promptDone = nil
		case <-ticker.C:
		}
	}
}

func (rt *runTurnRuntime) finish(run *ownedRunTurn, status string, kind agent.ErrorKind, evidence turnEvidence, kill bool) {
	run.mu.Lock()
	if run.finishing {
		run.mu.Unlock()
		return
	}
	if kind == agent.ProviderNotReady && run.record.Result.Status != "pending" {
		run.mu.Unlock()
		return
	}
	if kind == agent.Timeout && rt.backend.Agent == "codex" && run.record.Result.Status == "pending" {
		status, kind = "failed", agent.ProviderNotReady
	}
	if status == "done" && !time.Now().Before(run.record.Result.Deadline) {
		status, kind, evidence, kill = "timeout", agent.Timeout, turnEvidence{}, true
	}
	run.finishing = true
	run.cancel()
	if run.timer != nil {
		run.timer.Stop()
	}
	if run.readyTimer != nil {
		run.readyTimer.Stop()
	}
	result := run.record.Result
	run.mu.Unlock()
	if kill && rt.backend.Kill != nil {
		var err error
		result.SurvivingDescendants, err = rt.backend.Kill()
		if err != nil {
			result.ContainmentError = "Process-group cleanup or descendant inventory could not be verified."
		}
	}
	result.Status, result.FailureKind, result.EndedAt = status, kind, time.Now().UTC()
	result.Text = evidence.Text
	if evidence.TurnID != "" {
		result.TurnID = evidence.TurnID
	}
	if kind != "" {
		result.Error = "Egg run turn ended: " + string(kind) + "."
		if kind == agent.ProviderNotReady {
			result.Error += " " + evidence.Diagnostic
		}
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	record := run.record
	record.Result = result
	if err := persistRunTurn(rt.dir, record); err != nil {
		run.err = errors.New("could not persist egg run outcome")
	} else {
		run.record = record
	}
	// A waiter can observe done only after the entire result has been synced.
	close(run.done)
}

func (rt *runTurnRuntime) get(id string, text bool) (RunTurnResult, error) {
	rt.mu.Lock()
	run := rt.runs[id]
	rt.mu.Unlock()
	var result RunTurnResult
	var err error
	if run == nil {
		result, err = ReadRunTurnResult(rt.dir, id)
	} else {
		run.mu.Lock()
		result, err = run.record.Result, run.err
		run.mu.Unlock()
	}
	if !text {
		result.Text = ""
	}
	return result, err
}

func (rt *runTurnRuntime) wait(ctx context.Context, id string) (RunTurnResult, error) {
	rt.mu.Lock()
	run := rt.runs[id]
	rt.mu.Unlock()
	if run == nil {
		return rt.get(id, false)
	}
	select {
	case <-run.done:
		return rt.get(id, false)
	case <-ctx.Done():
		return RunTurnResult{}, ctx.Err()
	}
}

func (rt *runTurnRuntime) stop(id string) (RunTurnResult, error) {
	rt.mu.Lock()
	run := rt.runs[id]
	rt.mu.Unlock()
	if run == nil {
		return rt.get(id, false)
	}
	rt.finish(run, "stopped", agent.Stopped, turnEvidence{}, true)
	<-run.done
	return rt.get(id, false)
}
