package reviewjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type jobBackend struct {
	mu              sync.Mutex
	gate            chan struct{}
	started         chan struct{}
	runs            []string
	reviewPrompts   []string
	verdicts        []string
	testFailures    int
	checks          int
	ownerError      error
	runError        error
	waitError       error
	stops           int
	closed          chan struct{}
	prepared        int
	modelWorkspaces map[string]bool
}

func fakeBackend() *jobBackend {
	return &jobBackend{started: make(chan struct{}, 10), closed: make(chan struct{}), modelWorkspaces: map[string]bool{}}
}
func (b *jobBackend) CheckOwner(_ context.Context, _ Target, _ string) error { return b.ownerError }
func (b *jobBackend) Prepare(_ context.Context, _ Target, _ Spec) (Workspace, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prepared++
	return Workspace{ID: fmt.Sprintf("workspace-%d", b.prepared), CWD: fmt.Sprintf("/workspace/%d", b.prepared)}, nil
}
func (b *jobBackend) Run(_ context.Context, target Target, workspace Workspace, prompt string, _ int) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.runs = append(b.runs, target.WingID)
	b.modelWorkspaces[workspace.ID] = true
	if target.WingID == "reviewer-wing" {
		b.reviewPrompts = append(b.reviewPrompts, prompt)
	}
	b.started <- struct{}{}
	return fmt.Sprintf("run-%s-%d", target.WingID, len(b.runs)), b.runError
}
func (b *jobBackend) Wait(ctx context.Context, _ Target, id string) (RunResult, error) {
	if b.gate != nil {
		select {
		case <-b.gate:
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		}
	}
	if b.waitError != nil {
		return RunResult{}, b.waitError
	}
	if strings.Contains(id, "reviewer") {
		b.mu.Lock()
		verdict := "pass"
		if len(b.verdicts) > 0 {
			verdict = b.verdicts[0]
			b.verdicts = b.verdicts[1:]
		}
		b.mu.Unlock()
		output, _ := json.Marshal(Review{Verdict: verdict, PatchSHA256: "candidate-digest", Summary: "independent assessment"})
		return RunResult{Status: "done", Output: "Reviewing the patch.\n" + string(output), FinalOutput: string(output)}, nil
	}
	return RunResult{Status: "done", Output: "implementation complete"}, nil
}
func (b *jobBackend) Stop(context.Context, Target, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stops++
	return nil
}
func (b *jobBackend) Capture(context.Context, Target, Workspace) (Candidate, error) {
	return Candidate{BaseCommit: strings.Repeat("a", 40), Patch: "exact candidate patch", SHA256: "candidate-digest"}, nil
}
func (b *jobBackend) Apply(context.Context, Target, Workspace, Candidate) error { return nil }
func (b *jobBackend) Test(_ context.Context, _ Target, workspace Workspace) (TestResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checks++
	if b.modelWorkspaces[workspace.ID] {
		return TestResult{}, errors.New("authoritative tests used a mutable model workspace")
	}
	code := 0
	if b.testFailures > 0 {
		b.testFailures--
		code = 1
	}
	return TestResult{Argv: []string{"make", "test"}, ExitCode: code, Output: "test evidence", SHA256: "test-digest"}, nil
}
func (b *jobBackend) Close() { close(b.closed) }

func jobSpec() Spec {
	return Spec{RequestID: "request-0001", Prompt: "Add a regression", BaseCommit: strings.Repeat("a", 40), Implementer: Target{WingID: "implementer-wing", Source: "/source", Model: "terra"}, Reviewer: Target{WingID: "reviewer-wing", Source: "/source", Model: "sol"}, AllowedPaths: []string{"regression_test.go"}, TestArgv: []string{"make", "test"}, MaxRevisions: 2, RunSeconds: 10, TestSeconds: 10, TimeoutSeconds: 30}
}

func awaitJob(t *testing.T, e Engine, owner, id string, b *jobBackend) Job {
	t.Helper()
	select {
	case <-b.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("job did not finish")
	}
	j, err := e.Get(owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if !j.Terminal() {
		t.Fatalf("not terminal: %+v", j)
	}
	return j
}

func TestReviewJobDisconnectAndDedupe(t *testing.T) {
	e := Engine{Dir: t.TempDir()}
	b := fakeBackend()
	b.gate = make(chan struct{})
	j, err := e.Submit("alice", jobSpec(), b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-b.gate:
		default:
			close(b.gate)
		}
		select {
		case <-b.closed:
		case <-time.After(5 * time.Second):
		}
	})
	if j.ID == "" || j.Status != "pending" {
		t.Fatalf("not immediate: %+v", j)
	}
	select {
	case <-b.started:
	case <-time.After(time.Second):
		t.Fatal("implementation did not start")
	}
	reconnected := Engine{Dir: e.Dir}
	if _, err := reconnected.Get("bob", j.ID); err == nil {
		t.Fatal("cross-owner read allowed")
	}
	duplicate := fakeBackend()
	same, err := reconnected.Submit("alice", jobSpec(), duplicate)
	if err != nil || same.ID != j.ID {
		t.Fatalf("retry: %+v %v", same, err)
	}
	changed := jobSpec()
	changed.Prompt = "different"
	if _, err := reconnected.Submit("alice", changed, fakeBackend()); err == nil {
		t.Fatal("idempotency conflict accepted")
	}
	another := jobSpec()
	another.RequestID = "request-0002"
	if _, err := reconnected.Submit("alice", another, fakeBackend()); err == nil {
		t.Fatal("concurrency limit bypassed")
	}
	close(b.gate)
	final := awaitJob(t, reconnected, "alice", j.ID, b)
	if final.Status != "succeeded" || len(final.Rounds) != 1 || len(b.runs) != 2 {
		t.Fatalf("workflow: %+v runs %v", final, b.runs)
	}
	r := final.Rounds[0]
	if r.ImplementerRun == "" || r.ReviewerRun == "" || r.Candidate == nil || r.ImplementerTest == nil || r.ReviewerTest == nil || r.Review == nil {
		t.Fatalf("missing durable evidence: %+v", r)
	}
	if r.ImplementerTest.WorkspaceID == "" || r.ImplementerTest.WorkspaceID == r.ReviewerTest.WorkspaceID {
		t.Fatalf("independent test evidence was overwritten: implementation=%+v review=%+v", r.ImplementerTest, r.ReviewerTest)
	}
	if !strings.Contains(b.reviewPrompts[0], "exact candidate patch") || !strings.Contains(b.reviewPrompts[0], "test evidence") {
		t.Fatal("review did not receive exact candidate and tests")
	}
}

func TestReviewJobFailureAndRevisionGates(t *testing.T) {
	for _, tt := range []struct {
		name                          string
		failures                      int
		verdicts                      []string
		ownerErr, errorRun, errorWait error
		status                        string
		rounds, runs                  int
	}{
		{name: "requested revision", verdicts: []string{"changes_requested", "pass"}, status: "succeeded", rounds: 2, runs: 4},
		{name: "failed tests", failures: 1, status: "succeeded", rounds: 2, runs: 3},
		{name: "test exhaustion", failures: 3, status: "failed", rounds: 3, runs: 3},
		{name: "review exhaustion", verdicts: []string{"changes_requested", "changes_requested", "changes_requested"}, status: "failed", rounds: 3, runs: 6},
		{name: "unavailable worker", ownerErr: errors.New("offline"), status: "failed", rounds: 0, runs: 0},
		{name: "wrong worker owner", ownerErr: errors.New("owner mismatch"), status: "failed", rounds: 0, runs: 0},
		{name: "ambiguous submission", errorRun: errors.New("connection lost after send"), status: "failed", rounds: 1, runs: 1},
		{name: "lost wait", errorWait: errors.New("worker unavailable"), status: "failed", rounds: 1, runs: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := Engine{Dir: t.TempDir()}
			b := fakeBackend()
			b.testFailures = tt.failures
			b.verdicts = tt.verdicts
			b.ownerError = tt.ownerErr
			b.runError = tt.errorRun
			b.waitError = tt.errorWait
			j, err := e.Submit("alice", jobSpec(), b)
			if err != nil {
				t.Fatal(err)
			}
			j = awaitJob(t, e, "alice", j.ID, b)
			if j.Status != tt.status || len(j.Rounds) != tt.rounds || len(b.runs) != tt.runs {
				t.Fatalf("got %+v runs %v", j, b.runs)
			}
			if tt.errorWait != nil && b.stops != 1 {
				t.Fatal("lost wait did not attempt exact child cancellation")
			}
		})
	}
}

func TestReviewJobDeadlineStopsExactChild(t *testing.T) {
	e := Engine{Dir: t.TempDir()}
	b := fakeBackend()
	b.gate = make(chan struct{})
	j := Job{ID: "j-" + strings.Repeat("a", 32), Owner: "alice", Spec: jobSpec(), Deadline: time.Now().Add(time.Second)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := e.execute(ctx, &j, b)
	if !errors.Is(err, context.DeadlineExceeded) || b.stops != 1 || len(b.runs) != 1 {
		t.Fatalf("timeout: %v stops %d runs %v", err, b.stops, b.runs)
	}
}

func TestReviewJobRestartInterruptsWithoutReplay(t *testing.T) {
	if dir := os.Getenv("WT_TEST_REVIEW_JOB_CRASH_DIR"); dir != "" {
		e := Engine{Dir: dir}
		b := fakeBackend()
		b.gate = make(chan struct{})
		j, err := e.Submit("alice", jobSpec(), b)
		if err != nil {
			t.Fatal(err)
		}
		<-b.started
		// Exit after the persisted child handle appears, abandoning both file locks.
		for {
			data, err := os.ReadFile(e.path(j.ID, ".json"))
			if err != nil {
				t.Fatal(err)
			}
			var stored Job
			if err := json.Unmarshal(data, &stored); err != nil {
				t.Fatal(err)
			}
			if len(stored.Rounds) == 1 && stored.Rounds[0].ImplementerRun != "" {
				os.Exit(0)
			}
			time.Sleep(time.Millisecond)
		}
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReviewJobRestartInterruptsWithoutReplay$")
	cmd.Env = append(os.Environ(), "WT_TEST_REVIEW_JOB_CRASH_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture: %v %s", err, out)
	}
	files, err := filepath.Glob(filepath.Join(dir, "j-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("records: %v %v", files, err)
	}
	id := strings.TrimSuffix(filepath.Base(files[0]), ".json")
	e := Engine{Dir: dir}
	if _, err := e.Get("bob", id); err == nil {
		t.Fatal("outsider recovered owner job")
	}
	j, err := e.Get("alice", id)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != "interrupted" || len(j.Rounds) != 1 || j.Rounds[0].ImplementerRun == "" {
		t.Fatalf("restart: %+v", j)
	}
	b := fakeBackend()
	retried, err := e.Submit("alice", jobSpec(), b)
	if err != nil || retried.Status != "interrupted" || len(b.runs) != 0 {
		t.Fatalf("replayed: %+v %v", retried, err)
	}
}

func TestReviewJobRejectsUnboundedAndUnsafeSpec(t *testing.T) {
	for _, change := range []func(*Spec){func(s *Spec) { s.MaxRevisions = 3 }, func(s *Spec) { s.RunSeconds = 0 }, func(s *Spec) { s.TimeoutSeconds = 0 }, func(s *Spec) { s.AllowedPaths = []string{"../escape"} }, func(s *Spec) { s.AllowedPaths = []string{"sub/egg.yaml"} }, func(s *Spec) { s.BaseCommit = "main" }, func(s *Spec) { s.Reviewer.WingID = s.Implementer.WingID }} {
		s := jobSpec()
		change(&s)
		if err := s.Validate(); err == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}
