package eggclient

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func recoveryFixture(t *testing.T, cfg *config.Config, id string) string {
	t.Helper()
	dir := filepath.Join(cfg.Dir, "eggs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\ncwd=/fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := egg.WriteLaunchIntent(dir, egg.LaunchIntent{Version: 1, Agent: "claude", CWD: "/fixture", Started: true, ProviderSessionID: "provider-" + id, Model: "claude-opus", EggConfig: "/fixture/egg.yaml"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRecoverySpawnPersistsIntentBeforeProviderAdmission(t *testing.T) {
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	state, err := os.MkdirTemp("", "wt-rec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	cfg := &config.Config{Dir: state}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := db.ReserveConversation(store.Conversation{ID: "root", OwnerID: "owner", SessionID: "source", LaunchKey: "initial", SpecDigest: "initial", Agent: "claude", CWD: state}); err != nil {
		t.Fatal(err)
	}
	policy := egg.UnsandboxedEggConfig()
	policy.SourcePath = filepath.Join(cfg.Dir, "custom-egg.yaml")
	_, err = SpawnEgg(cfg, "spawn", "claude", policy, 24, 80, cfg.Dir, false, false, false, EggIdentity{}, 0,
		SpawnEggOpts{ProviderReserved: true, ResumeSessionID: "provider", ResumeSourceSessionID: "source", AgentArgs: []string{"--model", "opus", "--settings", "secret-settings"}, Label: "coordinator"})
	if err == nil || !strings.Contains(err.Error(), "reservation is unavailable") {
		t.Fatalf("unexpected admission: %v", err)
	}
	dir := filepath.Join(cfg.Dir, "eggs", "spawn")
	intent, err := egg.ReadLaunchIntent(dir)
	if err != nil || intent.Agent != "claude" || intent.CWD != cfg.Dir || intent.Label != "coordinator" || intent.ProviderSessionID != "provider" || intent.Model != "opus" || intent.EggConfig != policy.SourcePath || intent.ConversationID != "root" || intent.RootConversationID != "root" || intent.Started {
		t.Fatalf("launch intent: %+v %v", intent, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, egg.LaunchIntentFile))
	if err != nil || strings.Contains(string(data), "secret-settings") {
		t.Fatalf("argv persisted in intent: %s %v", data, err)
	}
	if got := ClassifyEgg(cfg, "spawn"); got.Class != RecoveryArchived {
		t.Fatalf("failed launch eligible: %+v", got)
	}
}

func TestRecoveryClassificationCapturesExactCodexHookIdentity(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	dir := recoveryFixture(t, cfg, "codex-session")
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=codex\ncwd=/fixture\nprovider_home="+home+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := egg.WriteLaunchIntent(dir, egg.LaunchIntent{Version: 1, Agent: "codex", CWD: "/fixture", Started: true}); err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(home, ".codex", "wingthing-events", "codex-session")
	if err := os.MkdirAll(spool, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spool, "seq.00000000000000000001.json"), []byte(`{"session_id":"exact-thread","hook_event_name":"SessionStart"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ClassifyEgg(cfg, "codex-session"); got.Class != RecoveryEligible || got.Intent.ProviderSessionID != "exact-thread" {
		t.Fatalf("native identity not reconciled: %+v", got)
	}
}

func TestRecoveryClassificationAndLegacyCompatibility(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	alive := recoveryFixture(t, cfg, "alive")
	if err := os.WriteFile(filepath.Join(alive, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	recoveryFixture(t, cfg, "interrupted")
	archived := recoveryFixture(t, cfg, "unsupported")
	if err := os.WriteFile(filepath.Join(archived, "egg.meta"), []byte("agent=ollama\ncwd=/fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := egg.UpdateLaunchIntent(archived, func(i *egg.LaunchIntent) { i.Agent = "ollama" }); err != nil {
		t.Fatal(err)
	}
	missing := recoveryFixture(t, cfg, "missing-provider")
	if err := egg.UpdateLaunchIntent(missing, func(i *egg.LaunchIntent) { i.ProviderSessionID = "" }); err != nil {
		t.Fatal(err)
	}
	stopped := recoveryFixture(t, cfg, "stopped")
	if err := egg.MarkDeliberateStop(stopped, "kill"); err != nil {
		t.Fatal(err)
	}
	legacy := recoveryFixture(t, cfg, "legacy")
	if err := os.Remove(filepath.Join(legacy, egg.LaunchIntentFile)); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]RecoveryClass{"alive": RecoveryAlive, "interrupted": RecoveryEligible, "unsupported": RecoveryArchived, "missing-provider": RecoveryArchived, "stopped": RecoveryStopped, "legacy": RecoveryArchived} {
		if got := ClassifyEgg(cfg, id); got.Class != want {
			t.Errorf("%s: %s, want %s", id, got.Class, want)
		}
	}
	if err := os.WriteFile(filepath.Join(legacy, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ClassifyEgg(cfg, "legacy"); got.Class != RecoveryAlive {
		t.Fatalf("live legacy: %s", got.Class)
	}
	sessions, err := DiscoverRecoverableSessions(cfg)
	if err != nil || len(sessions) != 1 || !sessions[0].Recoverable || sessions[0].Status != "exited" {
		t.Fatalf("eligible inventory: %+v, %v", sessions, err)
	}
	CleanEggDir(stopped)
	if got := ClassifyEgg(cfg, "stopped"); got.Class != RecoveryStopped {
		t.Fatalf("stop lost during cleanup: %s", got.Class)
	}
}

func TestRecoveryAutoClaimOncePerBootAndBackoff(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	dir := recoveryFixture(t, cfg, "source")
	now := time.Unix(100, 0)
	for n, want := range []bool{true, false} {
		claimed, err := ClaimAutoRecovery(dir, "boot-a", now)
		if err != nil || claimed != want {
			t.Fatalf("attempt %d: %v %v", n, claimed, err)
		}
	}
	if err := RecordRecoveryFailure(dir, errors.New("provider unavailable"), true, now); err != nil {
		t.Fatal(err)
	}
	if claimed, err := ClaimAutoRecovery(dir, "boot-b", now.Add(time.Second)); err != nil || claimed {
		t.Fatalf("backoff ignored: %v %v", claimed, err)
	}
	if claimed, err := ClaimAutoRecovery(dir, "boot-b", now.Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("backoff never expired: %v %v", claimed, err)
	}
	intent, err := egg.ReadLaunchIntent(dir)
	if err != nil || intent.RecoveryError == "" || intent.AutoFailures != 1 {
		t.Fatalf("failure evidence: %+v %v", intent, err)
	}
}

func TestRecoveryIntentContainsOnlyLaunchMetadata(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	dir := recoveryFixture(t, cfg, "source")
	if err := RecordRecoveryFailure(dir, errors.New("env API_KEY=credential-value"), true, time.Now()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, egg.LaunchIntentFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"env", "credential", "args", "token"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("unexpected %s in intent", forbidden)
		}
	}
	info, err := os.Stat(filepath.Join(dir, egg.LaunchIntentFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("intent permissions: %v %v", info, err)
	}
}
