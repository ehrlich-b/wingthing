package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/reviewjob"
	"github.com/ehrlich-b/wingthing/internal/store"
	"gopkg.in/yaml.v3"
)

func reviewServerFixture(t *testing.T) (*localMCPServer, reviewJobPolicy) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Dir: filepath.Join(root, "state")}
	workspaceRoot := filepath.Join(root, "workspaces")
	if err := os.MkdirAll(workspaceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveWingConfig(cfg.Dir, &config.WingConfig{Paths: config.PathList{{Path: root}}}); err != nil {
		t.Fatal(err)
	}
	s := &localMCPServer{cfg: cfg, logs: io.Discard, surface: control.SurfaceDirectMCP, principal: roostSessionPrincipal("alice"), identity: EggIdentity{UserID: "alice"}, grants: grantSet(defaultDirectMCPGrants)}
	policy := reviewJobPolicy{OwnerPrincipal: s.principal, WorkspaceRoot: workspaceRoot, Sources: []string{filepath.Join(root, "source")}}
	data, err := yaml.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Dir, "review-jobs.yaml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return s, policy
}

func TestReviewJobPolicyOwnerAndSharedHostBoundaries(t *testing.T) {
	s, _ := reviewServerFixture(t)
	if _, err := s.reviewJobPolicy(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*localMCPServer){
		func(s *localMCPServer) { s.principal = roostSessionPrincipal("bob"); s.identity.UserID = "bob" },
		func(s *localMCPServer) { s.identity.SharedHost = true },
		func(s *localMCPServer) { s.identity.OrgWing = true },
		func(s *localMCPServer) { s.identity.UserID = "" },
	} {
		other := *s
		change(&other)
		if _, err := other.reviewJobPolicy(); err == nil {
			t.Fatal("unauthorized policy accepted")
		}
	}
	if err := config.SaveWingConfig(s.cfg.Dir, &config.WingConfig{Org: "organization"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.reviewJobPolicy(); err == nil {
		t.Fatal("organization config accepted through personal adapter")
	}
}

func TestReviewJobListDiscoversOwnerEvidenceAndPaginates(t *testing.T) {
	s, _ := reviewServerFixture(t)
	dir := filepath.Join(s.cfg.Dir, "review-jobs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for i, owner := range []string{s.principal, roostSessionPrincipal("bob"), s.principal} {
		job := reviewjob.Job{ID: fmt.Sprintf("j-%032x", i+1), Owner: owner, Status: "succeeded", Stage: "terminal", CreatedAt: time.Unix(int64(i), 0), UpdatedAt: time.Unix(int64(i+1), 0), Spec: reviewjob.Spec{Implementer: reviewjob.Target{WingID: "terra-wing"}, Reviewer: reviewjob.Target{WingID: "sol-wing"}}, Rounds: []reviewjob.Round{{Number: 0, Candidate: &reviewjob.Candidate{Patch: "patch", SHA256: "digest"}, Review: &reviewjob.Review{Verdict: "pass"}, ImplementerTest: &reviewjob.TestResult{WorkspaceID: "implementation-test"}, ReviewerTest: &reviewjob.TestResult{WorkspaceID: "review-test"}}}}
		data, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, job.ID+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, bad, protocolErr := s.callTool(context.Background(), "review_job_list", json.RawMessage(`{"limit":1}`))
	if bad || protocolErr != nil {
		t.Fatalf("list failed: %v %v %v", first, bad, protocolErr)
	}
	rows := first["jobs"].([]map[string]any)
	if len(rows) != 1 || rows[0]["job_id"] != fmt.Sprintf("j-%032x", 3) || rows[0]["final_evidence_available"] != true {
		t.Fatalf("first page: %v", first)
	}
	for _, key := range []string{"job_id", "status", "stage", "created_at", "updated_at", "implementer", "reviewer", "final_evidence_available"} {
		if _, ok := rows[0][key]; !ok {
			t.Fatalf("missing discovery field %s", key)
		}
	}
	args, _ := json.Marshal(map[string]any{"limit": 1, "cursor": first["next_cursor"]})
	second, bad, protocolErr := s.callTool(context.Background(), "review_job_list", args)
	if bad || protocolErr != nil {
		t.Fatalf("second page: %v %v %v", second, bad, protocolErr)
	}
	rows = second["jobs"].([]map[string]any)
	if len(rows) != 1 || rows[0]["job_id"] != fmt.Sprintf("j-%032x", 1) || second["next_cursor"] != "" {
		t.Fatalf("other owner leaked or pagination failed: %v", second)
	}
	for _, args := range []string{`{"limit":0}`, `{"limit":101}`, `{"cursor":"../escape"}`, fmt.Sprintf(`{"cursor":"j-%032x"}`, 2), `{"owner":"bob"}`} {
		_, bad, protocolErr := s.callTool(context.Background(), "review_job_list", json.RawMessage(args))
		if !bad && protocolErr == nil {
			t.Fatalf("list accepted %s", args)
		}
	}
	other := *s
	other.principal, other.identity.UserID = roostSessionPrincipal("bob"), "bob"
	if data, bad, protocolErr := other.callTool(context.Background(), "review_job_list", json.RawMessage(`{}`)); !bad && protocolErr == nil {
		t.Fatalf("other owner listed jobs: %v", data)
	}
}

func TestReviewJobListEmptyAndInterrupted(t *testing.T) {
	s, _ := reviewServerFixture(t)
	data, bad, protocolErr := s.callTool(context.Background(), "review_job_list", json.RawMessage(`{}`))
	if bad || protocolErr != nil || len(data["jobs"].([]map[string]any)) != 0 {
		t.Fatalf("empty list: %v %v %v", data, bad, protocolErr)
	}
	dir := filepath.Join(s.cfg.Dir, "review-jobs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	job := reviewjob.Job{ID: "j-" + strings.Repeat("a", 32), Owner: s.principal, Status: "running"}
	encoded, _ := json.Marshal(job)
	if err := os.WriteFile(filepath.Join(dir, job.ID+".json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	data, bad, protocolErr = s.callTool(context.Background(), "review_job_list", json.RawMessage(`{}`))
	if bad || protocolErr != nil {
		t.Fatalf("interrupted list: %v %v %v", data, bad, protocolErr)
	}
	rows := data["jobs"].([]map[string]any)
	if len(rows) != 1 || rows[0]["status"] != "interrupted" || rows[0]["final_evidence_available"] != false {
		t.Fatalf("abandoned job was hidden or claimed complete: %v", data)
	}
}

func TestReviewJobStrictSchemasAndGrants(t *testing.T) {
	s, _ := reviewServerFixture(t)
	for _, name := range []string{"review_job_submit", "review_job_list", "review_job_status", "review_job_result", "review_workspace"} {
		tool, ok := control.Lookup(name)
		if !ok || tool.InputSchema["additionalProperties"] != false || !tool.Supports(control.SurfaceDirectMCP) || !tool.Supports(control.SurfaceHTTPMCP) {
			t.Fatalf("bad tool contract: %+v", tool)
		}
		_, bad, protocolErr := s.callTool(context.Background(), name, json.RawMessage(`{"unexpected":true}`))
		if !bad && protocolErr == nil {
			t.Fatalf("%s accepted unknown field", name)
		}
		denied := *s
		denied.grants = map[string]bool{}
		data, bad, protocolErr := denied.callTool(context.Background(), name, json.RawMessage(`{}`))
		if !bad || protocolErr != nil || !strings.Contains(data["error"].(string), "lacks grant") {
			t.Fatalf("%s grant: %v %v %v", name, data, bad, protocolErr)
		}
	}
	for _, args := range []string{`{"action":"capture","workspace_id":"../outside"}`, `{"action":"test","workspace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","source":"/"}`, `{"action":"apply","workspace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`} {
		if _, err := s.toolReviewWorkspace(context.Background(), json.RawMessage(args)); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
}

func TestReviewWorkspacePinnedReplicaAndOwner(t *testing.T) {
	s, policy := reviewServerFixture(t)
	source := policy.Sources[0]
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "egg.yaml"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("baseline\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--template=")
	git("add", ".")
	git("-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--no-gpg-sign", "-m", "base")
	base := git("rev-parse", "HEAD")
	spec := reviewjob.Spec{RequestID: "request-0001", Prompt: "Change a.txt", BaseCommit: base, Implementer: reviewjob.Target{WingID: "implementer-wing", Source: source, Model: "terra"}, Reviewer: reviewjob.Target{WingID: "reviewer-wing", Source: source, Model: "sol"}, AllowedPaths: []string{"a.txt"}, TestArgv: []string{"true"}, MaxRevisions: 2, RunSeconds: 10, TestSeconds: 10, TimeoutSeconds: 60}
	args, _ := json.Marshal(reviewWorkspaceArgs{Action: "prepare", Source: source, Spec: &spec})
	data, err := s.toolReviewWorkspace(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	id := data["workspace_id"].(string)
	cwd := data["cwd"].(string)
	if cwd == source || !strings.HasPrefix(cwd, policy.WorkspaceRoot+string(filepath.Separator)) {
		t.Fatalf("not isolated replica: %s", cwd)
	}
	other := *s
	other.principal = roostSessionPrincipal("bob")
	other.identity.UserID = "bob"
	readArgs, _ := json.Marshal(reviewWorkspaceArgs{Action: "capture", ID: id})
	if _, err := other.toolReviewWorkspace(context.Background(), readArgs); err == nil {
		t.Fatal("other owner read workspace")
	}
	if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("candidate\n"), 0644); err != nil {
		t.Fatal(err)
	}
	captured, err := s.toolReviewWorkspace(context.Background(), readArgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured["patch"].(string), "candidate") {
		t.Fatalf("missing candidate: %v", captured)
	}
	seen := make(chan taskRunOptions, 1)
	s.runAgentTask = func(ctx context.Context, _ *config.Config, taskStore *store.Store, task *store.Task, options taskRunOptions) error {
		if task.Isolation != "standard" || task.CWD != cwd {
			return fmt.Errorf("unexpected admitted task: isolation=%s cwd=%s", task.Isolation, task.CWD)
		}
		seen <- options
		return taskStore.UpdateTaskStatus(task.ID, "done")
	}
	runArgs, _ := json.Marshal(reviewWorkspaceArgs{Action: "run", ID: id, Run: &agentRunArgs{Agent: "codex", Model: "terra", Prompt: "test", TimeoutSeconds: 10}})
	started, err := s.toolReviewWorkspace(context.Background(), runArgs)
	if err != nil {
		t.Fatal(err)
	}
	runID := started["run_id"].(string)
	if active, ok := activeMCPAgentRuns.Load(s.agentRunKey(runID)); ok {
		t.Cleanup(func() {
			run := active.(activeMCPAgentRun)
			run.cancel()
			select {
			case <-run.done:
			case <-time.After(5 * time.Second):
				t.Error("run cleanup timed out")
			}
		})
	}
	select {
	case options := <-seen:
		if len(options.DenyWrite) != 1 || options.DenyWrite[0] != filepath.Join(cwd, ".git") {
			t.Fatalf("Git metadata not protected: %+v", options)
		}
		if len(options.Deny) != 1 || options.Deny[0] != s.cfg.Dir {
			t.Fatalf("coordinator state not denied: %+v", options)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("semantic workspace run did not execute")
	}
	original, err := os.ReadFile(filepath.Join(source, "a.txt"))
	if err != nil || string(original) != "baseline\n" {
		t.Fatal("source modified")
	}
	args, _ = json.Marshal(reviewWorkspaceArgs{Action: "prepare", Source: filepath.Dir(source), Spec: &spec})
	if _, err := s.toolReviewWorkspace(context.Background(), args); err == nil {
		t.Fatal("source policy bypassed")
	}
}
