package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestContextMachineScalarPreservesServiceMapping(t *testing.T) {
	for _, raw := range []string{"context: home\nphone_fleet_exclude_contexts: [work]\n", "context:\n  url: https://context.example\n  client_id: wing\n  secret_file: /private/secret\n"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadWingConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MachineContext == "" && cfg.Context == nil {
			t.Fatal("lost context")
		}
		clone := cfg.Clone()
		if len(clone.PhoneFleetExcludeContexts) > 0 {
			clone.PhoneFleetExcludeContexts[0] = "changed"
			if cfg.PhoneFleetExcludeContexts[0] != "work" {
				t.Fatal("clone shares exclusion policy")
			}
		}
		if err := SaveWingConfig(dir, cfg); err != nil {
			t.Fatal(err)
		}
		again, err := LoadWingConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if again.MachineContext != cfg.MachineContext || (again.Context == nil) != (cfg.Context == nil) {
			t.Fatal("roundtrip lost scalar or service mapping")
		}
	}
	for _, raw := range []string{"context: \"home\\nforged\"", "context: 123", "context: home\ncontext: work"} {
		var cfg WingConfig
		if err := yaml.Unmarshal([]byte(raw), &cfg); err == nil {
			t.Fatalf("accepted invalid context: %s", raw)
		}
	}
}
