package localrelay

import (
	"github.com/ehrlich-b/wingthing/internal/config"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRoostAllowedEmailsFromEnv(t *testing.T) {
	t.Setenv("WT_ROOST_ALLOWED_EMAILS", " Alice@Example.com, bob@example.com,alice@example.com ")
	got, err := RoostAllowedEmailsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alice@example.com", "bob@example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed emails = %#v, want %#v", got, want)
	}

	for _, invalid := range []string{"not-an-email", "missing@", "@missing", "two@@example.com", "white space@example.com", "alice@example.com,"} {
		t.Setenv("WT_ROOST_ALLOWED_EMAILS", invalid)
		if _, err := RoostAllowedEmailsFromEnv(); err == nil {
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
	derived, err := JwtKeyFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if derived == "" {
		t.Fatal("WT_JWT_SECRET did not derive a signing key")
	}

	t.Setenv("WT_JWT_KEY", "explicit-key")
	got, err := JwtKeyFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if got != "explicit-key" {
		t.Fatalf("explicit WT_JWT_KEY did not take precedence: %q", got)
	}
}

func TestRoostMCPReloadRetainsStartupContext(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	if err := os.Mkdir(toolsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "context.yaml"), []byte("name: tickets\ncontext: jira-search\n"), 0600); err != nil {
		t.Fatal(err)
	}
	initial := &config.ContextConfig{URL: "https://context.example", ClientID: "wing", SecretFile: filepath.Join(dir, "secret")}
	if err := os.WriteFile(initial.SecretFile, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	_, release := config.FreezeContextConfig(dir, initial)
	defer release()
	// Removing enrollment from disk must neither remove valid Context tools nor
	// cause the runner to lose its original client on SIGHUP.
	if err := config.SaveWingConfig(dir, &config.WingConfig{MCP: &config.MCPConfig{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	tools, _, err := loadRoostMCPConfig(dir, initial)
	if err != nil || len(tools) != 1 {
		t.Fatalf("reload lost Context tools: %v", err)
	}
	runner, err := roostToolRunner(dir, tools)
	if err != nil {
		t.Fatal(err)
	}
	response := runner.Call("tickets", nil)
	if !strings.Contains(response.Error, "verified owner email is required") {
		t.Fatalf("runner lost startup Context: %+v", response)
	}
	// Enabling Context on disk cannot enroll a previously unenrolled process.
	otherDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(otherDir, "tools"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "tools", "context.yaml"), []byte("name: tickets\ncontext: jira-search\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveWingConfig(otherDir, &config.WingConfig{Context: initial, MCP: &config.MCPConfig{Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadRoostMCPConfig(otherDir, nil); err == nil {
		t.Fatal("reload enabled Context without restart")
	}
}
