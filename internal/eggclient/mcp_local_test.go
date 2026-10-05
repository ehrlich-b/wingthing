package eggclient

import (
	"path/filepath"
	"reflect"

	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"

	"github.com/ehrlich-b/wingthing/internal/egg"
)

// Provider disabling flags must survive both separated and equals argv forms.
// NUL is rejected before spawn; empty values are preserved literally.
func TestAgentStartArgsAreValidated(t *testing.T) {
	tests := map[string][]string{
		"NUL byte": {"--model\x00sonnet"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAgentArgs(args); err == nil {
				t.Fatalf("accepted %q", args)
			}
		})
	}
	for _, args := range [][]string{
		nil,
		{"--model", "claude-opus-5-5", "--tools", "", "--setting-sources", ""},
		{"--model=claude-opus-5-5", "--tools=", "--setting-sources="},
		{" \t "},
	} {
		if err := ValidateAgentArgs(args); err != nil {
			t.Fatalf("rejected literal argv %q: %v", args, err)
		}
	}
}

func TestSharedHostFilesystemPolicyPreservesAdministratorConfig(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	readOnlySource := t.TempDir()
	writableCache := t.TempDir()
	deniedSecret := t.TempDir()
	cfg := &config.Config{Dir: stateDir}
	source := &egg.EggConfig{
		FS: []string{
			"deny:/",
			"rw:" + workspace,
			"ro:" + readOnlySource,
			"rw:" + writableCache,
			"deny:" + deniedSecret,
			"deny-write:" + filepath.Join(workspace, "egg.yaml"),
		},
		AgentSettings: map[string]string{"claude": "/host/secret/settings.json"},
	}
	sealed, err := sealedSharedHostEggConfig(cfg, source, workspace, []string{workspace})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sealed.FS, source.FS) {
		t.Fatalf("sealed fs = %#v, want administrator policy %#v", sealed.FS, source.FS)
	}
	if !reflect.DeepEqual(sealed.AgentSettings, source.AgentSettings) {
		t.Fatalf("agent settings = %#v, want %#v", sealed.AgentSettings, source.AgentSettings)
	}
	sealed.FS[0] = "mutated"
	sealed.AgentSettings["claude"] = "mutated"
	if source.FS[0] != "deny:/" || source.AgentSettings["claude"] != "/host/secret/settings.json" {
		t.Fatal("sealed config aliases the administrator config")
	}
	if _, err := sealedSharedHostEggConfig(cfg, &egg.EggConfig{FS: []string{"rw:" + workspace}}, workspace, []string{workspace}); err == nil || !strings.Contains(err.Error(), "must deny the filesystem root") {
		t.Fatalf("unjailled shared-host policy error = %v", err)
	}
	if _, err := sealedSharedHostEggConfig(cfg, &egg.EggConfig{FS: []string{"deny:/", "ro:/"}}, workspace, []string{workspace}); err == nil || !strings.Contains(err.Error(), "must not mount the filesystem root") {
		t.Fatalf("host-root mount error = %v", err)
	}
	if _, err := ValidateSharedHostWorkspacePaths(cfg, []string{stateDir}); err == nil {
		t.Fatal("Wingthing state was accepted as a shared workspace")
	}
}
