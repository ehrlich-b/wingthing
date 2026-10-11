package egg

import (
	"os"
	"path/filepath"
	"reflect"
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
