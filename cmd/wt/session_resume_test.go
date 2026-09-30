package main

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func writeResumeSessionFixture(t *testing.T, cfg *config.Config, sessionID, owner, agent, cwd, providerID, content string) string {
	t.Helper()
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.owner"), []byte(owner+"\nowner@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent="+agent+"\ncwd="+cwd+"\nstarted_at=100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chat.meta"), []byte("agent_session_id="+providerID+"\nagent="+agent+"\nformat=jsonl\ncwd="+cwd+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, providerResumeMetadataFile), []byte("agent="+agent+"\nprovider_session_id="+providerID+"\nsource_session_id=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "chat.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPrepareBrowserResumeRestoresOwnedProviderConversationWithoutChangingSource(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := t.TempDir()
	sourceDir := writeResumeSessionFixture(t, cfg, "old-session", "alice", "claude", cwd, "provider-id", "conversation\n")
	before, err := os.ReadFile(filepath.Join(sourceDir, "chat.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	wingCfg := &config.WingConfig{Org: "slide"}
	start := ws.PTYStart{SessionID: "new-session", ResumeSessionID: "old-session", UserID: "alice", Agent: "claude", CWD: cwd}
	providerID, restoredCWD, release, err := prepareBrowserResume(cfg, wingCfg, start, []string{cwd}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release(true)
	if providerID != "provider-id" || restoredCWD != canonicalSessionPath(cwd) {
		t.Fatalf("provider=%q cwd=%q", providerID, restoredCWD)
	}
	restored := filepath.Join(cfg.Dir, "user-homes", userHash("alice"), ".claude", "projects", strings.ReplaceAll(canonicalSessionPath(cwd), "/", "-"), "provider-id.jsonl")
	data, err := os.ReadFile(restored)
	if err != nil || string(data) != "conversation\n" {
		t.Fatalf("restored=%q err=%v", data, err)
	}
	after, _ := os.ReadFile(filepath.Join(sourceDir, "chat.jsonl.gz"))
	if string(before) != string(after) {
		t.Fatal("source history changed")
	}
}

func TestPrepareBrowserResumeRequiresExactOwnerAgentAndCurrentPath(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := t.TempDir()
	writeResumeSessionFixture(t, cfg, "old-session", "alice", "claude", cwd, "provider-id", "conversation\n")
	base := ws.PTYStart{SessionID: "new-session", ResumeSessionID: "old-session", UserID: "alice", Agent: "claude", CWD: cwd}
	for name, mutate := range map[string]func(*ws.PTYStart, *[]string){
		"other owner":  func(start *ws.PTYStart, _ *[]string) { start.UserID = "bob" },
		"other agent":  func(start *ws.PTYStart, _ *[]string) { start.Agent = "codex" },
		"other cwd":    func(start *ws.PTYStart, _ *[]string) { start.CWD = t.TempDir() },
		"revoked path": func(_ *ws.PTYStart, paths *[]string) { *paths = []string{t.TempDir()} },
	} {
		t.Run(name, func(t *testing.T) {
			start := base
			paths := []string{cwd}
			mutate(&start, &paths)
			if _, _, _, err := prepareBrowserResume(cfg, &config.WingConfig{Org: "slide"}, start, paths, true); err == nil {
				t.Fatal("resume unexpectedly allowed")
			}
		})
	}
}

func TestPrepareBrowserResumeAllowsPersonalOwnerWithoutConfiguredPaths(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := t.TempDir()
	writeResumeSessionFixture(t, cfg, "old-session", "alice", "claude", cwd, "provider-id", "conversation\n")
	start := ws.PTYStart{
		SessionID: "new-session", ResumeSessionID: "old-session", UserID: "alice",
		OrgRole: "owner", Agent: "claude", CWD: cwd,
	}
	providerID, _, release, err := prepareBrowserResume(cfg, &config.WingConfig{}, start, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release(false)
	if providerID != "provider-id" {
		t.Fatalf("provider ID = %q", providerID)
	}
	member := start
	member.SessionID = "member-session"
	member.OrgRole = "member"
	if _, _, _, err := prepareBrowserResume(cfg, &config.WingConfig{Org: "slide"}, member, nil, true); err == nil {
		t.Fatal("org member without paths was allowed to resume")
	}
}

func TestPrepareBrowserResumeReservesIdenticalProviderConversationUntilRelease(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := t.TempDir()
	writeResumeSessionFixture(t, cfg, "old-session", "alice", "claude", cwd, "provider-id", "conversation\n")
	wingCfg := &config.WingConfig{Org: "slide"}
	first := ws.PTYStart{SessionID: "new-one", ResumeSessionID: "old-session", UserID: "alice", Agent: "claude", CWD: cwd}
	_, _, release, err := prepareBrowserResume(cfg, wingCfg, first, []string{cwd}, true)
	if err != nil {
		t.Fatal(err)
	}
	reservationPath := filepath.Join(cfg.Dir, "eggs", first.SessionID, providerResumeMetadataFile)
	if _, err := os.Stat(reservationPath); err != nil {
		t.Fatalf("resume identity was not persisted: %v", err)
	}
	second := first
	second.SessionID = "new-two"
	if _, _, _, err := prepareBrowserResume(cfg, wingCfg, second, []string{cwd}, true); err == nil || !strings.Contains(err.Error(), "already being resumed") {
		t.Fatalf("concurrent identical resume error = %v", err)
	}
	release(true)
	if _, err := os.Stat(reservationPath); err != nil {
		t.Fatalf("spawned resume identity did not persist: %v", err)
	}
	_, _, releaseAgain, err := prepareBrowserResume(cfg, wingCfg, second, []string{cwd}, true)
	if err != nil {
		t.Fatalf("resume remained reserved after release: %v", err)
	}
	releaseAgain(true)
}

func TestProviderResumeReservationSurvivesReclaimUntilProviderExit(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	home := filepath.Join(cfg.Dir, "user-homes", "alice")
	key := providerResumeKey(home, "claude", "provider-id")
	runningDir := filepath.Join(cfg.Dir, "eggs", "reclaimed-session")
	if err := os.MkdirAll(runningDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runningDir, providerResumeMetadataFile), providerResumeMetadata(key, "old-session"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := providerResumeRegistry{active: make(map[string]string)}
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
	if _, err := os.Stat(filepath.Join(cfg.Dir, "eggs", "after-exit", providerResumeMetadataFile)); !os.IsNotExist(err) {
		t.Fatalf("failed resume reservation was not released: %v", err)
	}
}

func TestEffectiveProviderSessionPinsFreshClaudeAndPreservesExplicitFlags(t *testing.T) {
	providerID, args, generatedResume, err := effectiveProviderSession("claude", "", []string{"--model", "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if !validProviderSessionID(providerID) || generatedResume != "" || len(args) != 4 || args[0] != "--session-id" || args[1] != providerID {
		t.Fatalf("fresh provider launch = id %q args %#v resume %q", providerID, args, generatedResume)
	}

	explicit := []string{"--model", "opus", "--session-id=caller-id"}
	providerID, args, generatedResume, err = effectiveProviderSession("claude", "", explicit)
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "caller-id" || generatedResume != "" || strings.Join(args, "\x00") != strings.Join(explicit, "\x00") {
		t.Fatalf("explicit session ID changed: id %q args %#v resume %q", providerID, args, generatedResume)
	}

	providerID, args, generatedResume, err = effectiveProviderSession("claude", "restored-id", []string{"--resume", "caller-resume", "--model", "opus"})
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
		"fork fresh":          {args: []string{"--fork-session"}},
		"fork resumed":        {args: []string{"--resume", "source-id", "--fork-session"}},
	} {
		t.Run(name, func(t *testing.T) {
			gotProviderID, gotArgs, gotGeneratedID, err := effectiveProviderSession("claude", "", test.args)
			if err != nil {
				t.Fatal(err)
			}
			if gotProviderID != test.wantProviderID || gotGeneratedID != test.wantGeneratedID || strings.Join(gotArgs, "\x00") != strings.Join(test.args, "\x00") {
				t.Fatalf("native argv changed: provider %q args %#v generated %q", gotProviderID, gotArgs, gotGeneratedID)
			}
		})
	}

	providerID, args, generatedResume, err = effectiveProviderSession("claude", "restored-id", []string{"--fork-session"})
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "" || generatedResume != "restored-id" || strings.Join(args, "\x00") != "--fork-session" {
		t.Fatalf("generated resume fork changed: provider %q args %#v generated %q", providerID, args, generatedResume)
	}
	if _, _, _, err := effectiveProviderSession("claude", "", []string{"--session-id", "--model", "opus"}); err == nil {
		t.Fatal("bare session ID flag was accepted")
	}
}

func TestSessionResumeStatusRequiresNativeMetadata(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := t.TempDir()
	dir := writeResumeSessionFixture(t, cfg, "old-session", "alice", "claude", cwd, "provider-id", "conversation\n")
	if ok, reason := sessionResumeStatus(dir, "claude", cwd); !ok || reason != "" {
		t.Fatalf("resumable=%v reason=%q", ok, reason)
	}
	if ok, _ := sessionResumeStatus(dir, "ollama", cwd); ok {
		t.Fatal("agent without native resume marked resumable")
	}
	if err := os.Remove(filepath.Join(dir, "chat.meta")); err != nil {
		t.Fatal(err)
	}
	if ok, _ := sessionResumeStatus(dir, "claude", cwd); ok {
		t.Fatal("missing metadata marked resumable")
	}
}

func TestCollectSessionsHistoryUsesDurableNameStartAndProviderResumeStatus(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := t.TempDir()
	dir := writeResumeSessionFixture(t, cfg, "old-session", "alice", "claude", cwd, "provider-id", "conversation\n")
	if err := writeSessionName(dir, "support-case"); err != nil {
		t.Fatal(err)
	}
	history := collectSessionsHistory(cfg)
	if len(history) != 1 {
		t.Fatalf("history = %#v", history)
	}
	got := history[0]
	if got.SessionID != "old-session" || got.Name != "support-case" || got.StartedAt != 100 || !got.Resumable || got.ResumeUnavailableReason != "" {
		t.Fatalf("history entry = %#v", got)
	}
}
