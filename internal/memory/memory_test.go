package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	writeFile(t, dir, "index.md", `---
topic: index
---

# Memory Index

- identity: who I am
- projects: active projects
`)

	writeFile(t, dir, "identity.md", `---
topic: identity
tags: [personal, name]
---

# Identity

Name: Bryan Ehrlich
Role: Staff Engineer
`)

	writeFile(t, dir, "projects.md", `---
topic: projects
tags: [work, slide, pr, deploy]
---

# Active Projects

## Slide (Work)
- Current sprint: SLIDE-4521
- PR open: #892

## Lang (Side Project)
- Status: bootstrapping
`)

	writeFile(t, dir, "no-frontmatter.md", `# Just a heading

Some body content without frontmatter.
`)

	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// --- Frontmatter parsing ---

func TestParseFrontmatter(t *testing.T) {
	data := []byte(`---
topic: projects
tags: [work, slide]
---

# Projects

Body here.
`)
	fm, body := parseFrontmatterBytes(data)
	if fm == nil {
		t.Fatal("expected frontmatter")
	}
	if fm["topic"] != "projects" {
		t.Errorf("topic = %v, want projects", fm["topic"])
	}
	if body != "# Projects\n\nBody here.\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParseFrontmatterMissing(t *testing.T) {
	data := []byte("# No frontmatter\n\nJust body.\n")
	fm, body := parseFrontmatterBytes(data)
	if fm != nil {
		t.Errorf("expected nil frontmatter, got %v", fm)
	}
	if body != "# No frontmatter\n\nJust body.\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParseFrontmatterMalformed(t *testing.T) {
	data := []byte("---\n: invalid yaml [[\n---\n\nBody.\n")
	fm, body := parseFrontmatterBytes(data)
	if fm != nil {
		t.Errorf("expected nil frontmatter for malformed yaml, got %v", fm)
	}
	// Falls back to entire content as body
	if body != string(data) {
		t.Errorf("expected raw data as body for malformed yaml")
	}
}

// --- Load ---

func TestLoad(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	body := s.Load("identity")
	if body == "" {
		t.Fatal("expected non-empty body")
	}
	if body != "# Identity\n\nName: Bryan Ehrlich\nRole: Staff Engineer\n" {
		t.Errorf("body = %q", body)
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	body := s.Load("nonexistent")
	if body != "" {
		t.Errorf("expected empty string for missing file, got %q", body)
	}
}

func TestLoadCaches(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	body1 := s.Load("identity")
	body2 := s.Load("identity")
	if body1 != body2 {
		t.Error("expected same content on second load")
	}
}

func TestLoadNoFrontmatter(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	body := s.Load("no-frontmatter")
	if body != "# Just a heading\n\nSome body content without frontmatter.\n" {
		t.Errorf("body = %q", body)
	}
}

// --- Index ---

func TestIndex(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	idx := s.Index()
	if idx == "" {
		t.Fatal("expected non-empty index")
	}
	if idx != "# Memory Index\n\n- identity: who I am\n- projects: active projects\n" {
		t.Errorf("index = %q", idx)
	}
}

func TestIndexMissing(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)

	idx := s.Index()
	if idx != "" {
		t.Errorf("expected empty index for missing file, got %q", idx)
	}
}

// --- Retrieve Layer 1: index always loaded ---

func TestRetrieveAlwaysIncludesIndex(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	entries := s.Retrieve("", nil)
	if len(entries) == 0 {
		t.Fatal("expected at least index entry")
	}
	if entries[0].Name != "index" {
		t.Errorf("first entry = %q, want index", entries[0].Name)
	}
	if entries[0].Body == "" {
		t.Error("expected non-empty index body")
	}
}

// --- Retrieve Layer 2: skill-declared deps ---

func TestRetrieveSkillDeps(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	entries := s.Retrieve("", []string{"identity", "projects"})
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (index + 2 deps)", len(entries))
	}
	if entries[0].Name != "index" {
		t.Errorf("entries[0] = %q, want index", entries[0].Name)
	}
	if entries[1].Name != "identity" {
		t.Errorf("entries[1] = %q, want identity", entries[1].Name)
	}
	if entries[2].Name != "projects" {
		t.Errorf("entries[2] = %q, want projects", entries[2].Name)
	}
}

func TestRetrieveSkillDepsDedupIndex(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	entries := s.Retrieve("", []string{"index", "identity"})
	names := make(map[string]int)
	for _, e := range entries {
		names[e.Name]++
	}
	if names["index"] != 1 {
		t.Errorf("index appeared %d times, want 1", names["index"])
	}
}

func TestRetrieveMissingDep(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	entries := s.Retrieve("", []string{"nonexistent"})
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (index + missing dep)", len(entries))
	}
	if entries[1].Name != "nonexistent" {
		t.Errorf("entries[1] = %q, want nonexistent", entries[1].Name)
	}
	if entries[1].Body != "" {
		t.Errorf("expected empty body for missing dep, got %q", entries[1].Body)
	}
}

// --- Retrieve Layer 3: keyword matching ---

func TestRetrieveKeywordMatchTag(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	entries := s.Retrieve("check my PR status", nil)
	found := false
	for _, e := range entries {
		if e.Name == "projects" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected projects.md to match keyword 'pr' from tags")
	}
}

func TestRetrieveKeywordMatchHeading(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	entries := s.Retrieve("what is my identity", nil)
	found := false
	for _, e := range entries {
		if e.Name == "identity" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected identity.md to match keyword 'identity' from heading")
	}
}

func TestRetrieveKeywordNoMatch(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	entries := s.Retrieve("weather forecast tomorrow", nil)
	// Should only have index
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name
		}
		t.Errorf("got entries %v, expected only [index]", names)
	}
}

func TestRetrieveNoDuplicates(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	// "deploy" matches projects.md tag, and projects is also a skill dep
	entries := s.Retrieve("check deploy", []string{"projects"})
	names := make(map[string]int)
	for _, e := range entries {
		names[e.Name]++
	}
	for name, count := range names {
		if count > 1 {
			t.Errorf("%s appeared %d times", name, count)
		}
	}
}

// --- Frontmatter access ---

func TestFrontmatter(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	fm := s.Frontmatter("projects")
	if fm == nil {
		t.Fatal("expected frontmatter")
	}
	if fm["topic"] != "projects" {
		t.Errorf("topic = %v, want projects", fm["topic"])
	}
}

func TestFrontmatterMissing(t *testing.T) {
	dir := setupTestDir(t)
	s := New(dir)

	fm := s.Frontmatter("nonexistent")
	if fm != nil {
		t.Errorf("expected nil frontmatter for missing file, got %v", fm)
	}
}

// --- Tokenize ---

func TestTokenize(t *testing.T) {
	tokens := tokenize("Check my PR status!")
	expected := map[string]bool{"check": true, "my": true, "pr": true, "status": true}
	for _, tok := range tokens {
		if !expected[tok] {
			t.Errorf("unexpected token %q", tok)
		}
	}
}

func TestTokenizeDedup(t *testing.T) {
	tokens := tokenize("PR PR PR status status")
	counts := make(map[string]int)
	for _, tok := range tokens {
		counts[tok]++
	}
	for tok, count := range counts {
		if count > 1 {
			t.Errorf("token %q appeared %d times", tok, count)
		}
	}
}

func TestTokenizeStripsShortWords(t *testing.T) {
	tokens := tokenize("a b I the pr")
	for _, tok := range tokens {
		if len(tok) < 2 {
			t.Errorf("short token %q should have been filtered", tok)
		}
	}
}
