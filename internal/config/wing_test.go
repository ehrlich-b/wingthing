package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSaveWingConfigRestrictsSigningKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wing.yaml")
	if err := os.WriteFile(path, []byte("wing_id: old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SaveWingConfig(dir, &WingConfig{WingID: "wing-1", JWTKey: "private-key"}); err != nil {
		t.Fatalf("SaveWingConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("wing.yaml mode = %o, want 600", got)
	}
}

func TestSaveWingConfigAtomicallyReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(target, []byte("do not overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wing.yaml")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	if err := SaveWingConfig(dir, &WingConfig{WingID: "new-wing"}); err != nil {
		t.Fatalf("SaveWingConfig: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "do not overwrite" {
		t.Fatalf("symlink target changed: data=%q err=%v", data, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("wing.yaml remained a symlink")
	}
	cfg, err := LoadWingConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WingID != "new-wing" {
		t.Fatalf("wing ID = %q, want new-wing", cfg.WingID)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".wing.yaml-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary config files remain: %v", matches)
	}
}

func TestSaveWingConfigReportsDirectoryCreationFailure(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveWingConfig(filepath.Join(blocker, "child"), &WingConfig{}); err == nil {
		t.Fatal("SaveWingConfig unexpectedly ignored directory creation failure")
	}
}

func TestLoadWingConfigDirectMCPIsAdditiveAndStrict(t *testing.T) {
	t.Run("legacy config keeps implicit compatibility policy", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte("wing_id: legacy\npaths: [~/repos]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadWingConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.DirectMCP != nil {
			t.Fatalf("legacy config unexpectedly gained direct_mcp: %#v", cfg.DirectMCP)
		}
	})

	t.Run("valid restrictions load without changing old fields", func(t *testing.T) {
		dir := t.TempDir()
		body := `wing_id: current
label: office
direct_mcp:
  allow_grants: [capabilities.read, terminal.read, terminal.read]
  max_sessions: 4
  max_spawns_per_hour: 20
`
		if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadWingConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Label != "office" || cfg.DirectMCP == nil {
			t.Fatalf("loaded config = %#v", cfg)
		}
		if got := cfg.DirectMCP.AllowGrants; len(got) != 2 || got[0] != "capabilities.read" || got[1] != "terminal.read" {
			t.Fatalf("normalized allow grants = %#v", got)
		}
	})

	for name, direct := range map[string]string{
		"unknown field":       "  surprise: true\n",
		"allow and deny":      "  allow_grants: [terminal.read]\n  deny_grants: [terminal.stop]\n",
		"negative sessions":   "  max_sessions: -1\n",
		"negative spawn rate": "  max_spawns_per_hour: -1\n",
		"empty grant":         "  allow_grants: ['']\n",
	} {
		t.Run(name+" fails closed", func(t *testing.T) {
			dir := t.TempDir()
			body := "direct_mcp:\n" + direct
			if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadWingConfig(dir); err == nil {
				t.Fatalf("LoadWingConfig accepted:\n%s", body)
			}
		})
	}
}

func TestLoadWingConfigHostedRelayIsAdditiveAndStrict(t *testing.T) {
	t.Run("legacy config keeps hosted relay compatibility", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte("wing_id: legacy\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadWingConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.HostedRelay != "" || !cfg.HostedRelayAllowed() {
			t.Fatalf("legacy hosted relay policy = %q allowed=%v", cfg.HostedRelay, cfg.HostedRelayAllowed())
		}
		if got := cfg.EffectiveHostedRelay(); got != HostedRelayAllow {
			t.Fatalf("legacy effective policy = %q, want %q", got, HostedRelayAllow)
		}
	})

	for _, policy := range []string{HostedRelayAllow, HostedRelayDeny} {
		t.Run(policy+" loads", func(t *testing.T) {
			dir := t.TempDir()
			body := "hosted_relay: " + policy + "\n"
			if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadWingConfig(dir)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.EffectiveHostedRelay() != policy {
				t.Fatalf("effective policy = %q, want %q", cfg.EffectiveHostedRelay(), policy)
			}
		})
	}

	t.Run("unknown policy fails closed", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte("hosted_relay: sometimes\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWingConfig(dir); err == nil {
			t.Fatal("LoadWingConfig accepted an unknown hosted_relay policy")
		}
	})
}

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
		{Path: "~/docs"}, // open
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

func TestWingConfigIdleTimeout(t *testing.T) {
	input := `
wing_id: abc123
idle_timeout: 4h
`
	var cfg WingConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.IdleTimeout != "4h" {
		t.Errorf("IdleTimeout = %q, want %q", cfg.IdleTimeout, "4h")
	}
}

func TestWingConfigIdleTimeoutEmpty(t *testing.T) {
	input := `
wing_id: abc123
`
	var cfg WingConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.IdleTimeout != "" {
		t.Errorf("IdleTimeout = %q, want empty", cfg.IdleTimeout)
	}
}

func TestWingConfigIdleTimeoutRoundtrip(t *testing.T) {
	cfg := WingConfig{
		WingID:      "abc123",
		IdleTimeout: "30m",
	}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var cfg2 WingConfig
	if err := yaml.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("unmarshal roundtrip: %v", err)
	}
	if cfg2.IdleTimeout != "30m" {
		t.Errorf("roundtrip IdleTimeout = %q, want %q", cfg2.IdleTimeout, "30m")
	}
}

func containsHelper(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestWingConfigCloneIsDeep(t *testing.T) {
	original := &WingConfig{
		Labels:    []string{"one"},
		AllowKeys: []AllowKey{{UserID: "user-one"}},
		Admins:    []string{"admin@example.com"},
		Paths:     PathList{{Path: "/work", Members: []string{"member@example.com"}}},
		ICEServers: []ICEServer{{
			URLs: []string{"stun:one.example"},
		}},
		DirectMCP: &DirectMCPConfig{AllowGrants: []string{"terminal.read"}},
		MCP: &MCPConfig{Roles: map[string]*MCPRoleConfig{
			"member": {Allow: []string{"read"}, Members: []string{"member@example.com"}},
		}},
	}
	clone := original.Clone()
	clone.Labels[0] = "two"
	clone.AllowKeys[0].UserID = "user-two"
	clone.Admins[0] = "other@example.com"
	clone.Paths[0].Members[0] = "other@example.com"
	clone.ICEServers[0].URLs[0] = "stun:two.example"
	clone.DirectMCP.AllowGrants[0] = "terminal.start"
	clone.MCP.Roles["member"].Allow[0] = "write"
	clone.MCP.Roles["member"].Members[0] = "other@example.com"

	if original.Labels[0] != "one" || original.AllowKeys[0].UserID != "user-one" ||
		original.Admins[0] != "admin@example.com" || original.Paths[0].Members[0] != "member@example.com" ||
		original.ICEServers[0].URLs[0] != "stun:one.example" || original.DirectMCP.AllowGrants[0] != "terminal.read" ||
		original.MCP.Roles["member"].Allow[0] != "read" || original.MCP.Roles["member"].Members[0] != "member@example.com" {
		t.Fatalf("clone mutation changed original: %#v", original)
	}
	if (*WingConfig)(nil).Clone() != nil {
		t.Fatal("nil config clone was not nil")
	}
}
