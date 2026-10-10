package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

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

func (s *Server) wingAgentRun(arguments json.RawMessage) (map[string]any, error) {
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
	return s.wingSubmitRun(wingsession.RunRequest{Prompt: args.Prompt, Agent: args.Agent, Model: args.Model, CWD: args.CWD, Label: args.Label, TimeoutSeconds: args.TimeoutSeconds, RequestKey: args.RequestKey})
}

func (s *Server) wingSubmitRun(request wingsession.RunRequest) (map[string]any, error) {
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
	opts := eggclient.SpawnEggOpts{AgentArgs: modelArgs}
	if s.broker != nil {
		opts = s.broker.launchOpts(s.Cfg, opts)
	}
	var run *wingsession.Run
	err = s.admitSpawn(func() error {
		var err error
		run, _, err = s.Sessions.RunManager.Admit(s.sessionLaunch, request, wingsession.StartOptions{Egg: opts, Tools: s.forkTools, Trace: s.forkTrace && cfg.Trace, IdleTimeout: s.forkIdleTimeout}, s.clientActor())
		return err
	})
	if err != nil {
		return nil, err
	}
	data := wingRunStatus(run)
	if request.Label != "" {
		data["label"] = request.Label
	}
	return data, nil
}

func (s *Server) wingAgentResult(arguments json.RawMessage) (map[string]any, error) {
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

func (s *Server) wingAgentWait(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
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
