//go:build runinspectfixture

package runinspect_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

// Check all three public report forms and inventory the entire disposable
// workspace, including the supplied snapshot and the restore oracle's copy.
func inspectReviewRoundTrip(t *testing.T, b *fixture.Builder) (*runinspect.Report, []byte) {
	t.Helper()
	binary := os.Getenv("WT_TEST_BINARY")
	if binary == "" {
		t.Fatal("WT_TEST_BINARY required; run make test-run-inspect-roundtrip")
	}
	before := fixture.Inventory(t, b.Workspace)
	report := inspectRoundTrip(t, b, 0)
	var human []byte
	for _, format := range []string{"json", "text"} {
		args := []string{"run-inspect", "--snapshot", b.Path}
		if format == "json" {
			args = append(args, "--json")
		}
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = b.Workspace
		cmd.Env = append(os.Environ(), "WINGTHING_DIR="+filepath.Join(b.Workspace, "ambient-do-not-open"), "WINGTHING_PREVIEW_DIR="+filepath.Join(b.Workspace, "ambient-do-not-open"))
		wire, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI %s: %v %s", format, err, wire)
		}
		if format == "json" {
			var decoded runinspect.Report
			if err := json.Unmarshal(wire, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(report, &decoded) {
				t.Fatal("typed and CLI JSON reports differ")
			}
		} else {
			var rendered bytes.Buffer
			if err := runinspect.RenderText(&rendered, report); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(wire, rendered.Bytes()) {
				t.Fatal("typed and CLI human reports differ")
			}
			human = wire
		}
	}
	if !reflect.DeepEqual(before, fixture.Inventory(t, b.Workspace)) {
		t.Fatal("inspection changed supplied bytes, modes, mtimes or directory entries")
	}
	return report, human
}

func TestRunInspectRoundTripCaseSensitiveToolEnvPreservesCompletion(t *testing.T) {
	b := newRoundTrip(t)
	toolsDir := filepath.Join(b.Workspace, "tools")
	if err := os.Mkdir(toolsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "example.yaml"), []byte("name: example\nrun: 'true'\nenv:\n  FOO: upper\n  foo: lower\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tools, err := config.LoadToolsDir(toolsDir)
	if err != nil || len(tools) != 1 || len(tools[0].Env) != 2 {
		t.Fatalf("config oracle lost distinct environment names: %+v %v", tools, err)
	}
	// Preserve the ordinary admission path: immutable tool launch provenance is
	// admitted first, then a successful terminal transition is saved by the writer.
	run := wingsession.Run{ID: "run", SessionID: "execution", SpecHash: "wing-spec-run", Prompt: fixture.PrivatePrompt, OriginalPrompt: fixture.PrivatePrompt, Agent: "claude", CWD: "/fixture", CreatedAt: fixture.CapturedAt.Add(-time.Minute), Phase: "admitted", TimeoutSeconds: 120, Launch: wingsession.RunLaunch{Authority: wingsession.Authority{Principal: "owner", UserID: "owner"}, Options: wingsession.StartOptions{Tools: tools}}, Result: fixture.Result("run", "execution", "pending", "")}
	wire, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	task := &store.Task{ID: run.ID, Type: "agent_run", What: run.Prompt, Agent: run.Agent, CWD: run.CWD, Principal: "owner", RunAt: run.CreatedAt, CreatedAt: run.CreatedAt, Status: "pending", TimeoutSeconds: 120}
	row, fresh, err := b.Store.AdmitAgentRun(&store.AgentRun{ID: run.ID, SessionID: run.SessionID, Principal: "owner", SpecHash: run.SpecHash, Record: wire}, task)
	if err != nil || !fresh {
		t.Fatalf("admission: %v fresh=%v", err, fresh)
	}
	run.Phase = "terminal"
	run.Result = fixture.Result("run", "execution", "done", "known complete output")
	row.Record, err = json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	task.Status, task.Output = "done", &run.Result.Text
	task.StartedAt, task.FinishedAt = &run.Result.StartedAt, &run.Result.EndedAt
	if err := b.Store.SaveAgentRun(row, task, "done", ""); err != nil {
		t.Fatal(err)
	}
	var runtime wingsession.Run
	if err := json.Unmarshal(row.Record, &runtime); err != nil {
		t.Fatal(err)
	}
	if runtime.Result.Status != "done" || runtime.Result.Text != "known complete output" || !reflect.DeepEqual(runtime.Launch.Options.Tools[0].Env, map[string]string{"FOO": "upper", "foo": "lower"}) {
		t.Fatal("full runtime decoder lost completion or case-sensitive map entries")
	}
	b.Seal()
	report, human := inspectReviewRoundTrip(t, b)
	observed := roundTripRun(t, report, "run")
	if observed.Diagnosis != "completed_with_output" || observed.EvidenceState != "consistent" || len(observed.Receipts) != 1 || len(observed.Output) != 1 || !observed.Output[0].Available || observed.Output[0].Bytes != int64(len(run.Result.Text)) || !bytes.Contains(human, []byte("completed_with_output")) {
		t.Fatalf("valid tool environment discards completion: %+v", observed)
	}
}

func TestRunInspectRoundTripCaseSensitiveNativeToolInputPreservesArchive(t *testing.T) {
	b, _, _ := interruptedRoundTrip(t)
	home := filepath.Join(b.Workspace, "provider-"+filepath.Base(b.Path)+"-execution")
	relative := filepath.Join(".claude", "projects", "-fixture", "native-execution.jsonl")
	record := map[string]any{"type": "assistant", "sessionId": "native-execution", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "tool-1", "name": "example", "input": map[string]any{"FOO": "upper", "foo": "lower"}}}}}
	wire, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	wire = append(wire, '\n')
	if err := os.WriteFile(filepath.Join(home, relative), wire, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(b.Path, "eggs", "execution")
	if err := egg.CaptureSessionHistory("claude", "/fixture", dir, home, time.Time{}, "native-execution"); err != nil {
		t.Fatal(err)
	}
	b.Seal()
	copyPath, restoreHome := filepath.Join(b.Workspace, "restore-probe"), filepath.Join(b.Workspace, "restore-home")
	if err := os.CopyFS(copyPath, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	id, err := egg.RestoreSessionHistory("claude", "/fixture", copyPath, restoreHome)
	if err != nil || id != "native-execution" {
		t.Fatalf("restore rejected valid native tool input: %s %v", id, err)
	}
	restored, err := os.ReadFile(filepath.Join(restoreHome, relative))
	if err != nil || !bytes.Equal(restored, wire) {
		t.Fatalf("restore did not preserve exact native tool input: %v", err)
	}
	report, human := inspectReviewRoundTrip(t, b)
	if report.Archives[0].State != "complete_bound_archive" || !roundTripAction(roundTripRun(t, report, "run"), "manual_resume_eligible") || !bytes.Contains(human, []byte("suggested_action=manual_resume_eligible")) {
		t.Fatalf("valid native object keys discard bound archive: %+v", report)
	}
}

func TestRunInspectRoundTripInvalidLifecycleFieldCannotBindArchive(t *testing.T) {
	b, _, _ := interruptedRoundTrip(t)
	b.Write("eggs/execution/egg.meta", []byte("agent=claude\nprovider_session_id=launch-before-clear\ncwd=/fixture\n"))
	event := egg.SessionEvent{Sequence: 1, Source: "claude_hook", Type: "session_ready", ProviderSessionID: "native-execution", State: "ready"}
	wire, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt one real typed field, leaving the binding fields intact.
	wire = bytes.Replace(wire, []byte(`"state":"ready"`), []byte(`"state":42`), 1)
	b.Write("eggs/execution/lifecycle.jsonl", append(wire, '\n'))
	b.Seal()
	copyPath, restoreHome := filepath.Join(b.Workspace, "restore-probe"), filepath.Join(b.Workspace, "restore-home")
	if err := os.CopyFS(copyPath, os.DirFS(filepath.Join(b.Path, "eggs", "execution"))); err != nil {
		t.Fatal(err)
	}
	_, err = egg.RestoreSessionHistory("claude", "/fixture", copyPath, restoreHome)
	if err == nil || !strings.Contains(err.Error(), "invalid lifecycle journal") {
		t.Fatalf("restore oracle did not reject the invalid typed field: %v", err)
	}
	if _, err := os.Stat(restoreHome); !os.IsNotExist(err) {
		t.Fatalf("restore published provider state after invalid journal: %v", err)
	}
	report, human := inspectReviewRoundTrip(t, b)
	if report.Archives[0].State != "invalid_lifecycle_binding" || roundTripAction(roundTripRun(t, report, "run"), "manual_resume_eligible") || bytes.Contains(human, []byte("suggested_action=manual_resume_eligible")) {
		t.Fatalf("journal refused by restore establishes binding: %+v", report)
	}
}
