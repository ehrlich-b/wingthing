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
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

func recoveryFixture(t *testing.T, cfg *config.Config, id string) string {
	t.Helper()
	dir := filepath.Join(cfg.Dir, "eggs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\ncwd=/fixture\nprovider_home="+EffectiveSessionHome(cfg, EggIdentity{})+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := egg.WriteLaunchIntent(dir, egg.LaunchIntent{Version: 1, Agent: "claude", CWD: "/fixture", Started: true, ProviderSessionID: "provider-" + id, Model: "claude-opus"}); err != nil {
		t.Fatal(err)
	}
	writeRecoveryAuthorityFixture(t, dir, egg.UnsandboxedEggConfig())
	return dir
}

func writeRecoveryAuthorityFixture(t *testing.T, dir string, policy *egg.EggConfig) {
	t.Helper()
	intent, err := egg.ReadLaunchIntent(dir)
	if err != nil {
		t.Fatal(err)
	}
	record, err := egg.NewRecoveryRecord(intent, policy, ReadEggMetaValues(dir)["provider_home"])
	if err != nil {
		t.Fatal(err)
	}
	record.Principal, record.OwnerID, record.OwnerEmail = ReadSessionPrincipal(dir), ReadEggOwner(dir), ReadEggOwnerEmail(dir)
	record.ProviderHome = ReadEggMetaValues(dir)["provider_home"]
	if err := egg.WriteRecoveryRecord(dir, record); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(policy.SourcePath, []byte("base: none\nenv: ['*']\nnetwork: ['*']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = SpawnEgg(cfg, "spawn", "claude", policy, 24, 80, cfg.Dir, false, false, false, EggIdentity{}, 0,
		SpawnEggOpts{ProviderReserved: true, ResumeSessionID: "provider", ResumeSourceSessionID: "source", AgentArgs: []string{"--model", "opus", "--settings", "secret-settings"}, Label: "coordinator"})
	if err == nil || !strings.Contains(err.Error(), "reservation is unavailable") {
		t.Fatalf("unexpected admission: %v", err)
	}
	dir := filepath.Join(cfg.Dir, "eggs", "spawn")
	intent, err := egg.ReadLaunchIntent(dir)
	if err != nil || intent.Agent != "claude" || intent.CWD != wingpolicy.CanonicalSessionPath(cfg.Dir) || intent.Label != "coordinator" || intent.ProviderSessionID != "provider" || intent.Model != "opus" || intent.EggConfig != wingpolicy.CanonicalSessionPath(policy.SourcePath) || intent.ConversationID != "root" || intent.RootConversationID != "root" || intent.Started {
		t.Fatalf("launch intent: %+v %v", intent, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, egg.LaunchIntentFile))
	if err != nil || strings.Contains(string(data), "secret-settings") {
		t.Fatalf("argv persisted in intent: %s %v", data, err)
	}
	if got := ClassifyEgg(cfg, "spawn"); got.Class != RecoveryArchived {
		t.Fatalf("failed launch eligible: %+v", got)
	}
	record, err := egg.ReadRecoveryRecord(dir)
	if err != nil || record.Intent != intent || record.Sandboxed || record.ConfigSHA256 == "" || record.PolicySHA256 == "" {
		t.Fatalf("missing admission ceiling: %+v %v", record, err)
	}
}

func TestRecoveryNestedSpawnCannotCreateHostAuthority(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("fixture requires ordinary permission enforcement")
	}
	old := config.ReleaseChannel
	config.ReleaseChannel = "stable"
	t.Cleanup(func() { config.ReleaseChannel = old })
	state, err := os.MkdirTemp("", "wt-rec-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	recovery := filepath.Join(state, "recovery")
	if err := os.Mkdir(recovery, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(recovery, 0700) })
	cfg := &config.Config{Dir: state}
	_, err = SpawnEgg(cfg, "nested", "claude", egg.UnsandboxedEggConfig(), 24, 80, state, false, false, false, EggIdentity{}, 0,
		SpawnEggOpts{ProviderReserved: true, ResumeSessionID: "provider", ResumeSourceSessionID: "source"})
	if err == nil || !strings.Contains(err.Error(), "reservation is unavailable") {
		t.Fatalf("nested launch did not reach provider admission: %v", err)
	}
	dir := filepath.Join(state, "eggs", "nested")
	if _, err := egg.ReadLaunchIntent(dir); err != nil {
		t.Fatal(err)
	}
	if err := egg.UpdateLaunchIntent(dir, func(i *egg.LaunchIntent) { i.Started = true }); err != nil {
		t.Fatalf("nested provider cannot persist its local lifecycle: %v", err)
	}
	if got := ClassifyEgg(cfg, "nested"); got.Class != RecoveryArchived {
		t.Fatalf("nested launch became recoverable: %+v", got)
	}
	if err := egg.MarkDeliberateStop(dir, "idle"); err != nil {
		t.Fatalf("nested provider cannot persist its deliberate stop: %v", err)
	}
	if err := os.Chmod(recovery, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := egg.ReadRecoveryRecord(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nested launch created authority: %v", err)
	}
}

func TestRecoveryClassificationCapturesExactHookIdentity(t *testing.T) {
	for _, agent := range []string{"claude", "codex", "gemini", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			cfg := &config.Config{Dir: t.TempDir()}
			dir := recoveryFixture(t, cfg, "hook-session")
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent="+agent+"\ncwd=/fixture\nprovider_home="+home+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := egg.WriteLaunchIntent(dir, egg.LaunchIntent{Version: 1, Agent: agent, CWD: "/fixture", Started: true}); err != nil {
				t.Fatal(err)
			}
			writeRecoveryAuthorityFixture(t, dir, egg.UnsandboxedEggConfig())
			base := "." + agent
			if agent == "opencode" {
				base = filepath.Join(".local", "share", "opencode")
			}
			spool := filepath.Join(home, base, "wingthing-events", "hook-session")
			if err := os.MkdirAll(spool, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(spool, "seq.00000000000000000001.json"), []byte(`{"session_id":"exact-thread","hook_event_name":"SessionStart"}`), 0600); err != nil {
				t.Fatal(err)
			}
			// Even a journal claiming this exact hook was already imported is
			// only an egg-directory hint, not provider identity authority.
			journal := `{"sequence":1,"type":"session_ready","state":"idle","source":"` + agent + `_hook","source_key":"hook:seq.00000000000000000001.json","provider_session_id":"forged-thread"}` + "\n"
			if err := os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), []byte(journal), 0600); err != nil {
				t.Fatal(err)
			}
			if got := ClassifyEgg(cfg, "hook-session"); got.Class != RecoveryEligible || got.Intent.ProviderSessionID != "exact-thread" {
				t.Fatalf("native identity not reconciled: %+v", got)
			}
			record, err := egg.ReadRecoveryRecord(dir)
			if err != nil || record.Intent.ProviderSessionID != "exact-thread" {
				t.Fatalf("native identity not protected: %+v %v", record, err)
			}
		})
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
	if err := egg.UpdateRecoveryRecord(missing, func(r *egg.RecoveryRecord) { r.Intent.ProviderSessionID = "" }); err != nil {
		t.Fatal(err)
	}
	stopped := recoveryFixture(t, cfg, "stopped")
	if err := egg.MarkDeliberateStop(stopped, "kill"); err != nil {
		t.Fatal(err)
	}
	legacy := recoveryFixture(t, cfg, "legacy")
	if err := os.Remove(filepath.Join(cfg.Dir, "recovery", "legacy.json")); err != nil {
		t.Fatal(err)
	}
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
	record, err := egg.ReadRecoveryRecord(dir)
	intent := record.Intent
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
	data, err := os.ReadFile(filepath.Join(cfg.Dir, "recovery", "source.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"env", "credential", "args", "token"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("unexpected %s in intent", forbidden)
		}
	}
	info, err := os.Stat(filepath.Join(cfg.Dir, "recovery", "source.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("intent permissions: %v %v", info, err)
	}
}
