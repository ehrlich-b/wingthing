package main

import (
	"bytes"
	"encoding/json"
	"github.com/ehrlich-b/wingthing/internal/onboarding"
	"testing"
)

func TestSetupStructuredDiscovery(t *testing.T) {
	cmd := setupCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var agents []onboarding.Agent
	if err := json.Unmarshal(out.Bytes(), &agents); err != nil || len(agents) != 2 {
		t.Fatalf("structured detection: %s %v", out.String(), err)
	}
}
func TestCodexShortcutKeepsEggPolicyFlags(t *testing.T) {
	cmd := codexCmd()
	for _, flag := range []string{"cwd", "name", "config", "detach", "json", "resume"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("lost flag %s", flag)
		}
	}
	if len(cmd.Commands()) != 0 || len(cmd.Aliases) != 0 {
		t.Fatal("shortcut exposes internal egg commands")
	}
	if err := cmd.ParseFlags([]string{"--config", "policy.yaml", "--unsandboxed"}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.ValidateFlagGroups(); err == nil {
		t.Fatal("weakened mutually exclusive policy flags")
	}
}
