package wing

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
)

func TestSessionReportStoppedPinSurvivesWebInventoryWithoutChangingActiveDiscovery(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	for _, id := range []string{"coordinator", "ordinary"} {
		dir := filepath.Join(cfg.Dir, "eggs", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=codex\ncwd="+cfg.Dir+"\nprovider_home="+cfg.Dir+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := eggclient.WriteEggOwner(dir, "owner", ""); err != nil {
			t.Fatal(err)
		}
		if id == "coordinator" {
			if err := sessionrole.Write(dir, sessionrole.Metadata{Pinned: true, Role: "coordinator"}); err != nil {
				t.Fatal(err)
			}
			if _, err := egg.RecordSessionReport(dir, egg.SessionReport{Goal: "Keep the release moving", Update: "Ready", NextAction: "Review", Presence: "awake"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := egg.RecordSessionProcessEvent(dir, "session_exit", "stopped", "fixture exit"); err != nil {
			t.Fatal(err)
		}
	}
	if rows := ListAliveEggSessions(cfg); len(rows) != 0 {
		t.Fatalf("active=%v", rows)
	}
	rows := ListWebEggSessions(cfg)
	if len(rows) != 1 || rows[0].SessionID != "coordinator" || !rows[0].Pinned || rows[0].UserID != "owner" || rows[0].Lifecycle["process_alive"] != false {
		t.Fatalf("web=%v", rows)
	}
	if report, ok := rows[0].Lifecycle["report"].(*egg.SessionReport); !ok || report.Goal != "Keep the release moving" {
		t.Fatalf("report=%v", rows[0].Lifecycle)
	}
	if err := sessionrole.Write(filepath.Join(cfg.Dir, "eggs", "coordinator"), sessionrole.Metadata{Role: "coordinator"}); err != nil {
		t.Fatal(err)
	}
	if rows := ListWebEggSessions(cfg); len(rows) != 0 {
		t.Fatalf("unpin=%v", rows)
	}
}
