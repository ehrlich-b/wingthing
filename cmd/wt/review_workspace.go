package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/reviewjob"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type reviewWorkspaceArgs struct {
	Action    string               `json:"action"`
	ID        string               `json:"workspace_id"`
	Source    string               `json:"source"`
	Spec      *reviewjob.Spec      `json:"spec"`
	Candidate *reviewjob.Candidate `json:"candidate"`
	Run       *agentRunArgs        `json:"run"`
}

type reviewWorkspaceRecord struct {
	Workspace reviewjob.Workspace `json:"workspace"`
	Owner     string              `json:"owner"`
	Spec      reviewjob.Spec      `json:"spec"`
}

func (s *localMCPServer) toolReviewWorkspace(ctx context.Context, arguments json.RawMessage) (map[string]any, error) {
	var args reviewWorkspaceArgs
	if err := decodeStrict(arguments, &args); err != nil {
		return nil, err
	}
	policy, err := s.reviewJobPolicy()
	if err != nil {
		return nil, err
	}
	if args.Action == "prepare" {
		if args.Spec == nil || args.Source == "" || args.ID != "" || args.Candidate != nil || args.Run != nil {
			return nil, errors.New("prepare requires only source and spec")
		}
		if err := args.Spec.Validate(); err != nil {
			return nil, err
		}
		workspace, err := s.prepareReviewWorkspace(ctx, policy, args.Source, *args.Spec)
		if err != nil {
			return nil, err
		}
		return reviewData(workspace)
	}
	if args.Spec != nil || args.Source != "" || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(args.ID) {
		return nil, errors.New("workspace operation requires only its workspace_id and action payload")
	}
	if (args.Action == "apply") != (args.Candidate != nil) {
		return nil, errors.New("only apply requires candidate")
	}
	if (args.Action == "run") != (args.Run != nil) {
		return nil, errors.New("only run requires run arguments")
	}
	if args.Action != "capture" && args.Action != "apply" && args.Action != "test" && args.Action != "run" {
		return nil, errors.New("action must be prepare, capture, apply, run, or test")
	}
	stateDir := filepath.Join(s.cfg.Dir, "review-workspaces")
	data, err := os.ReadFile(filepath.Join(stateDir, args.ID+".json"))
	if err != nil {
		return nil, err
	}
	var record reviewWorkspaceRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	if record.Owner != s.clientPrincipal() || record.Workspace.ID != args.ID {
		return nil, errors.New("workspace not found or not owned by caller")
	}
	expected := filepath.Join(policy.WorkspaceRoot, args.ID)
	resolved, err := filepath.EvalSymlinks(record.Workspace.CWD)
	if err != nil || resolved != expected || record.Workspace.CWD != expected {
		return nil, errors.New("workspace path no longer matches its admission")
	}
	guard, err := os.OpenFile(filepath.Join(stateDir, args.ID+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer guard.Close()
	if err := unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.New("workspace operation already active")
	}
	switch args.Action {
	case "run":
		if !s.toolAllowed("agent_run") {
			return nil, errors.New("workspace run requires agent.run grant")
		}
		if args.Run.Agent != "codex" || args.Run.CWD != "" || args.Run.TimeoutSeconds != record.Spec.RunSeconds || (args.Run.Model != record.Spec.Implementer.Model && args.Run.Model != record.Spec.Reviewer.Model) {
			return nil, errors.New("run differs from admitted agent, model, cwd, or deadline")
		}
		args.Run.CWD = record.Workspace.CWD
		worker := &localMCPServer{
			cfg: s.cfg, logs: s.logs, principal: s.principal, actor: s.actor, identity: s.identity,
			surface: s.surface, grants: s.grants, admission: s.admission,
			maxSessions: s.maxSessions, maxSpawnsPerHour: s.maxSpawnsPerHour,
			allowedPaths: s.allowedPaths, enforcePathBounds: s.enforcePathBounds,
			runAgentTask: s.runAgentTask, forceAgentIsolation: "standard",
			agentDenyWrite: []string{filepath.Join(record.Workspace.CWD, ".git")},
			agentDeny:      []string{s.cfg.Dir},
		}
		return worker.submitAgentRun(*args.Run, nil)
	case "capture":
		candidate, err := reviewjob.Capture(ctx, record.Workspace.CWD, record.Spec.BaseCommit, record.Spec.AllowedPaths)
		if err != nil {
			return nil, err
		}
		return reviewData(candidate)
	case "apply":
		if args.Candidate.BaseCommit != record.Spec.BaseCommit {
			return nil, errors.New("candidate base differs from admitted workspace")
		}
		if err := reviewjob.Apply(ctx, record.Workspace.CWD, *args.Candidate, record.Spec.AllowedPaths); err != nil {
			return nil, err
		}
		return map[string]any{"applied": true, "sha256": args.Candidate.SHA256}, nil
	case "test":
		result, err := s.testReviewWorkspace(ctx, record)
		if err != nil {
			return nil, err
		}
		return reviewData(result)
	}
	return nil, errors.New("unreachable workspace operation")
}

func (s *localMCPServer) prepareReviewWorkspace(ctx context.Context, policy reviewJobPolicy, source string, spec reviewjob.Spec) (reviewjob.Workspace, error) {
	var empty reviewjob.Workspace
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resolved, err := s.resolveWorkingDirectory(source)
	if err != nil {
		return empty, err
	}
	allowed := false
	for _, path := range policy.Sources {
		if path == resolved {
			allowed = true
		}
	}
	if !allowed {
		return empty, errors.New("source is not enabled by this wing's review job policy")
	}
	if resolved != source {
		return empty, errors.New("source must be canonical")
	}
	root, err := filepath.EvalSymlinks(policy.WorkspaceRoot)
	if err != nil || root != policy.WorkspaceRoot {
		return empty, errors.New("workspace_root must be a canonical existing directory")
	}
	base, err := reviewGit(ctx, source, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(string(base)) != spec.BaseCommit {
		return empty, errors.New("source HEAD does not match base_commit")
	}
	baseline, err := reviewjob.Capture(ctx, source, spec.BaseCommit, spec.AllowedPaths)
	if err != nil {
		return empty, fmt.Errorf("inspect source: %w", err)
	}
	if len(baseline.Files) != 0 {
		return empty, errors.New("source replica must be clean at the pinned commit")
	}
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	cwd := filepath.Join(root, id)
	if _, err := os.Lstat(cwd); !errors.Is(err, os.ErrNotExist) {
		return empty, errors.New("workspace already exists")
	}
	if _, err := reviewGit(ctx, root, "clone", "--no-local", "--no-checkout", "--template=", "--", source, cwd); err != nil {
		return empty, fmt.Errorf("clone pinned source: %w", err)
	}
	if _, err := reviewGit(ctx, cwd, "checkout", "--detach", spec.BaseCommit); err != nil {
		return empty, fmt.Errorf("checkout pinned source: %w", err)
	}
	workspace := reviewjob.Workspace{ID: id, CWD: cwd}
	record := reviewWorkspaceRecord{Workspace: workspace, Owner: s.clientPrincipal(), Spec: spec}
	stateDir := filepath.Join(s.cfg.Dir, "review-workspaces")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return empty, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return empty, err
	}
	file, err := os.OpenFile(filepath.Join(stateDir, id+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return empty, err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return empty, err
	}
	if err := file.Sync(); err != nil {
		return empty, err
	}
	return workspace, nil
}

func reviewGit(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = cwd
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git: %w: %.2048s", err, out)
	}
	return out, nil
}

type reviewTestOutput struct {
	mu        sync.Mutex
	data      bytes.Buffer
	truncated bool
}

func (b *reviewTestOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	remain := 64000 - b.data.Len()
	if len(data) > remain {
		data = data[:remain]
		b.truncated = true
	}
	_, _ = b.data.Write(data)
	return n, nil
}

func (s *localMCPServer) testReviewWorkspace(ctx context.Context, record reviewWorkspaceRecord) (reviewjob.TestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(record.Spec.TestSeconds)*time.Second)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		return reviewjob.TestResult{}, err
	}
	cwd := record.Workspace.CWD
	eggCfg := egg.DiscoverEggConfig(cwd, nil)
	sbCfg, err := directAgentSandboxConfigForTask(eggCfg, "", "standard", home, cwd, []string{cwd}, false)
	if err != nil {
		return reviewjob.TestResult{}, err
	}
	// Tests have no provider identity or network capability.
	sbCfg.NetworkNeed = sandbox.NetworkNone
	sbCfg.Domains = nil
	sbCfg.LocalPorts = nil
	sbCfg.Deny = append(sbCfg.Deny, s.cfg.Dir)
	for _, path := range []string{".codex", ".claude", ".claude.json", ".gemini", ".cursor", ".config/opencode", ".local/share/opencode", ".local/state/opencode", ".cache/opencode", ".config/gh"} {
		sbCfg.Deny = append(sbCfg.Deny, filepath.Join(home, path))
	}
	sbCfg.DenyWrite = append(sbCfg.DenyWrite, filepath.Join(cwd, ".git"))
	sbCfg.SessionID = "review-test-" + record.Workspace.ID
	sb, err := sandbox.New(sbCfg)
	if err != nil {
		return reviewjob.TestResult{}, err
	}
	defer sb.Destroy()
	name, err := sandboxAgentExecutable(record.Spec.TestArgv[0], home, false)
	if err != nil {
		return reviewjob.TestResult{}, err
	}
	cmd, err := sb.Exec(ctx, name, record.Spec.TestArgv[1:])
	if err != nil {
		return reviewjob.TestResult{}, err
	}
	cmd.Dir = cwd
	agent.ConfigureProcessTree(cmd)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GOTOOLCHAIN=local", "GOPROXY=off"}
	var output reviewTestOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return reviewjob.TestResult{}, err
	}
	defer cmd.Cancel()
	if err := sb.PostStart(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return reviewjob.TestResult{}, err
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return reviewjob.TestResult{}, ctx.Err()
	}
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return reviewjob.TestResult{}, err
		}
		code = exit.ExitCode()
	}
	text := output.data.String()
	if output.truncated {
		text += "\n[truncated]"
	}
	digest := sha256.Sum256([]byte(text))
	return reviewjob.TestResult{Argv: record.Spec.TestArgv, ExitCode: code, Output: text, SHA256: hex.EncodeToString(digest[:])}, nil
}

func reviewData(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(data, &result)
	return result, err
}
