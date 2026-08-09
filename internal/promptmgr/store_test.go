package promptmgr

import (
	"strings"
	"testing"
)

func TestSaveRenderHistoryAndConflict(t *testing.T) {
	store := New(t.TempDir())
	first, err := store.Save(Asset{
		Name: "review", Description: "Review a change", Template: "Review {{.target}}",
		Variables: []string{"target"}, Agent: "codex", CWD: "/work/repo",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Revision) != 12 || first.CreatedAt == "" || first.UpdatedAt == "" {
		t.Fatalf("missing revision metadata: %+v", first)
	}
	rendered, err := Render(first, map[string]string{"target": "the parser"})
	if err != nil {
		t.Fatal(err)
	}
	if rendered != "Review the parser" {
		t.Fatalf("rendered = %q", rendered)
	}

	second, err := store.Save(Asset{
		Name: "review", Description: "Review a change", Template: "Deeply review {{.target}}",
		Variables: []string{"target"}, Agent: "codex", CWD: "/work/repo",
	}, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision == first.Revision || second.CreatedAt != first.CreatedAt {
		t.Fatalf("revision history not advanced: first=%+v second=%+v", first, second)
	}
	historical, err := store.Get("review", first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if historical.Template != first.Template {
		t.Fatalf("historical template = %q", historical.Template)
	}
	if _, err := store.Save(*first, "stale0000000"); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected revision conflict, got %v", err)
	}
	assets, err := store.List()
	if err != nil || len(assets) != 1 || assets[0].Revision != second.Revision {
		t.Fatalf("list = %+v, err=%v", assets, err)
	}
}

func TestValidationAndRenderFailures(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Save(Asset{Name: "../escape", Template: "no"}, ""); err == nil {
		t.Fatal("path traversal name was accepted")
	}
	if _, err := store.Save(Asset{Name: "relative-cwd", Template: "no", CWD: "repo"}, ""); err == nil {
		t.Fatal("relative cwd was accepted")
	}
	asset := &Asset{Name: "vars", Template: "{{.required}}", Variables: []string{"required"}}
	if _, err := Render(asset, nil); err == nil {
		t.Fatal("missing variable was accepted")
	}
	if _, err := Render(asset, map[string]string{"required": "yes", "extra": "no"}); err == nil {
		t.Fatal("undeclared variable was accepted")
	}
}
