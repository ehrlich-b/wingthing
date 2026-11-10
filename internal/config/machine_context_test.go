package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestContextMachineScalarPreservesServiceMapping(t *testing.T) {
	for _, raw := range []string{"context: home\nphone_fleet_exclude_contexts: [work]\n", "context:\n  url: https://context.example\n  client_id: wing\n  secret_file: /private/secret\n", "machine_context: home\ncontext:\n  url: https://context.example\n  client_id: wing\n  secret_file: /private/secret\n  scopes: [tools]\nphone_fleet_exclude_contexts: [work]\n"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "wing.yaml"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadWingConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(raw, "machine_context:") && (cfg.MachineContext != "home" || cfg.Context == nil || cfg.Context.URL != "https://context.example" || len(cfg.Context.Scopes) != 1) {
			t.Fatalf("machine and service contexts must coexist: %+v", cfg)
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
		if again.MachineContext != cfg.MachineContext || !reflect.DeepEqual(again.Context, cfg.Context) {
			t.Fatal("roundtrip lost scalar or service mapping")
		}
	}
	for _, raw := range []string{"context: \"home\\nforged\"", "context: 123", "context: home\ncontext: work", "context: home\nmachine_context: work", "machine_context: [home]", "machine_context: 123"} {
		var cfg WingConfig
		if err := yaml.Unmarshal([]byte(raw), &cfg); err == nil {
			t.Fatalf("accepted invalid context: %s", raw)
		}
	}
}
