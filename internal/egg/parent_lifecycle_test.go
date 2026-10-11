package egg

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

func TestParentLifecycleExitedCodexAndDelayedResult(t *testing.T) {
	for _, flush := range []string{"before_exit_scan", "after_terminal_failure"} {
		t.Run(flush, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rt, home, request, _ := codexRunFixture(t)
				request.Deadline = time.Time{}
				exited := make(chan struct{})
				rt.backend.Done = exited
				permit := make(chan struct{})
				prepare := rt.backend.Prepare
				rt.backend.Prepare = func(prompt, id string) (func() (turnEvidence, error), error) {
					scan, err := prepare(prompt, id)
					return func() (turnEvidence, error) { <-permit; return scan() }, err
				}
				if err := rt.startInitialCodex(request); err != nil {
					t.Fatal(err)
				}
				session := filepath.Base(rt.dir)
				lifecycleWrite(t, filepath.Join(home, ".codex", "wingthing-events", session, "seq.00000000000000000002.json"), `{"session_id":"thread-exact","turn_id":"turn-exact","hook_event_name":"UserPromptSubmit","prompt":"inspect native fixture"}`)
				synctest.Wait() // first native scan is held, after durable admission
				close(exited)
				wantStatus, wantKind := "done", agent.ErrorKind("")
				if flush == "before_exit_scan" {
					publishCodexNotify(t, home, session, "delayed", "thread-exact", "turn-exact", request.Prompt, "flushed after process exit Ω")
					permit <- struct{}{}
				} else {
					wantStatus, wantKind = "failed", agent.ProviderExit
					permit <- struct{}{} // receipt, without a completed result
					synctest.Wait()      // process exit observed; final scan held
					permit <- struct{}{}
				}
				if _, err := rt.wait(t.Context(), request.RunID); err != nil {
					t.Fatal(err)
				}
				rt.awaitProcessExit()
				first, err := ReadRunTurnResult(rt.dir, request.RunID)
				if err != nil || first.Status != wantStatus || first.FailureKind != wantKind || first.ProviderSessionID != "thread-exact" || first.TurnID != "turn-exact" {
					t.Fatalf("exit/flush outcome: %+v %v", first, err)
				}
				before, err := os.ReadFile(runTurnPath(rt.dir, request.RunID))
				if err != nil {
					t.Fatal(err)
				}
				if flush == "after_terminal_failure" {
					publishCodexNotify(t, home, session, "too-late", "thread-exact", "turn-exact", request.Prompt, "late result must not overwrite failure")
				} else if first.Text != "flushed after process exit Ω" {
					t.Fatal("exit lost the durable native result")
				}
				// A fresh runtime must reuse this terminal artifact without
				// preparing, launching or resubmitting provider work.
				backend := rt.backend
				backend.Prepare = func(string, string) (func() (turnEvidence, error), error) {
					t.Error("terminal inspection prepared a new provider turn")
					return nil, os.ErrInvalid
				}
				recovered := newRunTurnRuntime(rt.dir, backend)
				for range 2 {
					again, err := recovered.submit(request)
					if err != nil || !reflect.DeepEqual(first, again) {
						t.Fatalf("terminal retry changed outcome: %+v %v", again, err)
					}
				}
				after, err := os.ReadFile(runTurnPath(rt.dir, request.RunID))
				if err != nil || string(before) != string(after) {
					t.Fatalf("late flush/inspection rewrote terminal state: %v", err)
				}
				t.Logf("run=%s session=%s provider=thread-exact turn=turn-exact flush=%s terminal=%s retries=2 resubmissions=0", request.RunID, session, flush, first.Status)
			})
		})
	}
}

func TestParentLifecycleStopJoinsDisposableChild(t *testing.T) {
	fixture := newRunFixture(t)
	// Only shell builtins execute, so this known child has no descendants.
	// Its stdin command and Wait receipt distinguish Stop from worker exit
	// without pretending that a denied host process inventory succeeded.
	child := exec.Command("/bin/sh", "-c", `printf 'ready\n'; IFS= read -r command; test "$command" = stop`)
	child.Dir = fixture.home
	child.Env = []string{"HOME=" + fixture.home, "PATH=/usr/bin:/bin", "TMPDIR=" + os.Getenv("TMPDIR")}
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() { waitErr = child.Wait(); close(exited) }()
	t.Cleanup(func() { _ = input.Close(); _ = child.Process.Kill(); <-exited })
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("disposable child readiness: %q %v", line, err)
	}
	fixture.runtime.backend.Done = exited
	var stops atomic.Int32
	fixture.runtime.backend.Kill = func() ([]RunDescendant, error) {
		stops.Add(1)
		if _, err := io.WriteString(input, "stop\n"); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		select {
		case <-exited:
			return nil, waitErr
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if _, err := fixture.runtime.submit(fixture.request); err != nil {
		t.Fatal(err)
	}
	<-fixture.sent
	first, err := fixture.runtime.stop(fixture.request.RunID)
	if err != nil || first.Status != "stopped" || first.ContainmentError != "" || len(first.SurvivingDescendants) != 0 {
		t.Fatalf("Stop did not join the disposable child: %+v %v", first, err)
	}
	fixture.runtime.awaitProcessExit()
	if waitErr != nil || child.ProcessState == nil || !child.ProcessState.Success() {
		t.Fatalf("worker exit unaccounted: %v %v", child.ProcessState, waitErr)
	}
	second, err := fixture.runtime.stop(fixture.request.RunID)
	if err != nil || !reflect.DeepEqual(first, second) || stops.Load() != 1 {
		t.Fatalf("Stop repeated its effect: %+v %v calls=%d", second, err, stops.Load())
	}
	durable, err := ReadRunTurnResult(fixture.runtime.dir, fixture.request.RunID)
	if err != nil || !reflect.DeepEqual(first, durable) {
		t.Fatalf("Stop lost its terminal receipt: %+v %v", durable, err)
	}
	t.Logf("run=%s child_pid=%d stop_requests=1 joined_children=1 descendants=0 terminal=%s", first.RunID, child.Process.Pid, durable.Status)
}
