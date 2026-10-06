package tunnel

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

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

func TestSessionHistoryForkAvailabilityRequiresClaudeHistory(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	cwd := t.TempDir()
	writeResumeSessionFixture(t, cfg, "claude-source", "alice", "claude", cwd, "provider", "{}\n")
	writeResumeSessionFixture(t, cfg, "codex-source", "alice", "codex", cwd, "other", "{}\n")
	for _, session := range collectSessionsHistory(cfg) {
		if session.SessionID == "claude-source" && (!session.Forkable || session.ForkUnavailableReason != "") {
			t.Fatalf("Claude fork unavailable: %#v", session)
		}
		if session.SessionID == "codex-source" && (session.Forkable || session.ForkUnavailableReason == "") {
			t.Fatalf("unsupported fork visible: %#v", session)
		}
	}
}
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
