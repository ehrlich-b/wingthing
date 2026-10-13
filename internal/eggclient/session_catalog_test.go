package eggclient

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func catalogFixture(t *testing.T, cfg *config.Config, id, name, agent, content string) {
	t.Helper()
	dir := filepath.Join(cfg.Dir, "eggs", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for file, value := range map[string]string{"egg.meta": "agent=" + agent + "\ncwd=/personal/repo\nprovider_home=" + cfg.Dir + "\n", "session.name": name, "lifecycle.jsonl": content} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestSessionCatalogSearchLivePastAndUnauthorizedContent(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	catalogFixture(t, cfg, "archived-codex", "DNS repair", "codex", `{"sequence":1,"type":"message","source":"codex_transcript","text":"fixed the DNS helper"}`+"\n")
	catalogFixture(t, cfg, "foreign", "private name", "codex", `{"sequence":1,"type":"message","source":"codex_transcript","text":"forbidden discovery"}`+"\n")
	catalogFixture(t, cfg, "archived-claude", "other repair", "claude", "")
	file, e := os.Create(filepath.Join(cfg.Dir, "eggs", "archived-claude", "chat.jsonl.gz"))
	if e != nil {
		t.Fatal(e)
	}
	gz := gzip.NewWriter(file)
	_, _ = gz.Write([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"archived legacy response"}]}}` + "\n"))
	_ = gz.Close()
	_ = file.Close()
	owns := func(s LocalSession) bool { return s.ID != "foreign" }
	for _, tc := range []struct {
		q     string
		count int
		id    string
	}{{"DNS helper", 1, "archived-codex"}, {"repo", 2, ""}, {"forge", 2, ""}, {"archived legacy", 1, "archived-claude"}, {"forbidden", 0, ""}} {
		rows, full, e := SessionCatalog(t.Context(), cfg, "origin", "forge", tc.q, owns)
		if e != nil || !full || len(rows) != tc.count {
			t.Fatalf("q=%s rows=%+v full=%t err=%v", tc.q, rows, full, e)
		}
		if tc.id != "" && (rows[0].SessionID != tc.id || rows[0].Live) {
			t.Fatalf("archived identity wrong: %+v", rows)
		}
	}
}
func TestSessionCatalogCancellationAndNonregularArchive(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	catalogFixture(t, cfg, "run", "test", "unsupported", "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, e := SessionCatalog(ctx, cfg, "origin", "machine", "", nil); e == nil {
		t.Fatal("cancel ignored")
	}
	if err := os.Symlink(filepath.Join(cfg.Dir, "eggs", "run", "egg.meta"), filepath.Join(cfg.Dir, "eggs", "run", "chat.jsonl.gz")); err != nil {
		t.Fatal(err)
	}
	rows, full, e := SessionCatalog(t.Context(), cfg, "origin", "machine", "test", nil)
	if e != nil || full || len(rows) != 1 || !rows[0].SearchIncomplete {
		t.Fatalf("nonregular file not incomplete: %+v %t %v", rows, full, e)
	}
	if _, _, e := SessionCatalog(t.Context(), cfg, "origin", "machine", "a b c d e f g h i", nil); e == nil {
		t.Fatal("query bound ignored")
	}
}

func TestSessionCatalogMalformedRecordReportsIncomplete(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	catalogFixture(t, cfg, "run", "visible", "unsupported", "invalid json\n")
	rows, complete, e := SessionCatalog(t.Context(), cfg, "origin", "machine", "visible", nil)
	if e != nil || complete || len(rows) != 1 || !rows[0].SearchIncomplete {
		t.Fatalf("malformed data claimed complete: %+v %t %v", rows, complete, e)
	}
}
