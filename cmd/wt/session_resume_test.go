package main

import (
	"compress/gzip"
	"os"

	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
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
	if err := os.WriteFile(filepath.Join(dir, eggclient.ProviderResumeMetadataFile), []byte("agent="+agent+"\nprovider_session_id="+providerID+"\nsource_session_id=\n"), 0o600); err != nil {
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
	providerID, restoredCWD, release, err := eggclient.PrepareBrowserResume(cfg, wingCfg, start, []string{cwd}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release(true)
	if providerID != "provider-id" || restoredCWD != wingpolicy.CanonicalSessionPath(cwd) {
		t.Fatalf("provider=%q cwd=%q", providerID, restoredCWD)
	}
	restored := filepath.Join(cfg.Dir, "user-homes", eggclient.UserHash("alice"), ".claude", "projects", strings.ReplaceAll(wingpolicy.CanonicalSessionPath(cwd), "/", "-"), "provider-id.jsonl")
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
			if _, _, _, err := eggclient.PrepareBrowserResume(cfg, &config.WingConfig{Org: "slide"}, start, paths, true); err == nil {
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
	providerID, _, release, err := eggclient.PrepareBrowserResume(cfg, &config.WingConfig{}, start, nil, true)
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
	if _, _, _, err := eggclient.PrepareBrowserResume(cfg, &config.WingConfig{Org: "slide"}, member, nil, true); err == nil {
		t.Fatal("org member without paths was allowed to resume")
	}
}

func TestPrepareBrowserResumeReservesIdenticalProviderConversationUntilRelease(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := t.TempDir()
	writeResumeSessionFixture(t, cfg, "old-session", "alice", "claude", cwd, "provider-id", "conversation\n")
	wingCfg := &config.WingConfig{Org: "slide"}
	first := ws.PTYStart{SessionID: "new-one", ResumeSessionID: "old-session", UserID: "alice", Agent: "claude", CWD: cwd}
	_, _, release, err := eggclient.PrepareBrowserResume(cfg, wingCfg, first, []string{cwd}, true)
	if err != nil {
		t.Fatal(err)
	}
	reservationPath := filepath.Join(cfg.Dir, "eggs", first.SessionID, eggclient.ProviderResumeMetadataFile)
	if _, err := os.Stat(reservationPath); err != nil {
		t.Fatalf("resume identity was not persisted: %v", err)
	}
	second := first
	second.SessionID = "new-two"
	if _, _, _, err := eggclient.PrepareBrowserResume(cfg, wingCfg, second, []string{cwd}, true); err == nil || !strings.Contains(err.Error(), "already being resumed") {
		t.Fatalf("concurrent identical resume error = %v", err)
	}
	release(true)
	if _, err := os.Stat(reservationPath); err != nil {
		t.Fatalf("spawned resume identity did not persist: %v", err)
	}
	_, _, releaseAgain, err := eggclient.PrepareBrowserResume(cfg, wingCfg, second, []string{cwd}, true)
	if err != nil {
		t.Fatalf("resume remained reserved after release: %v", err)
	}
	releaseAgain(true)
}

func TestSessionResumeStatusRequiresNativeMetadata(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := t.TempDir()
	dir := writeResumeSessionFixture(t, cfg, "old-session", "alice", "claude", cwd, "provider-id", "conversation\n")
	if ok, reason := eggclient.SessionResumeStatus(dir, "claude", cwd); !ok || reason != "" {
		t.Fatalf("resumable=%v reason=%q", ok, reason)
	}
	if ok, _ := eggclient.SessionResumeStatus(dir, "ollama", cwd); ok {
		t.Fatal("agent without native resume marked resumable")
	}
	if err := os.Remove(filepath.Join(dir, "chat.meta")); err != nil {
		t.Fatal(err)
	}
	if ok, _ := eggclient.SessionResumeStatus(dir, "claude", cwd); ok {
		t.Fatal("missing metadata marked resumable")
	}
}

func TestCollectSessionsHistoryUsesDurableNameStartAndProviderResumeStatus(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := t.TempDir()
	dir := writeResumeSessionFixture(t, cfg, "old-session", "alice", "claude", cwd, "provider-id", "conversation\n")
	if err := eggclient.WriteSessionName(dir, "support-case"); err != nil {
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
