package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRoostAllowedEmailsFromEnv(t *testing.T) {
	t.Setenv("WT_ROOST_ALLOWED_EMAILS", " Alice@Example.com, bob@example.com,alice@example.com ")
	got, err := roostAllowedEmailsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alice@example.com", "bob@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed emails = %#v, want %#v", got, want)
	}

	for _, invalid := range []string{"not-an-email", "missing@", "@missing", "two@@example.com", "white space@example.com", "alice@example.com,"} {
		t.Setenv("WT_ROOST_ALLOWED_EMAILS", invalid)
		if _, err := roostAllowedEmailsFromEnv(); err == nil {
			t.Fatalf("invalid enrollment email %q accepted", invalid)
		}
	}
}

func TestLoadRoostMCPConfigUsesWingYAMLAndConfiguredToolsDir(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "custom-tools")
	if err := os.Mkdir(toolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "echo.yaml"), []byte("name: echo\nrun: echo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wingYAML := "tools_dir: " + toolsDir + `
mcp:
  enabled: true
  default_allow_all: false
  roles:
    engineering:
      enabled: true
      allow: [echo]
      members: [alice@example.com]
`
	if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte(wingYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	tools, policy, err := loadRoostMCPConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %#v", tools)
	}
	if policy == nil || !policy.AllowedAny([]string{"engineering"}, "echo") {
		t.Fatalf("policy = %#v", policy)
	}
}

func TestJWTKeyFromEnvironmentUsesExistingSecretAndPrefersExplicitKey(t *testing.T) {
	t.Setenv("WT_JWT_KEY", "")
	t.Setenv("WT_JWT_SECRET", "0123456789abcdef-existing-deployment-secret")
	derived, err := jwtKeyFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if derived == "" {
		t.Fatal("WT_JWT_SECRET did not derive a signing key")
	}

	t.Setenv("WT_JWT_KEY", "explicit-key")
	got, err := jwtKeyFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if got != "explicit-key" {
		t.Fatalf("explicit WT_JWT_KEY did not take precedence: %q", got)
	}
}
