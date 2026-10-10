package egg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

func initialCodexFixture(t *testing.T) (*runTurnRuntime, string, RunTurnRequest, chan struct{}) {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	lifecycleWrite(t, filepath.Join(dir, "egg.meta"), "agent=codex\n")
	read := func(ctx context.Context, after int64, limit int) (SessionView, error) {
		return ReadSessionLifecycle(dir, "codex", "/fixture", home, "", true, after, limit)
	}
	started := make(chan struct{})
	backend := runTurnBackend{Agent: "codex", Read: read, Started: started,
		Send: func(context.Context, string) (PromptDelivery, error) {
			t.Error("initial argv prompt was sent to the PTY")
			return PromptDelivery{}, nil
		}}
	backend.Prepare = func(prompt, id string) (func() (turnEvidence, error), error) {
		return codexRunScanner(home, filepath.Base(dir), id, prompt, read, readRunFile)
	}
	rt := newRunTurnRuntime(dir, backend)
	t.Cleanup(rt.stopActive)
	return rt, home, RunTurnRequest{RunID: "initial", Prompt: "native argv prompt", Deadline: time.Now().Add(time.Hour)}, started
}

func initialCodexHooks(t *testing.T, rt *runTurnRuntime, home, prompt string) {
	t.Helper()
	spool := filepath.Join(home, ".codex", "wingthing-events", filepath.Base(rt.dir))
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, filepath.Join(spool, "seq.00000000000000000001.json"), `{"session_id":"thread-exact","source":"startup","hook_event_name":"SessionStart"}`)
	wire, _ := json.Marshal(map[string]string{"session_id": "thread-exact", "turn_id": "turn-exact", "prompt": prompt, "hook_event_name": "UserPromptSubmit"})
	lifecycleWrite(t, filepath.Join(spool, "seq.00000000000000000002.json"), string(wire))
}

func TestCodexInitialPromptIncludesEventsBeforeSocketReadiness(t *testing.T) {
	rt, home, request, started := initialCodexFixture(t)
	if err := rt.startInitialCodex(request); err != nil {
		t.Fatal(err)
	}
	// The durable reservation and timers exist before any provider bytes. Both
	// hooks and notify may finish before the wing can open the egg socket.
	result, err := ReadRunTurnResult(rt.dir, request.RunID)
	if err != nil || result.Status != "pending" {
		t.Fatalf("prelaunch reservation: %+v %v", result, err)
	}
	initialCodexHooks(t, rt, home, request.Prompt)
	publishCodexNotify(t, home, filepath.Base(rt.dir), "complete", "thread-exact", "turn-exact", request.Prompt, "initial result")
	close(started)
	if _, err := rt.reserve(request); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.submit(request); err != nil {
		t.Fatal(err)
	}
	if _, err = rt.wait(context.Background(), request.RunID); err != nil {
		t.Fatal(err)
	}
	result, err = rt.get(request.RunID, true)
	if err != nil || result.Status != "done" || result.ProviderSessionID != "thread-exact" || result.TurnID != "turn-exact" || result.Text != "initial result" {
		t.Fatalf("initial completion: %+v %v", result, err)
	}
}

func TestCodexInitialReadinessTimeoutSurvivesObserverExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt, _, request, started := initialCodexFixture(t)
		killed := make(chan struct{}, 1)
		rt.backend.Kill = func() ([]RunDescendant, error) { killed <- struct{}{}; return nil, nil }
		rt.backend.StartupDiagnostic = func() string { return codexStartupDiagnostic("Update available private-canary-secret") }
		if err := rt.startInitialCodex(request); err != nil {
			t.Fatal(err)
		}
		close(started)
		ctx, disconnect := context.WithCancel(context.Background())
		disconnect()
		if _, err := rt.wait(ctx, request.RunID); err == nil {
			t.Fatal("cancelled observer did not disconnect")
		}
		result, err := rt.wait(context.Background(), request.RunID)
		if err != nil || result.Status != "failed" || result.FailureKind != agent.ProviderNotReady || !strings.Contains(result.Error, "update prompt") || strings.Contains(result.Error, "private-canary-secret") {
			t.Fatalf("readiness bound: %+v %v", result, err)
		}
		<-killed
		reopened := newRunTurnRuntime(rt.dir, rt.backend)
		if replay, err := reopened.submit(request); err != nil || replay.FailureKind != agent.ProviderNotReady {
			t.Fatalf("durable readiness failure: %+v %v", replay, err)
		}
	})
}

func TestCodexReadinessTimerCannotFailAnAdmittedTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt, home, request, started := initialCodexFixture(t)
		if err := rt.startInitialCodex(request); err != nil {
			t.Fatal(err)
		}
		initialCodexHooks(t, rt, home, request.Prompt)
		close(started)
		synctest.Wait()
		run := rt.runs[request.RunID]
		result, err := rt.get(request.RunID, true)
		if err != nil || result.Status != "running" {
			t.Fatalf("native receipt: %+v %v", result, err)
		}
		rt.expire(run, true)
		if result, err = rt.get(request.RunID, true); err != nil || result.Terminal() {
			t.Fatalf("stale readiness timer failed active turn: %+v %v", result, err)
		}
		rt.expire(run, false)
		if result, err = rt.wait(context.Background(), request.RunID); err != nil || result.FailureKind != agent.Timeout {
			t.Fatalf("execution bound after receipt: %+v %v", result, err)
		}
	})
}

func TestCodexStartupOverridesAreLastAndKeepPromptLiteral(t *testing.T) {
	input := []string{"-c", "check_for_update_on_startup=true", "--", "-literal prompt"}
	want := []string{"-c", "check_for_update_on_startup=true",
		"-c", "check_for_update_on_startup=false",
		"-c", `notice={hide_full_access_warning=true,hide_gpt5_1_migration_prompt=true,"hide_gpt-5.1-codex-max_migration_prompt"=true,hide_rate_limit_model_nudge=true,hide_world_writable_warning=true,external_config_migration_prompts={home=true,projects={"/fixture/a.b"=true}}}`,
		"-c", `projects={"/fixture/a.b"={trust_level="trusted"}}`, "--", "-literal prompt"}
	if got := codexStartupArgs(input, "/fixture/a.b"); !reflect.DeepEqual(got, want) {
		t.Fatalf("startup argv=%q, want %q", got, want)
	}
}

func TestCodexStartupDiagnosticUsesLastScreenAndRedactsDetails(t *testing.T) {
	vt := NewVTerm(100, 24)
	defer vt.Close()
	_, _ = vt.Write([]byte("Update available secret-old-token\r\n"))
	_, _ = vt.Write([]byte("\x1b[2J\x1b[HSign in secret-new-token /private/path\r\n"))
	if got := codexStartupDiagnostic(vt.ScreenText()); got != "Startup screen: authentication required (details redacted)." {
		t.Fatal(got)
	}
}
