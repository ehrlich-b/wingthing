package eggclient

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestProviderResumeReservationExcludesPendingLaunchAcrossProcesses(t *testing.T) {
	const stateEnv = "WT_TEST_PROVIDER_RESUME_STATE"
	if state := os.Getenv(stateEnv); state != "" {
		cfg := &config.Config{Dir: state}
		registry := providerResumeRegistry{Active: make(map[string]string)}
		release, err := registry.reserveWithAlive(cfg, state, "claude", "provider-id", "source", "child", func(string) bool { return false })
		if err == nil {
			release(false)
			t.Fatal("second process reserved a conversation with a pending launch")
		}
		if !strings.Contains(err.Error(), "already being resumed") {
			t.Fatalf("unexpected reservation error: %v", err)
		}
		return
	}
	cfg := &config.Config{Dir: t.TempDir()}
	registry := providerResumeRegistry{Active: make(map[string]string)}
	release, err := registry.reserveWithAlive(cfg, cfg.Dir, "claude", "provider-id", "source", "parent", func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer release(false)
	child := exec.Command(os.Args[0], "-test.run=^TestProviderResumeReservationExcludesPendingLaunchAcrossProcesses$")
	child.Env = append(os.Environ(), stateEnv+"="+cfg.Dir)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("cross-process reservation failed: %v\n%s", err, output)
	}
	release(false)
	otherRegistry := providerResumeRegistry{Active: make(map[string]string)}
	releaseAgain, err := otherRegistry.reserveWithAlive(cfg, cfg.Dir, "claude", "provider-id", "source", "after-release", func(string) bool { return false })
	if err != nil {
		t.Fatalf("reservation remained locked after release: %v", err)
	}
	releaseAgain(false)
}

func TestProviderResumeReservationSurvivesReclaimUntilProviderExit(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	home := filepath.Join(cfg.Dir, "user-homes", "alice")
	key := ProviderResumeKey(home, "claude", "provider-id")
	runningDir := filepath.Join(cfg.Dir, "eggs", "reclaimed-session")
	if err := os.MkdirAll(runningDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runningDir, ProviderResumeMetadataFile), providerResumeMetadata(key, "old-session"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := providerResumeRegistry{Active: make(map[string]string)}
	alive := true
	isAlive := func(dir string) bool { return dir == runningDir && alive }
	if _, err := registry.reserveWithAlive(cfg, home, "claude", "provider-id", "old-session", "new-session", isAlive); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("reclaimed running provider reservation error = %v", err)
	}
	alive = false
	release, err := registry.reserveWithAlive(cfg, home, "claude", "provider-id", "old-session", "after-exit", isAlive)
	if err != nil {
		t.Fatalf("provider reservation did not release after exit: %v", err)
	}
	release(false)
	if _, err := os.Stat(filepath.Join(cfg.Dir, "eggs", "after-exit", ProviderResumeMetadataFile)); !os.IsNotExist(err) {
		t.Fatalf("failed resume reservation was not released: %v", err)
	}
}

func TestEffectiveProviderSessionPinsFreshClaudeAndPreservesExplicitFlags(t *testing.T) {
	providerID, args, generatedResume, err := EffectiveProviderSession("claude", "", []string{"--model", "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if !ValidProviderSessionID(providerID) || generatedResume != "" || len(args) != 4 || args[0] != "--session-id" || args[1] != providerID {
		t.Fatalf("fresh provider launch = id %q args %#v resume %q", providerID, args, generatedResume)
	}

	explicit := []string{"--model", "opus", "--session-id=caller-id"}
	providerID, args, generatedResume, err = EffectiveProviderSession("claude", "", explicit)
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "caller-id" || generatedResume != "" || strings.Join(args, "\x00") != strings.Join(explicit, "\x00") {
		t.Fatalf("explicit session ID changed: id %q args %#v resume %q", providerID, args, generatedResume)
	}

	providerID, args, generatedResume, err = EffectiveProviderSession("claude", "restored-id", []string{"--resume", "caller-resume", "--model", "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "caller-resume" || generatedResume != "" || strings.Join(args, "\x00") != "--resume\x00caller-resume\x00--model\x00opus" {
		t.Fatalf("explicit resume was duplicated or changed: id %q args %#v resume %q", providerID, args, generatedResume)
	}
	for name, test := range map[string]struct {
		args            []string
		wantProviderID  string
		wantGeneratedID string
	}{
		"continue long":       {args: []string{"--continue", "--model", "opus"}},
		"continue short":      {args: []string{"-c"}},
		"resume picker long":  {args: []string{"--resume", "--model", "opus"}},
		"resume picker short": {args: []string{"-r"}},
		"resume short exact":  {args: []string{"-r", "short-id"}, wantProviderID: "short-id"},
		"resume short equals": {args: []string{"-r=short-id"}},
		"resume short joined": {args: []string{"-rshort-id"}},
		"fork fresh":          {args: []string{"--fork-session"}},
		"fork resumed":        {args: []string{"--resume", "source-id", "--fork-session"}},
	} {
		t.Run(name, func(t *testing.T) {
			gotProviderID, gotArgs, gotGeneratedID, err := EffectiveProviderSession("claude", "", test.args)
			if err != nil {
				t.Fatal(err)
			}
			if gotProviderID != test.wantProviderID || gotGeneratedID != test.wantGeneratedID || strings.Join(gotArgs, "\x00") != strings.Join(test.args, "\x00") {
				t.Fatalf("native argv changed: provider %q args %#v generated %q", gotProviderID, gotArgs, gotGeneratedID)
			}
		})
	}

	providerID, args, generatedResume, err = EffectiveProviderSession("claude", "restored-id", []string{"--fork-session"})
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "" || generatedResume != "restored-id" || strings.Join(args, "\x00") != "--fork-session" {
		t.Fatalf("generated resume fork changed: provider %q args %#v generated %q", providerID, args, generatedResume)
	}
	if _, _, _, err := EffectiveProviderSession("claude", "", []string{"--session-id", "--model", "opus"}); err == nil {
		t.Fatal("bare session ID flag was accepted")
	}
}

func TestEffectiveProviderSessionPreservesEmptyDisablingFlags(t *testing.T) {
	want := []string{"--model", "claude-opus-5-5", "--tools", "", "--setting-sources", ""}
	providerID, args, resumeID, err := EffectiveProviderSession("claude", "", want)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidProviderSessionID(providerID) || resumeID != "" || len(args) != len(want)+2 {
		t.Fatalf("fresh provider identity = %q, argv = %#v, resume = %q", providerID, args, resumeID)
	}
	for i, value := range want {
		if args[i+2] != value {
			t.Fatalf("provider argv[%d] = %q, want %q", i, args[i+2], value)
		}
	}
}

func TestEffectiveProviderSessionStopsAtNativeTerminator(t *testing.T) {
	for _, tc := range []struct {
		name, resume, wantProvider string
		args                       []string
	}{
		{name: "wt egg claude -- -- --session-id", args: []string{"--", "--session-id"}},
		{name: "literal native flags", args: []string{"--model", "opus", "--", "--", "--session-id", "--continue", "--fork-session", "--resume=literal"}},
		{name: "explicit ID before prompt", args: []string{"--session-id", "caller-id", "--", "--session-id=prompt-id"}, wantProvider: "caller-id"},
		{name: "generated resume before prompt", resume: "restored-id", args: []string{"--", "--resume", "prompt-id"}, wantProvider: "restored-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := slices.Clone(tc.args)
			providerID, args, resumeID, err := EffectiveProviderSession("claude", tc.resume, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if resumeID != tc.resume || !ValidProviderSessionID(providerID) {
				t.Fatalf("provider identity = %q, resume = %q", providerID, resumeID)
			}
			want := tc.args
			if tc.wantProvider == "" {
				want = append([]string{"--session-id", providerID}, want...)
			} else if providerID != tc.wantProvider {
				t.Fatalf("provider ID = %q, want %q", providerID, tc.wantProvider)
			}
			if !slices.Equal(args, want) || !slices.Equal(tc.args, original) {
				t.Fatalf("native arguments changed: got %q, want %q; caller %q", args, want, tc.args)
			}
		})
	}
}

func TestEffectiveProviderSessionRecordsCodexResume(t *testing.T) {
	args := []string{"-m", "existing-model"}
	for _, id := range []string{"", "resumed"} {
		providerID, gotArgs, resumeID, err := EffectiveProviderSession("codex", id, args)
		if err != nil || providerID != id || resumeID != id || !slices.Equal(gotArgs, args) {
			t.Fatalf("Codex launch binding = %q args=%v resume=%q err=%v", providerID, gotArgs, resumeID, err)
		}
	}
	if _, _, _, err := EffectiveProviderSession("codex", "../victim", args); err == nil {
		t.Fatal("invalid Codex resume identity was accepted")
	}
}

func TestPrepareBrowserResumeAfterClaudeClearUsesRecordedConversation(t *testing.T) {
	cfg, dir, scope := forkFixture(t, false)
	cwd := scope.AllowedPaths[0]
	home := EffectiveSessionHome(cfg, scope.Identity)
	want := recordForkSessionStart(t, dir, home, cwd, "after-clear")
	if err := egg.CaptureSessionHistory("claude", cwd, dir, home, time.Time{}, "provider"); err != nil {
		t.Fatal(err)
	}
	// The archive retains its binding even after the hook spool is removed.
	if err := os.RemoveAll(filepath.Join(home, ".claude", "wingthing-events", "source")); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(home, ".claude", "projects", strings.ReplaceAll(cwd, "/", "-"))
	if err := os.Remove(filepath.Join(project, "after-clear.jsonl")); err != nil {
		t.Fatal(err)
	}
	start := ws.PTYStart{SessionID: "resumed", ResumeSessionID: "source", UserID: "alice", Agent: "claude", CWD: cwd}
	id, _, release, err := PrepareBrowserResume(cfg, &config.WingConfig{}, start, []string{cwd}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release(false)
	if id != "after-clear" {
		t.Fatalf("resumed %q instead of the recorded conversation", id)
	}
	if err := verifyProviderResumeReservation(cfg, home, "claude", id, "source", "resumed"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(project, id+".jsonl")); err != nil || string(got) != want {
		t.Fatalf("restored history = %q, err = %v", got, err)
	}
}

func TestPrepareBrowserResumeRefusesUnrecordedClaudeConversation(t *testing.T) {
	cfg, dir, scope := forkFixture(t, false)
	cwd := scope.AllowedPaths[0]
	home := EffectiveSessionHome(cfg, scope.Identity)
	recordForkSessionStart(t, dir, home, cwd, "after-clear")
	if err := egg.CaptureSessionHistory("claude", cwd, dir, home, time.Time{}, "provider"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat.meta"), []byte("agent=claude\ncwd="+cwd+"\nagent_session_id=unrecorded\n"), 0600); err != nil {
		t.Fatal(err)
	}
	start := ws.PTYStart{SessionID: "resumed", ResumeSessionID: "source", UserID: "alice", Agent: "claude", CWD: cwd}
	if _, _, release, err := PrepareBrowserResume(cfg, &config.WingConfig{}, start, []string{cwd}, false); err == nil {
		release(false)
		t.Fatal("resumed an unrecorded conversation")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects", strings.ReplaceAll(cwd, "/", "-"), "unrecorded.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("unrecorded conversation was restored: %v", err)
	}
}
