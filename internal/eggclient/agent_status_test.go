package eggclient

import (
	"context"

	"os"

	"path/filepath"
	"strconv"

	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestAgentStatusLegacyEggAndBrokenJournalRemainListable(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	cfg := &config.Config{Dir: root}
	for _, id := range []string{"old-egg", "broken-journal"} {
		dir := filepath.Join(root, "eggs", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string]string{
			"egg.pid": strconv.Itoa(os.Getpid()), "egg.meta": "agent=claude\nprovider_home=" + home + "\nprovider_session_id=provider-exact\n",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if id == "broken-journal" {
			if err := os.WriteFile(filepath.Join(dir, "lifecycle.jsonl"), []byte("invalid\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	sessions, err := DiscoverActiveSessions(context.Background(), cfg)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("legacy discovery failed: %v, %v", sessions, err)
	}
	for _, session := range sessions {
		if session.Status != "unknown" {
			t.Fatalf("old egg invented state: %+v", session)
		}
	}
}
