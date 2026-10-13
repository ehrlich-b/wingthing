package eggclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
)

func TestSessionCatalogCoordinatorNativeEvidenceRetainsItsAge(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	events := []egg.SessionEvent{
		{Sequence: 1, Type: "session_ready", Source: "codex_hook", State: "working", Timestamp: old},
		{Sequence: 2, Type: "message", Source: "codex_transcript", Role: "assistant", Text: "later content is not a native state transition", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)},
	}
	journal := ""
	for _, event := range events {
		raw, _ := json.Marshal(event)
		journal += string(raw) + "\n"
	}
	catalogFixture(t, cfg, "coordinator", "wing", "codex", journal)
	dir := filepath.Join(cfg.Dir, "eggs", "coordinator")
	if err := sessionrole.Write(dir, sessionrole.Metadata{Role: "coordinator", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "egg.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		rows, _, err := SessionCatalog(t.Context(), cfg, "origin", "machine", "", nil)
		if err != nil || len(rows) != 1 || !rows[0].Live {
			t.Fatalf("live catalog: %v %v", rows, err)
		}
		p := rows[0].Presence
		if p == nil || p.State != "unknown" || !p.Stale || p.ObservedAt != old || p.Source != "native_lifecycle" {
			t.Fatalf("stale hook refreshed on read: %+v", p)
		}
	}
}
