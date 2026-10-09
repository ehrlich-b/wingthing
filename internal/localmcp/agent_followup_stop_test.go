package localmcp

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/taskrun"
)

func TestAgentQueuedFollowupsSurviveParentStopAndFinish(t *testing.T) {
	for _, stop := range []bool{true, false} {
		name := "normal-finish"
		if stop {
			name = "parent-stop"
		}
		t.Run(name, func(t *testing.T) {
			s, db := fakeAgentWaitRuns(t)
			cwd := t.TempDir()
			parentStarted := make(chan struct{})
			finishParent := make(chan struct{})
			finishChildren := make(chan struct{})
			childPrompts := make(chan string, 2)
			s.runAgentTask = func(ctx context.Context, _ *config.Config, db *store.Store, task *store.Task, _ taskrun.TaskRunOptions) error {
				if err := db.UpdateTaskStatus(task.ID, "running"); err != nil {
					return err
				}
				if task.ParentID != nil {
					childPrompts <- task.What
					select {
					case <-finishChildren:
					case <-ctx.Done():
						return ctx.Err()
					}
					if err := db.SetTaskOutput(task.ID, "follow-up complete"); err != nil {
						return err
					}
					return db.UpdateTaskStatus(task.ID, "done")
				}
				if err := db.SetTaskOutput(task.ID, "retained parent output"); err != nil {
					return err
				}
				close(parentStarted)
				select {
				case <-finishParent:
					return db.UpdateTaskStatus(task.ID, "done")
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			var runIDs []string
			t.Cleanup(func() {
				for _, id := range runIDs {
					if value, ok := activeMCPAgentRuns.Load(s.agentRunKey(id)); ok {
						active := value.(activeMCPAgentRun)
						active.cancel()
						<-active.done
					}
				}
			})
			created, err := s.toolAgentRun(json.RawMessage(`{"prompt":"original request","agent":"claude","cwd":` + strconv.Quote(cwd) + `}`))
			if err != nil {
				t.Fatal(err)
			}
			parentID := created["run_id"].(string)
			runIDs = append(runIDs, parentID)
			select {
			case <-parentStarted:
			case <-time.After(3 * time.Second):
				t.Fatal("parent provider did not start")
			}
			// Current agent_steer rejects active parents. Exercise its underlying
			// admission path to cover queues persisted by earlier versions.
			for _, direction := range []string{"review auth", "review UI"} {
				child, err := s.submitAgentRun(agentRunArgs{
					Prompt: direction, Agent: "claude", CWD: cwd,
				}, &agentRunFollowup{parentID: parentID, direction: direction})
				if err != nil {
					t.Fatal(err)
				}
				childID := child["run_id"].(string)
				runIDs = append(runIDs, childID)
				status, err := s.toolAgentStatus(json.RawMessage(`{"run_id":` + strconv.Quote(childID) + `}`))
				if err != nil || status["status"] != "pending" || status["finished_at"] != nil {
					t.Fatalf("queued follow-up = %#v, %v", status, err)
				}
				if status["parent_id"] != parentID {
					t.Errorf("queued follow-up lost parent linkage: %#v", status)
				}
			}
			if err := db.CreateTask(&store.Task{
				ID: "foreign-child", Type: "agent_run", Principal: "other", ParentID: &parentID,
				Status: "pending", RunnerPID: os.Getpid(),
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case prompt := <-childPrompts:
				t.Fatalf("follow-up ran before parent finished: %q", prompt)
			default:
			}
			if stop {
				stopped, err := s.toolAgentStop(json.RawMessage(`{"run_id":` + strconv.Quote(parentID) + `}`))
				if err != nil || stopped["status"] != "failed" || stopped["stopped"] != true {
					t.Fatalf("parent stop = %#v, %v", stopped, err)
				}
				followups, ok := stopped["followups"].([]map[string]any)
				if stopped["followup_policy"] != "continue" || !ok || len(followups) != 2 {
					t.Errorf("stop did not disclose preserved owner-scoped follow-ups: %#v", stopped)
				} else {
					seen := make(map[string]bool)
					for _, followup := range followups {
						id := followup["run_id"].(string)
						seen[id] = true
						if followup["status"] != "pending" && followup["status"] != "running" {
							t.Errorf("stop changed follow-up outcome: %#v", followup)
						}
					}
					if !seen[runIDs[1]] || !seen[runIDs[2]] {
						t.Errorf("stop reported different follow-ups: %#v", followups)
					}
				}
			} else {
				close(finishParent)
			}
			for range 2 {
				select {
				case prompt := <-childPrompts:
					if !strings.Contains(prompt, "original request") || !strings.Contains(prompt, "retained parent output") ||
						(!strings.Contains(prompt, "review auth") && !strings.Contains(prompt, "review UI")) {
						t.Errorf("follow-up lost prior context or new direction: %q", prompt)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("parent completion did not release both follow-ups")
				}
			}
			close(finishChildren)
			for _, id := range runIDs[1:] {
				waited, err := s.toolAgentWait(context.Background(), json.RawMessage(`{"run_id":`+strconv.Quote(id)+`,"timeout_seconds":3}`))
				if err != nil || waited["status"] != "done" {
					t.Fatalf("follow-up did not finish: %#v, %v", waited, err)
				}
				for _, tool := range []string{"status", "result"} {
					var data map[string]any
					args := json.RawMessage(`{"run_id":` + strconv.Quote(id) + `}`)
					if tool == "status" {
						data, err = s.toolAgentStatus(args)
					} else {
						data, err = s.toolAgentResult(args)
					}
					if err != nil || data["status"] != "done" || data["parent_id"] != parentID || data["error"] != nil {
						t.Errorf("follow-up %s lost its outcome/linkage: %#v, %v", tool, data, err)
					}
				}
			}
			foreign, err := db.GetTask("foreign-child")
			if err != nil || foreign.Status != "pending" || foreign.FinishedAt != nil {
				t.Fatalf("foreign follow-up was changed: %#v, %v", foreign, err)
			}
			parent, err := db.GetTask(parentID)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := s.toolAgentStop(json.RawMessage(`{"run_id":` + strconv.Quote(parentID) + `}`))
			if err != nil || repeated["status"] != parent.Status || repeated["stopped"] != nil ||
				repeated["followup_policy"] != "continue" || len(repeated["followups"].([]map[string]any)) != 2 {
				t.Fatalf("terminal parent stop lost follow-up outcomes: %#v, %v", repeated, err)
			}
			for _, followup := range repeated["followups"].([]map[string]any) {
				if followup["status"] != "done" {
					t.Errorf("terminal parent stop changed completed follow-up: %#v", followup)
				}
			}
			after, err := db.GetTask(parentID)
			if err != nil || after.Status != parent.Status || !after.FinishedAt.Equal(*parent.FinishedAt) ||
				after.Output == nil || *after.Output != "retained parent output" {
				t.Fatalf("terminal parent stop changed its result: %#v, %v", after, err)
			}
		})
	}
}

func TestAgentFailedFollowupExposesOwnReason(t *testing.T) {
	parentID := "parent"
	s, db := fakeAgentWaitRuns(t,
		&store.Task{ID: parentID, Type: "agent_run", Principal: "owner", Status: "failed"},
		&store.Task{ID: "follow-up", Type: "agent_run", Principal: "owner", ParentID: &parentID},
	)
	if err := db.SetTaskError(parentID, "stopped by MCP principal owner"); err != nil {
		t.Fatal(err)
	}
	const reason = "provider_refused: claude exited with status 1"
	if err := db.SetTaskFailure("follow-up", reason, "provider_refused"); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"status", "result"} {
		var data map[string]any
		var err error
		args := json.RawMessage(`{"run_id":"follow-up"}`)
		if tool == "status" {
			data, err = s.toolAgentStatus(args)
		} else {
			data, err = s.toolAgentResult(args)
		}
		if err != nil || data["status"] != "failed" || data["parent_id"] != parentID ||
			data["error"] != reason || data["error_kind"] != "provider_refused" {
			t.Fatalf("failed follow-up %s hid or borrowed its reason: %#v, %v", tool, data, err)
		}
	}
}

func TestAgentStatusBoundsErrorWithoutSplittingUnicode(t *testing.T) {
	s, db := fakeAgentWaitRuns(t, &store.Task{ID: "run", Type: "agent_run", Principal: "owner"})
	reason := strings.Repeat("✓", 2001)
	if err := db.SetTaskError("run", reason); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"run_id":"run"}`)
	status, err := s.toolAgentStatus(args)
	if err != nil || status["error"] != strings.Repeat("✓", 2000) || status["error_truncated"] != true {
		t.Fatalf("status error is unbounded or splits Unicode: %#v, %v", status, err)
	}
	result, err := s.toolAgentResult(args)
	if err != nil || result["error"] != reason || result["error_truncated"] != nil {
		t.Fatalf("result lost the full error: %#v, %v", result, err)
	}
}

func TestAgentStopReportsLegacyDefaultOwnerFollowups(t *testing.T) {
	parentID := "parent"
	s, _ := fakeAgentWaitRuns(t,
		&store.Task{ID: parentID, Type: "agent_run", Status: "done"},
		&store.Task{ID: "legacy-child", Type: "agent_run", ParentID: &parentID, Status: "pending"},
		&store.Task{ID: "default-child", Type: "agent_run", Principal: "default", ParentID: &parentID, Status: "done"},
		&store.Task{ID: "foreign-child", Type: "agent_run", Principal: "other", ParentID: &parentID, Status: "pending"},
		&store.Task{ID: "ordinary-task", Type: "prompt", ParentID: &parentID, Status: "pending"},
	)
	s.Principal = ""
	stopped, err := s.toolAgentStop(json.RawMessage(`{"run_id":"parent"}`))
	if err != nil || stopped["status"] != "done" || stopped["followup_policy"] != "continue" {
		t.Fatalf("terminal default-owner stop = %#v, %v", stopped, err)
	}
	followups, ok := stopped["followups"].([]map[string]any)
	if !ok || len(followups) != 2 || followups[0]["run_id"] != "default-child" ||
		followups[0]["status"] != "done" || followups[1]["run_id"] != "legacy-child" || followups[1]["status"] != "pending" {
		t.Fatalf("stop did not preserve default-owner visibility: %#v", stopped)
	}
}

func TestAgentStopWithoutFollowupsKeepsExistingContract(t *testing.T) {
	s, _ := fakeAgentWaitRuns(t)
	started := make(chan struct{})
	s.runAgentTask = func(ctx context.Context, _ *config.Config, db *store.Store, task *store.Task, _ taskrun.TaskRunOptions) error {
		if err := db.UpdateTaskStatus(task.ID, "running"); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	created, err := s.toolAgentRun(json.RawMessage(`{"prompt":"work","agent":"claude","cwd":` + strconv.Quote(t.TempDir()) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	id := created["run_id"].(string)
	t.Cleanup(func() {
		if value, ok := activeMCPAgentRuns.Load(s.agentRunKey(id)); ok {
			active := value.(activeMCPAgentRun)
			active.cancel()
			<-active.done
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}
	args := json.RawMessage(`{"run_id":` + strconv.Quote(id) + `}`)
	stopped, err := s.toolAgentStop(args)
	if err != nil || stopped["status"] != "failed" || stopped["stopped"] != true ||
		stopped["followups"] != nil || stopped["followup_policy"] != nil {
		t.Fatalf("stop without follow-ups changed: %#v, %v", stopped, err)
	}
	for _, tool := range []string{"status", "result"} {
		var data map[string]any
		if tool == "status" {
			data, err = s.toolAgentStatus(args)
		} else {
			data, err = s.toolAgentResult(args)
		}
		if err != nil || data["status"] != "failed" || data["error"] != "stopped by MCP principal owner" {
			t.Errorf("%s hides stop reason: %#v, %v", tool, data, err)
		}
	}
}
