package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateAndGetSkill(t *testing.T) {
	s := testStore(t)

	err := s.CreateSkill("test-skill", "A test skill", "dev", "claude", `["dev","test"]`, "# content", "abc123")
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}

	sk, err := s.GetSkill("test-skill")
	if err != nil {
		t.Fatalf("get skill: %v", err)
	}
	if sk == nil {
		t.Fatal("expected skill, got nil")
	}
	if sk.Name != "test-skill" {
		t.Errorf("name = %q, want test-skill", sk.Name)
	}
	if sk.Description != "A test skill" {
		t.Errorf("description = %q, want 'A test skill'", sk.Description)
	}
	if sk.Category != "dev" {
		t.Errorf("category = %q, want dev", sk.Category)
	}
	if sk.Agent != "claude" {
		t.Errorf("agent = %q, want claude", sk.Agent)
	}
	if sk.Tags != `["dev","test"]` {
		t.Errorf("tags = %q, want [\"dev\",\"test\"]", sk.Tags)
	}
	if sk.Content != "# content" {
		t.Errorf("content = %q, want '# content'", sk.Content)
	}
	if sk.SHA256 != "abc123" {
		t.Errorf("sha256 = %q, want abc123", sk.SHA256)
	}
	if sk.Publisher != "wingthing" {
		t.Errorf("publisher = %q, want wingthing", sk.Publisher)
	}
}

func TestGetSkillNotFound(t *testing.T) {
	s := testStore(t)

	sk, err := s.GetSkill("nonexistent")
	if err != nil {
		t.Fatalf("get skill: %v", err)
	}
	if sk != nil {
		t.Errorf("expected nil, got %+v", sk)
	}
}

func TestCreateSkillUpsert(t *testing.T) {
	s := testStore(t)

	if err := s.CreateSkill("upsert", "v1", "dev", "", "[]", "body1", "hash1"); err != nil {
		t.Fatalf("create v1: %v", err)
	}
	if err := s.CreateSkill("upsert", "v2", "code", "", "[]", "body2", "hash2"); err != nil {
		t.Fatalf("create v2: %v", err)
	}

	sk, err := s.GetSkill("upsert")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if sk.Description != "v2" {
		t.Errorf("description = %q, want v2", sk.Description)
	}
	if sk.Category != "code" {
		t.Errorf("category = %q, want code", sk.Category)
	}
	if sk.Content != "body2" {
		t.Errorf("content = %q, want body2", sk.Content)
	}
}

func TestListSkills(t *testing.T) {
	s := testStore(t)

	s.CreateSkill("alpha", "A", "dev", "", "[]", "a", "h1")
	s.CreateSkill("beta", "B", "ops", "", "[]", "b", "h2")
	s.CreateSkill("gamma", "C", "dev", "", "[]", "c", "h3")

	// List all
	all, err := s.ListSkills("")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("list all count = %d, want 3", len(all))
	}

	// List by category
	devSkills, err := s.ListSkills("dev")
	if err != nil {
		t.Fatalf("list dev: %v", err)
	}
	if len(devSkills) != 2 {
		t.Errorf("list dev count = %d, want 2", len(devSkills))
	}
	for _, sk := range devSkills {
		if sk.Category != "dev" {
			t.Errorf("expected category dev, got %q", sk.Category)
		}
	}

	// List empty category
	none, err := s.ListSkills("personal")
	if err != nil {
		t.Fatalf("list personal: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("list personal count = %d, want 0", len(none))
	}
}

func TestSearchSkills(t *testing.T) {
	s := testStore(t)

	s.CreateSkill("jira-briefing", "Brief me on Jira", "dev", "", "[]", "body", "h1")
	s.CreateSkill("deploy-check", "Check deploy status", "ops", "", "[]", "body", "h2")
	s.CreateSkill("blog-draft", "Draft a blog post", "writing", "", "[]", "body", "h3")

	// Search by name
	results, err := s.SearchSkills("jira")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("search jira count = %d, want 1", len(results))
	}
	if len(results) > 0 && results[0].Name != "jira-briefing" {
		t.Errorf("search result = %q, want jira-briefing", results[0].Name)
	}

	// Search by description
	results, err = s.SearchSkills("deploy")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("search deploy count = %d, want 1", len(results))
	}

	// Search no match
	results, err = s.SearchSkills("zzzznothing")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("search nothing count = %d, want 0", len(results))
	}
}

func testServerWithStore(t *testing.T, store *RelayStore) *httptest.Server {
	t.Helper()
	srv := NewServer(store)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { ts.Close() })
	return ts
}

func TestHandlerListSkills(t *testing.T) {
	store := testStore(t)
	store.CreateSkill("s1", "Skill 1", "dev", "", "[]", "body1", "h1")
	store.CreateSkill("s2", "Skill 2", "ops", "", "[]", "body2", "h2")

	ts := testServerWithStore(t, store)

	resp, err := http.Get(ts.URL + "/skills")
	if err != nil {
		t.Fatalf("GET /skills: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var skills []map[string]any
	json.NewDecoder(resp.Body).Decode(&skills)
	if len(skills) != 2 {
		t.Errorf("skills count = %d, want 2", len(skills))
	}

	// Verify no content field in list response
	for _, sk := range skills {
		if _, ok := sk["content"]; ok {
			t.Error("list response should not include content")
		}
	}
}

func TestHandlerListSkillsCategory(t *testing.T) {
	store := testStore(t)
	store.CreateSkill("s1", "Skill 1", "dev", "", "[]", "body1", "h1")
	store.CreateSkill("s2", "Skill 2", "ops", "", "[]", "body2", "h2")
	store.CreateSkill("s3", "Skill 3", "dev", "", "[]", "body3", "h3")

	ts := testServerWithStore(t, store)

	resp, err := http.Get(ts.URL + "/skills?category=dev")
	if err != nil {
		t.Fatalf("GET /skills?category=dev: %v", err)
	}
	defer resp.Body.Close()

	var skills []map[string]any
	json.NewDecoder(resp.Body).Decode(&skills)
	if len(skills) != 2 {
		t.Errorf("skills count = %d, want 2", len(skills))
	}
}

func TestHandlerGetSkill(t *testing.T) {
	store := testStore(t)
	store.CreateSkill("my-skill", "desc", "dev", "claude", `["dev"]`, "# content here", "sha1")

	ts := testServerWithStore(t, store)

	resp, err := http.Get(ts.URL + "/skills/my-skill")
	if err != nil {
		t.Fatalf("GET /skills/my-skill: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var sk map[string]any
	json.NewDecoder(resp.Body).Decode(&sk)
	if sk["name"] != "my-skill" {
		t.Errorf("name = %v, want my-skill", sk["name"])
	}
	if sk["content"] != "# content here" {
		t.Errorf("content = %v, want '# content here'", sk["content"])
	}
}

func TestHandlerGetSkillNotFound(t *testing.T) {
	store := testStore(t)
	ts := testServerWithStore(t, store)

	resp, err := http.Get(ts.URL + "/skills/nonexistent")
	if err != nil {
		t.Fatalf("GET /skills/nonexistent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandlerGetSkillRaw(t *testing.T) {
	store := testStore(t)
	store.CreateSkill("raw-skill", "desc", "dev", "", "[]", "---\nname: raw-skill\n---\nbody here", "sha1")

	ts := testServerWithStore(t, store)

	resp, err := http.Get(ts.URL + "/skills/raw-skill/raw")
	if err != nil {
		t.Fatalf("GET /skills/raw-skill/raw: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "text/markdown; charset=utf-8" {
		t.Errorf("content-type = %q, want text/markdown; charset=utf-8", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "---\nname: raw-skill\n---\nbody here" {
		t.Errorf("body = %q, want raw markdown", string(body))
	}
}

func TestHandlerSearchSkills(t *testing.T) {
	store := testStore(t)
	store.CreateSkill("jira-briefing", "Brief on Jira", "dev", "", "[]", "body", "h1")
	store.CreateSkill("deploy-check", "Check deploy", "ops", "", "[]", "body", "h2")

	ts := testServerWithStore(t, store)

	resp, err := http.Get(ts.URL + "/skills?q=jira")
	if err != nil {
		t.Fatalf("GET /skills?q=jira: %v", err)
	}
	defer resp.Body.Close()

	var skills []map[string]any
	json.NewDecoder(resp.Body).Decode(&skills)
	if len(skills) != 1 {
		t.Errorf("search count = %d, want 1", len(skills))
	}
}

func TestSeedDefaultSkills(t *testing.T) {
	store := testStore(t)

	if err := SeedDefaultSkills(store); err != nil {
		t.Fatalf("seed: %v", err)
	}

	all, err := store.ListSkills("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) < 100 {
		t.Errorf("seeded count = %d, want >= 100", len(all))
	}
	seedCount := len(all)

	// Seed again should be idempotent
	if err := SeedDefaultSkills(store); err != nil {
		t.Fatalf("seed again: %v", err)
	}

	all2, err := store.ListSkills("")
	if err != nil {
		t.Fatalf("list after reseed: %v", err)
	}
	if len(all2) != seedCount {
		t.Errorf("reseed count = %d, want %d", len(all2), seedCount)
	}
}
