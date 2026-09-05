package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/reviewjob"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type reviewJobPolicy struct {
	OwnerPrincipal string   `yaml:"owner_principal"`
	WorkspaceRoot  string   `yaml:"workspace_root"`
	Sources        []string `yaml:"sources"`
}

func (s *localMCPServer) reviewJobPolicy() (reviewJobPolicy, error) {
	var policy reviewJobPolicy
	if s.identity.UserID == "" || s.identity.SharedHost || s.identity.OrgWing {
		return policy, errors.New("review jobs require an authenticated personal wing owner; shared and org hosts are not supported")
	}
	wingCfg, err := config.LoadWingConfig(s.cfg.Dir)
	if err != nil || wingCfg.Org != "" {
		return policy, errors.New("review jobs are unavailable on an organization wing")
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.Dir, "review-jobs.yaml"))
	if err != nil {
		return policy, errors.New("review jobs are not enabled by this wing's operator")
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&policy); err != nil {
		return policy, fmt.Errorf("review job policy: %w", err)
	}
	if policy.OwnerPrincipal != s.clientPrincipal() || policy.OwnerPrincipal != roostSessionPrincipal(s.identity.UserID) {
		return policy, errors.New("review jobs are not granted to this owner")
	}
	if !filepath.IsAbs(policy.WorkspaceRoot) || len(policy.Sources) == 0 {
		return policy, errors.New("review job policy requires an absolute workspace_root and explicit sources")
	}
	if _, err := s.resolveWorkingDirectory(policy.WorkspaceRoot); err != nil {
		return policy, err
	}
	return policy, nil
}

func (s *localMCPServer) toolReviewJobSubmit(arguments json.RawMessage) (map[string]any, error) {
	var spec reviewjob.Spec
	if err := decodeStrict(arguments, &spec); err != nil {
		return nil, err
	}
	if _, err := s.reviewJobPolicy(); err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	connector, err := newReviewConnector(s.cfg, "review-job", "")
	if err != nil {
		return nil, err
	}
	backend := &nativeReviewBackend{connector: connector}
	engine := reviewjob.Engine{Dir: filepath.Join(s.cfg.Dir, "review-jobs")}
	job, err := engine.Submit(s.clientPrincipal(), spec, backend)
	if err != nil {
		backend.Close()
		return nil, err
	}
	return reviewJobSummary(job), nil
}

type reviewJobReadArgs struct {
	JobID    string `json:"job_id"`
	Artifact string `json:"artifact"`
	Round    *int   `json:"round"`
}

func (s *localMCPServer) toolReviewJobRead(arguments json.RawMessage, result bool) (map[string]any, error) {
	var args reviewJobReadArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	if !result && (args.Artifact != "" || args.Round != nil) {
		return nil, errors.New("status accepts only job_id")
	}
	if _, err := s.reviewJobPolicy(); err != nil {
		return nil, err
	}
	job, err := (reviewjob.Engine{Dir: filepath.Join(s.cfg.Dir, "review-jobs")}).Get(s.clientPrincipal(), args.JobID)
	if err != nil {
		return nil, err
	}
	data := reviewJobSummary(job)
	if !result || args.Artifact == "" {
		return data, nil
	}
	n := len(job.Rounds) - 1
	if args.Round != nil {
		n = *args.Round
	}
	if n < 0 || n >= len(job.Rounds) {
		return nil, errors.New("round is not available")
	}
	round := job.Rounds[n]
	data["round"] = n
	data["artifact"] = args.Artifact
	switch args.Artifact {
	case "patch":
		if round.Candidate == nil {
			return nil, errors.New("candidate not available")
		}
		data["patch"] = round.Candidate.Patch
		data["sha256"] = round.Candidate.SHA256
		data["base_commit"] = round.Candidate.BaseCommit
	case "tests":
		data["implementation"] = round.ImplementerTest
		data["review"] = round.ReviewerTest
	case "review":
		data["review"] = round.Review
		data["output"] = round.ReviewOutput
	case "implementation":
		data["output"] = round.Implementation
	default:
		return nil, errors.New("artifact must be patch, tests, review, or implementation")
	}
	return data, nil
}

func reviewJobSummary(job reviewjob.Job) map[string]any {
	rounds := make([]map[string]any, 0, len(job.Rounds))
	for _, r := range job.Rounds {
		row := map[string]any{"round": r.Number, "implementer_run": r.ImplementerRun, "reviewer_run": r.ReviewerRun}
		if r.Candidate != nil {
			row["patch_sha256"] = r.Candidate.SHA256
		}
		if r.Review != nil {
			row["verdict"] = r.Review.Verdict
		}
		if r.ImplementerTest != nil {
			row["implementation_test_exit_code"] = r.ImplementerTest.ExitCode
		}
		if r.ReviewerTest != nil {
			row["review_test_exit_code"] = r.ReviewerTest.ExitCode
		}
		rounds = append(rounds, row)
	}
	return map[string]any{"job_id": job.ID, "status": job.Status, "stage": job.Stage, "error": job.Error, "ready": job.Terminal(), "created_at": job.CreatedAt, "updated_at": job.UpdatedAt, "deadline": job.Deadline, "base_commit": job.Spec.BaseCommit, "implementer": job.Spec.Implementer, "reviewer": job.Spec.Reviewer, "rounds": rounds}
}

func newReviewConnector(cfg *config.Config, actor, roost string) (*connectMCPServer, error) {
	tokens := auth.NewTokenStore(cfg.Dir)
	token, err := tokens.Load()
	if err != nil || !tokens.IsValid(token) {
		return nil, errors.New("coordinator requires its own Wingthing login")
	}
	key, err := auth.LoadPrivateKey(cfg.Dir)
	if err != nil {
		return nil, err
	}
	return &connectMCPServer{actor: actor, timeout: 15 * time.Second, tunnel: &ws.TunnelClient{RelayURL: finderRelayURL(cfg, roost), DeviceToken: token.Token, PrivKey: key, KnownWingsPath: filepath.Join(cfg.Dir, "known_wings.json")}}, nil
}

type nativeReviewBackend struct{ connector *connectMCPServer }

func (b *nativeReviewBackend) call(ctx context.Context, target reviewjob.Target, op string, args any, out any) error {
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var qualified map[string]any
	if err := json.Unmarshal(encoded, &qualified); err != nil {
		return err
	}
	qualified["wing_id"] = target.WingID
	encoded, err = json.Marshal(qualified)
	if err != nil {
		return err
	}
	data, isError, err := b.connector.callTool(ctx, op, encoded)
	if err != nil {
		return err
	}
	if isError {
		return fmt.Errorf("%s: %v", op, data["error"])
	}
	encoded, err = json.Marshal(data)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(encoded, out)
	}
	return nil
}

func (b *nativeReviewBackend) CheckOwner(ctx context.Context, t reviewjob.Target, owner string) error {
	var data struct {
		Principal string `json:"principal"`
	}
	if err := b.call(ctx, t, "wingthing_capabilities", map[string]any{}, &data); err != nil {
		return err
	}
	if data.Principal != owner {
		return errors.New("outbound worker identity differs from submitting owner")
	}
	return nil
}
func (b *nativeReviewBackend) Prepare(ctx context.Context, t reviewjob.Target, spec reviewjob.Spec) (reviewjob.Workspace, error) {
	var result reviewjob.Workspace
	err := b.call(ctx, t, "review_workspace", map[string]any{"action": "prepare", "source": t.Source, "spec": spec}, &result)
	return result, err
}
func (b *nativeReviewBackend) Run(ctx context.Context, t reviewjob.Target, w reviewjob.Workspace, prompt string, seconds int) (string, error) {
	var data struct {
		ID string `json:"run_id"`
	}
	err := b.call(ctx, t, "review_workspace", map[string]any{"action": "run", "workspace_id": w.ID, "run": agentRunArgs{Prompt: prompt, Agent: "codex", Model: t.Model, TimeoutSeconds: seconds, Label: "review-job"}}, &data)
	return data.ID, err
}
func (b *nativeReviewBackend) Wait(ctx context.Context, t reviewjob.Target, id string) (reviewjob.RunResult, error) {
	for {
		var data reviewjob.RunResult
		if err := b.call(ctx, t, "agent_wait", map[string]any{"run_id": id, "timeout_seconds": 20}, &data); err != nil {
			return data, err
		}
		if data.Status == "pending" || data.Status == "running" {
			continue
		}
		err := b.call(ctx, t, "agent_result", map[string]any{"run_id": id}, &data)
		return data, err
	}
}
func (b *nativeReviewBackend) Stop(ctx context.Context, t reviewjob.Target, id string) error {
	return b.call(ctx, t, "agent_stop", map[string]any{"run_id": id}, nil)
}
func (b *nativeReviewBackend) Capture(ctx context.Context, t reviewjob.Target, w reviewjob.Workspace) (reviewjob.Candidate, error) {
	var data reviewjob.Candidate
	err := b.call(ctx, t, "review_workspace", map[string]any{"action": "capture", "workspace_id": w.ID}, &data)
	return data, err
}
func (b *nativeReviewBackend) Apply(ctx context.Context, t reviewjob.Target, w reviewjob.Workspace, c reviewjob.Candidate) error {
	return b.call(ctx, t, "review_workspace", map[string]any{"action": "apply", "workspace_id": w.ID, "candidate": c}, nil)
}
func (b *nativeReviewBackend) Test(ctx context.Context, t reviewjob.Target, w reviewjob.Workspace) (reviewjob.TestResult, error) {
	var data reviewjob.TestResult
	err := b.call(ctx, t, "review_workspace", map[string]any{"action": "test", "workspace_id": w.ID}, &data)
	return data, err
}
func (b *nativeReviewBackend) Close() { b.connector.close() }

func reviewCmd() *cobra.Command {
	root := &cobra.Command{Use: "review", Short: "Submit and inspect a fixed VM-owned implementation/review job"}
	for _, verb := range []string{"submit", "status", "result"} {
		verb := verb
		var wingID, roost, specPath, artifact string
		var jsonOutput bool
		cmd := &cobra.Command{Use: verb + " [job-id]", Args: func(cmd *cobra.Command, args []string) error {
			if verb == "submit" {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		}, RunE: func(cmd *cobra.Command, args []string) error {
			if wingID == "" {
				return errors.New("--wing-id is required")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			connector, err := newReviewConnector(cfg, "review-cli", roost)
			if err != nil {
				return err
			}
			defer connector.close()
			values := map[string]any{"wing_id": wingID}
			if verb == "submit" {
				if specPath == "" {
					return errors.New("--spec is required")
				}
				file, err := os.Open(specPath)
				if err != nil {
					return err
				}
				defer file.Close()
				data, err := io.ReadAll(io.LimitReader(file, 128*1024+1))
				if err != nil {
					return err
				}
				if len(data) > 128*1024 {
					return errors.New("spec exceeds 128 KiB")
				}
				var spec reviewjob.Spec
				if err := decodeStrict(data, &spec); err != nil {
					return err
				}
				if err := spec.Validate(); err != nil {
					return err
				}
				data, _ = json.Marshal(spec)
				if err := json.Unmarshal(data, &values); err != nil {
					return err
				}
			} else {
				values["job_id"] = args[0]
				if artifact != "" {
					values["artifact"] = artifact
				}
			}
			data, _ := json.Marshal(values)
			result, isError, err := connector.callTool(cmd.Context(), "review_job_"+verb, data)
			if err != nil {
				return err
			}
			if isError {
				return fmt.Errorf("%v", result["error"])
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%v: %v (%v)\n", result["job_id"], result["status"], result["stage"])
			return err
		}}
		cmd.Flags().StringVar(&wingID, "wing-id", "", "coordinator wing ID")
		cmd.Flags().StringVar(&roost, "roost", "", "private coordination roost URL")
		cmd.Flags().BoolVar(&jsonOutput, "json", false, "print structured result")
		if verb == "submit" {
			cmd.Flags().StringVar(&specPath, "spec", "", "JSON job specification")
		}
		if verb == "result" {
			cmd.Flags().StringVar(&artifact, "artifact", "", "patch, tests, review, or implementation")
		}
		root.AddCommand(cmd)
	}
	return root
}
