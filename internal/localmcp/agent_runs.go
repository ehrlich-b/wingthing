package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

var errExistingAgentRun = errors.New("run already admitted")

func wingRunStatus(r *wingsession.Run) map[string]any {
	data := map[string]any{"run_id": r.ID, "status": r.Result.Status, "agent": r.Agent, "model": r.Model, "cwd": r.CWD, "isolation": r.Isolation, "timeout_seconds": r.TimeoutSeconds, "created_at": r.CreatedAt.UTC().Format(time.RFC3339), "session_id": r.SessionID}
	if !r.Result.StartedAt.IsZero() {
		data["started_at"] = r.Result.StartedAt.UTC().Format(time.RFC3339)
	}
	if !r.Result.EndedAt.IsZero() {
		data["finished_at"] = r.Result.EndedAt.UTC().Format(time.RFC3339)
	}
	if r.Result.ProviderSessionID != "" {
		data["provider_session_id"] = r.Result.ProviderSessionID
	}
	if r.Result.TurnID != "" {
		data["turn_id"] = r.Result.TurnID
	}
	if r.Result.FailureKind != "" {
		data["failure_kind"] = r.Result.FailureKind
	}
	if len(r.Result.SurvivingDescendants) > 0 {
		data["surviving_descendants"] = r.Result.SurvivingDescendants
	}
	if r.Result.ContainmentError != "" {
		data["containment_error"] = r.Result.ContainmentError
	}
	return data
}

func (s *Server) toolAgentRun(arguments json.RawMessage) (map[string]any, error) {
	return s.toolAgentRunTask(arguments, nil)
}

func (s *Server) toolAgentRunTask(arguments json.RawMessage, task *control.MCPTaskParams) (map[string]any, error) {
	var args struct {
		Prompt         string `json:"prompt"`
		Agent          string `json:"agent"`
		Model          string `json:"model"`
		CWD            string `json:"cwd"`
		Label          string `json:"label"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		RequestKey     string `json:"idempotency_key"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	return s.wingSubmitRun(wingsession.RunRequest{Prompt: args.Prompt, Agent: args.Agent, Model: args.Model, CWD: args.CWD, Label: args.Label, TimeoutSeconds: args.TimeoutSeconds, RequestKey: args.RequestKey, MCPTask: task})
}

func (s *Server) wingSubmitRun(request wingsession.RunRequest) (map[string]any, error) {
	if s.Sessions == nil || s.Sessions.RunManager == nil {
		return nil, errors.New("wing run service is not ready")
	}
	if strings.TrimSpace(request.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	if request.Agent == "" {
		request.Agent = s.Cfg.DefaultAgent
	}
	if request.TimeoutSeconds == 0 {
		request.TimeoutSeconds = 900
	}
	if err := eggclient.ValidateSessionName(request.Label); err != nil {
		return nil, err
	}
	modelArgs, err := agentModelArgs(request.Agent, request.Model)
	if err != nil {
		return nil, err
	}
	cwd, err := s.resolveWorkingDirectory(request.CWD)
	if err != nil {
		return nil, err
	}
	cfg, err := s.loadSessionLaunchConfig(cwd)
	if err != nil {
		return nil, err
	}
	if saved, err := s.Sessions.RunManager.Retry(s.sessionLaunch, request); err != nil {
		return nil, err
	} else if saved != nil {
		data := wingRunStatus(saved)
		if saved.Label != "" {
			data["label"] = saved.Label
		}
		return data, nil
	}
	opts := eggclient.SpawnEggOpts{AgentArgs: modelArgs}
	if s.broker != nil {
		opts = s.broker.launchOpts(s.Cfg, opts)
	}
	var run *wingsession.Run
	err = s.admitSpawn(func() error {
		var err error
		var fresh bool
		run, fresh, err = s.Sessions.RunManager.Admit(s.sessionLaunch, request, wingsession.StartOptions{Egg: opts, Tools: s.forkTools, Trace: s.forkTrace && cfg.Trace, IdleTimeout: s.forkIdleTimeout}, s.clientActor())
		if err == nil && !fresh {
			return errExistingAgentRun
		}
		return err
	})
	if errors.Is(err, errExistingAgentRun) {
		err = nil
	}
	if err != nil && request.RequestKey != "" {
		saved, retryErr := s.Sessions.RunManager.Retry(s.sessionLaunch, request)
		if retryErr != nil {
			return nil, retryErr
		}
		if saved != nil {
			run = saved
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	data := wingRunStatus(run)
	if request.Label != "" {
		data["label"] = request.Label
	}
	return data, nil
}

func (s *Server) toolAgentResult(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID    string `json:"run_id"`
		MaxChars int    `json:"max_chars"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.MaxChars == 0 {
		args.MaxChars = 50000
	}
	if args.MaxChars < 1 || args.MaxChars > 200000 {
		return nil, errors.New("max_chars must be between 1 and 200000")
	}
	if s.Sessions == nil || s.Sessions.RunManager == nil {
		return nil, errors.New("wing run service is not ready")
	}
	r, err := s.Sessions.RunManager.Get(s.sessionAuthority(), args.RunID)
	if err != nil {
		return nil, err
	}
	data := wingRunStatus(r)
	data["ready"] = r.Result.Terminal()
	if r.Result.Terminal() {
		text := []rune(r.Result.Text)
		if len(text) > args.MaxChars {
			data["truncated"] = true
			data["total_chars"] = len(text)
			text = text[:args.MaxChars]
		}
		data["output"] = string(text)
	}
	if r.Result.Error != "" {
		data["error"] = r.Result.Error
	}
	return data, nil
}

func (s *Server) toolAgentWait(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID          string  `json:"run_id"`
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 30
	}
	if args.TimeoutSeconds < 0.1 || args.TimeoutSeconds > 3600 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 3600")
	}
	if s.Sessions == nil || s.Sessions.RunManager == nil {
		return nil, errors.New("wing run service is not ready")
	}
	m := s.Sessions.RunManager
	a := s.sessionAuthority()
	if _, err := m.Get(a, args.RunID); err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, durationSeconds(args.TimeoutSeconds))
	defer cancel()
	err := m.Wait(waitCtx, a, []string{args.RunID})
	r, loadErr := m.Get(a, args.RunID)
	if loadErr != nil {
		return nil, loadErr
	}
	data := wingRunStatus(r)
	if errors.Is(err, context.DeadlineExceeded) {
		data["timed_out"] = true
		return data, nil
	}
	return data, err
}

func (s *Server) toolAgentStatus(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID string `json:"run_id"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if s.Sessions == nil || s.Sessions.RunManager == nil {
		return nil, errors.New("wing run service is not ready")
	}
	r, err := s.Sessions.RunManager.Get(s.sessionAuthority(), args.RunID)
	if err != nil {
		return nil, err
	}
	return wingRunStatus(r), nil
}

func (s *Server) toolAgentEvents(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID  string `json:"run_id"`
		Limit  int    `json:"limit"`
		Cursor int64  `json:"cursor"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if args.Limit == 0 {
		args.Limit = 50
	}
	if args.Limit < 1 || args.Limit > 200 {
		return nil, errors.New("limit must be between 1 and 200")
	}
	if args.Cursor < 0 {
		return nil, errors.New("cursor must be nonnegative")
	}
	if s.Sessions == nil || s.Sessions.RunManager == nil {
		return nil, errors.New("wing run service is not ready")
	}
	entries, err := s.Sessions.RunManager.Events(s.sessionAuthority(), args.RunID, args.Cursor, args.Limit)
	if err != nil {
		return nil, err
	}
	events := []map[string]any{}
	var cursor int64
	for _, e := range entries {
		entry := map[string]any{"timestamp": e.Timestamp, "event": e.Event, "cursor": e.Cursor}
		if e.Detail != "" {
			entry["detail"] = e.Detail
		}
		events = append(events, entry)
		cursor = e.Cursor
	}
	data := map[string]any{"run_id": args.RunID, "events": events}
	if cursor > 0 {
		data["next_cursor"] = cursor
	}
	return data, nil
}

func (s *Server) toolAgentSteer(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID  string `json:"run_id"`
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return nil, errors.New("prompt is required")
	}
	if s.Sessions == nil || s.Sessions.RunManager == nil {
		return nil, errors.New("wing run service is not ready")
	}
	r, err := s.Sessions.RunManager.Get(s.sessionAuthority(), args.RunID)
	if err != nil {
		return nil, err
	}
	if args.Model == "" {
		args.Model = r.Model
	}
	return s.wingSubmitRun(wingsession.RunRequest{Prompt: "Prior request:\n" + r.OriginalPrompt + "\n\nNew direction:\n" + args.Prompt, Agent: r.Agent, Model: args.Model, CWD: r.CWD, Label: "followup-" + r.ID, TimeoutSeconds: r.TimeoutSeconds, ParentID: r.ID, Direction: args.Prompt})
}

func (s *Server) toolAgentStop(arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunID string `json:"run_id"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if s.Sessions == nil || s.Sessions.RunManager == nil {
		return nil, errors.New("wing run service is not ready")
	}
	r, err := s.Sessions.RunManager.Stop(s.sessionAuthority(), args.RunID)
	if err != nil {
		return nil, err
	}
	data := wingRunStatus(r)
	if r.Result.Status == "stopped" {
		data["stopped"] = true
	}
	return data, nil
}

func (s *Server) toolAgentWaitAny(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args struct {
		RunIDs         []string `json:"run_ids"`
		TimeoutSeconds float64  `json:"timeout_seconds"`
	}
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if len(args.RunIDs) < 1 || len(args.RunIDs) > 64 {
		return nil, errors.New("run_ids must contain between 1 and 64 IDs")
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 30
	}
	if args.TimeoutSeconds < 0.1 || args.TimeoutSeconds > 600 {
		return nil, errors.New("timeout_seconds must be between 0.1 and 600")
	}
	release, err := s.admitAgentWaitAny()
	if err != nil {
		return nil, err
	}
	defer release()
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range args.RunIDs {
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if s.Sessions == nil || s.Sessions.RunManager == nil {
		return nil, errors.New("wing run service is not ready")
	}
	m := s.Sessions.RunManager
	a := s.sessionAuthority()
	waitCtx, cancel := context.WithTimeout(ctx, durationSeconds(args.TimeoutSeconds))
	defer cancel()
	err = m.Wait(waitCtx, a, ids)
	finished := []map[string]any{}
	pending := []string{}
	failures := []map[string]any{}
	for _, id := range ids {
		r, err := m.Get(a, id)
		if err != nil {
			failures = append(failures, map[string]any{"run_id": id, "error": "agent run " + strconv.Quote(id) + " not found or not owned by caller"})
		} else if r.Result.Terminal() {
			finished = append(finished, map[string]any{"run_id": id, "status": r.Result.Status})
		} else {
			pending = append(pending, id)
		}
	}
	data := map[string]any{"finished": finished, "pending": pending}
	if len(failures) > 0 {
		data["errors"] = failures
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return data, nil
	}
	return data, err
}
