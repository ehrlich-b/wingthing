//go:build runinspectfixture

package runinspect_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/runinspect"
	"github.com/ehrlich-b/wingthing/internal/runinspect/fixture"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

// Schedule bounded supplier changes serially at the existing cancellation
// boundary. Stack filtering confines this to inventory walks, with no sleeps,
// timing race, extra worker or production hook.
type inventorySwapContext struct {
	context.Context
	nodes, calls  int
	swap, restore func()
}

func (c *inventorySwapContext) Err() error {
	var pcs [16]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if strings.Contains(frame.Function, ".(*snapshot).verify.func1") {
			c.calls++
			if c.calls == c.nodes {
				c.swap()
			}
			if c.calls == c.nodes+1 {
				c.restore()
			}
			break
		}
		if !more {
			break
		}
	}
	return c.Context.Err()
}

func TestRunInspectRoundTripR3ConsumedBytesMatchInventory(t *testing.T) {
	for _, kind := range []string{"receipt", "database"} {
		t.Run(kind, func(t *testing.T) {
			b := newRoundTrip(t)
			alternate := newRoundTrip(t)
			relative := "wt.db"
			if kind == "receipt" {
				b.Egg("run", "execution", "pending", "")
				alternate.Egg("run", "execution", "done", "unmanifested completion")
				relative = fixture.EggPath("execution", "run")
			} else {
				b.Wing("run", "execution", "", "admitted", "pending", "")
				alternate.Wing("run", "execution", "", "terminal", "done", "unmanifested completion")
			}
			b.Write("z-trigger", []byte("bounded replacement synchronization\n"))
			b.Seal()
			alternate.Seal()
			path := filepath.Join(b.Path, relative)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			replacementPath := filepath.Join(alternate.Path, relative)
			replacement, err := os.ReadFile(replacementPath)
			if err != nil || bytes.Equal(original, replacement) {
				t.Fatalf("replacement must differ from inventory: %v", err)
			}
			if kind == "receipt" {
				result, err := egg.ReadRunTurnResult(filepath.Dir(path), "run")
				if err != nil || result.Status != "pending" {
					t.Fatalf("writer oracle: %+v %v", result, err)
				}
			}
			nodes := 0
			if err := filepath.WalkDir(b.Path, func(_ string, _ os.DirEntry, err error) error { nodes++; return err }); err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(b.Workspace, "original-artifact")
			swapped := false
			ctx := &inventorySwapContext{Context: t.Context(), nodes: nodes}
			ctx.swap = func() {
				if err := os.Rename(path, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacementPath, path); err != nil {
					t.Fatal(err)
				}
				swapped = true
			}
			ctx.restore = func() {
				if !swapped {
					return
				}
				if err := os.Rename(path, replacementPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(backup, path); err != nil {
					t.Fatal(err)
				}
				swapped = false
			}
			t.Cleanup(ctx.restore)
			report, inspectErr := runinspect.Inspect(ctx, b.Path, runinspect.Options{WorkspaceRoot: b.Workspace})
			if ctx.calls < nodes {
				t.Fatal("replacement schedule did not reach the first inventory's final node")
			}
			ctx.restore()
			final, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(original, final) {
				t.Fatalf("original evidence was not restored: %v", err)
			}
			if inspectErr == nil || report != nil || !strings.Contains(inspectErr.Error(), "do not match") {
				t.Fatalf("uninventoried %s accepted: error=%v report=%+v", kind, inspectErr, report)
			}
			valid := inspectRoundTrip(t, b, 0)
			if run := roundTripRun(t, valid, "run"); run.Diagnosis != "admitted_outcome_unknown" {
				t.Fatalf("original pending evidence was lost: %+v", run)
			}
		})
	}
}

func TestRunInspectRoundTripR3TrailingSpaceCWD(t *testing.T) {
	for _, name := range []string{"ordinary", "trailing-space "} {
		t.Run(strings.TrimSpace(name), func(t *testing.T) {
			b := newRoundTrip(t)
			cwd := filepath.Join(b.Workspace, name)
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			svc := &wingsession.Service{Home: b.Workspace, Policy: func() wingsession.Policy {
				return wingsession.Policy{Wing: &config.WingConfig{Paths: config.PathList{{Path: cwd}}}, Egg: egg.DefaultEggConfig()}
			}}
			launch, err := svc.PrepareLaunch(wingsession.Authority{Principal: "owner", UserID: "owner", Role: "owner"}, cwd)
			if err != nil || launch.CWD != cwd {
				t.Fatalf("launch oracle changed CWD: %+v %v", launch, err)
			}
			result := fixture.Result("run", "execution", "timeout", "partial")
			run := wingsession.Run{ID: "run", SessionID: "execution", SpecHash: "wing-spec-run", Agent: "claude", CWD: cwd, CreatedAt: fixture.CapturedAt, Phase: "terminal", Launch: wingsession.RunLaunch{Authority: wingsession.Authority{Principal: "owner", UserID: "owner"}}, Result: result}
			wire, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			task := &store.Task{ID: run.ID, Type: "agent_run", What: "fixture", CWD: cwd, Agent: "claude", Principal: "owner", RunAt: fixture.CapturedAt, CreatedAt: fixture.CapturedAt, Status: "timeout", Output: &result.Text, FinishedAt: &result.EndedAt}
			row, fresh, err := b.Store.AdmitAgentRun(&store.AgentRun{ID: run.ID, SessionID: run.SessionID, Principal: "owner", SpecHash: run.SpecHash, Record: wire}, task)
			if err != nil || !fresh {
				t.Fatalf("admission: %v", err)
			}
			if err := b.Store.SaveAgentRun(row, task, "timeout", ""); err != nil {
				t.Fatal(err)
			}
			b.Egg("run", "execution", "timeout", "partial")
			c, fresh, err := b.Store.ReserveConversation(store.Conversation{ID: "logical", OwnerID: "owner", SessionID: "execution", LaunchKey: "launch-logical", SpecDigest: "spec", Agent: "claude", CWD: cwd})
			if err != nil || !fresh {
				t.Fatalf("conversation admission: %v", err)
			}
			if err := b.Store.SetConversationLaunch(c.ID, "started", ""); err != nil {
				t.Fatal(err)
			}
			seq := b.Event(c, 1)
			b.Checkpoint(c.ID, 0, seq)
			home := filepath.Join(b.Workspace, "provider-home")
			project := filepath.Join(home, ".claude", "projects", strings.ReplaceAll(cwd, "/", "-"))
			if err := os.MkdirAll(project, 0700); err != nil {
				t.Fatal(err)
			}
			native := []byte("{\"type\":\"assistant\",\"sessionId\":\"native-execution\",\"message\":{\"content\":\"fixture\"}}\n")
			if err := os.WriteFile(filepath.Join(project, "native-execution.jsonl"), native, 0600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(b.Path, "eggs", "execution")
			b.Write("eggs/execution/egg.meta", []byte("agent=claude\nprovider_session_id=native-execution\ncwd="+cwd+"\n"))
			if err := egg.CaptureSessionHistory("claude", cwd, dir, home, fixture.CapturedAt.Add(-time.Hour), "native-execution"); err != nil {
				t.Fatal(err)
			}
			b.Seal()
			copyPath, restoreHome := filepath.Join(b.Workspace, "restore-copy"), filepath.Join(b.Workspace, "restore-home")
			if err := os.CopyFS(copyPath, os.DirFS(dir)); err != nil {
				t.Fatal(err)
			}
			id, err := egg.RestoreSessionHistory("claude", cwd, copyPath, restoreHome)
			if err != nil || id != "native-execution" {
				t.Fatalf("restore oracle: %q %v", id, err)
			}
			restored, err := os.ReadFile(filepath.Join(restoreHome, ".claude", "projects", strings.ReplaceAll(cwd, "/", "-"), "native-execution.jsonl"))
			if err != nil || !bytes.Equal(restored, native) {
				t.Fatalf("restore changed exact native bytes: %v", err)
			}
			report, _ := inspectReviewRoundTrip(t, b)
			if report.Archives[0].State != "complete_bound_archive" || !roundTripAction(roundTripRun(t, report, "run"), "manual_resume_eligible") {
				t.Fatalf("valid CWD falsely conflicts: %q %+v", cwd, report.Archives)
			}
		})
	}
}
