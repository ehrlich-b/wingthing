package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPathListUnmarshalMixed(t *testing.T) {
	input := `
paths:
  - ~/docs
  - path: ~/repos/api
    members: [alice@acme.com, bob@acme.com]
  - path: ~/repos/infra
    members:
      - carol@acme.com
`
	var cfg WingConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Paths) != 3 {
		t.Fatalf("expected 3 paths, got %d", len(cfg.Paths))
	}
	// Plain string
	if cfg.Paths[0].Path != "~/docs" || len(cfg.Paths[0].Members) != 0 {
		t.Errorf("path[0] = %+v", cfg.Paths[0])
	}
	// Mapping with members
	if cfg.Paths[1].Path != "~/repos/api" || len(cfg.Paths[1].Members) != 2 {
		t.Errorf("path[1] = %+v", cfg.Paths[1])
	}
	if cfg.Paths[2].Path != "~/repos/infra" || len(cfg.Paths[2].Members) != 1 {
		t.Errorf("path[2] = %+v", cfg.Paths[2])
	}
}

func TestPathListMarshalRoundtrip(t *testing.T) {
	pl := PathList{
		{Path: "~/docs"},
		{Path: "~/repos/api", Members: []string{"alice@acme.com"}},
	}
	data, err := yaml.Marshal(struct {
		Paths PathList `yaml:"paths"`
	}{Paths: pl})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(data)
	// Plain string entry should NOT have "path:" key
	if !contains(out, "- ~/docs") {
		t.Errorf("expected plain string for ~/docs, got:\n%s", out)
	}
	// Mapping entry should have path + members
	if !contains(out, "path: ~/repos/api") {
		t.Errorf("expected mapping for ~/repos/api, got:\n%s", out)
	}
	if !contains(out, "alice@acme.com") {
		t.Errorf("expected member email, got:\n%s", out)
	}
}

func TestPathListStrings(t *testing.T) {
	pl := PathList{
		{Path: "~/a"},
		{Path: "~/b", Members: []string{"x@y.com"}},
	}
	s := pl.Strings()
	if len(s) != 2 || s[0] != "~/a" || s[1] != "~/b" {
		t.Errorf("Strings() = %v", s)
	}
}

func TestPathsForUser(t *testing.T) {
	pl := PathList{
		{Path: "~/docs"},                                            // open
		{Path: "~/repos/api", Members: []string{"Alice@Acme.com"}}, // ACLed
		{Path: "~/repos/infra", Members: []string{"bob@acme.com"}}, // ACLed
	}

	// Owner sees all
	got := pl.PathsForUser("anyone@x.com", "owner")
	if len(got) != 3 {
		t.Errorf("owner should see all, got %v", got)
	}

	// Admin sees all
	got = pl.PathsForUser("anyone@x.com", "admin")
	if len(got) != 3 {
		t.Errorf("admin should see all, got %v", got)
	}

	// Member alice: open + api
	got = pl.PathsForUser("alice@acme.com", "member")
	if len(got) != 2 {
		t.Errorf("alice should see 2 paths, got %v", got)
	}

	// Case insensitive
	got = pl.PathsForUser("ALICE@ACME.COM", "member")
	if len(got) != 2 {
		t.Errorf("case insensitive: alice should see 2 paths, got %v", got)
	}

	// Unknown member: only open paths
	got = pl.PathsForUser("nobody@x.com", "member")
	if len(got) != 1 || got[0] != "~/docs" {
		t.Errorf("nobody should see only open paths, got %v", got)
	}

	// Empty role treated as member
	got = pl.PathsForUser("alice@acme.com", "")
	if len(got) != 2 {
		t.Errorf("empty role should behave as member, got %v", got)
	}
}

func TestPathListLegacyStringOnly(t *testing.T) {
	input := `
paths:
  - ~/a
  - ~/b
`
	var cfg WingConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Paths) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(cfg.Paths))
	}
	s := cfg.Paths.Strings()
	if s[0] != "~/a" || s[1] != "~/b" {
		t.Errorf("Strings() = %v", s)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
