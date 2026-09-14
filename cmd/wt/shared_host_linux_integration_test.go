//go:build linux && integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
		return
	}
	if filepath.Base(os.Args[0]) == "claude" {
		os.Exit(runSharedHostFixtureAgent(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func TestSharedHostAgentRunPreservesExternalReadOnlyMount(t *testing.T) {
	const providerKey = "shared-provider-key-canary"
	root := t.TempDir()
	hostHome := filepath.Join(root, "host-home")
	t.Setenv("HOME", hostHome)
	writePolicyFixture(t, filepath.Join(hostHome, ".claude", "settings.json"), `{"model":"claude-sonnet-5","env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh","HOST_SECRET":"must-not-cross"},"theme":"host-theme"}`)
	workspace := filepath.Join(root, "workspace")
	repos := filepath.Join(root, "repos")
	otherRole := filepath.Join(root, "other-role")
	stateDir := filepath.Join(root, "wingthing-state")
	userHome := filepath.Join(stateDir, "user-homes", "fixture-user")
	otherUserHome := filepath.Join(stateDir, "user-homes", "other-user")
	for _, path := range []string{workspace, repos, otherRole, filepath.Join(userHome, ".claude"), filepath.Join(otherUserHome, ".claude"), filepath.Join(stateDir, "memory")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repos, "source.txt"), []byte("source-visible"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repos, filepath.Join(workspace, "repos")); err != nil {
		t.Fatal(err)
	}
	otherRoleSecret := filepath.Join(otherRole, "secret")
	if err := os.WriteFile(otherRoleSecret, []byte("other-role-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	eggConfigPath := filepath.Join(workspace, "egg.yaml")
	eggConfig := fmt.Sprintf("base: none\nfs:\n  - deny:/\n  - rw:%s\n  - ro:%s\n  - deny-write:%s\n", workspace, repos, eggConfigPath)
	if err := os.WriteFile(eggConfigPath, []byte(eggConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.md", "identity.md"} {
		if err := os.WriteFile(filepath.Join(stateDir, "memory", name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(userHome, ".claude", "credentials-fixture"), []byte("personal-login-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePolicyFixture(t, filepath.Join(userHome, ".claude", "settings.json"), `{"model":"opus","theme":"user-theme"}`)
	secretPath := filepath.Join(otherUserHome, ".claude", "credentials-fixture")
	if err := os.WriteFile(secretPath, []byte("other-user-login-state"), 0o600); err != nil {
		t.Fatal(err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixtureBinDir := filepath.Join(root, "fixture-bin")
	if err := os.MkdirAll(fixtureBinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(fixtureBinDir, "claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fixtureBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WT_SHARED_HOST_SECRET", "must-not-cross-the-boundary")
	t.Setenv("ANTHROPIC_API_KEY", providerKey)

	cfg := &config.Config{Dir: stateDir, DefaultAgent: "claude", WingID: "fixture-wing"}
	taskStore, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer taskStore.Close()

	task := &store.Task{
		ID:        "shared-host-live-jail",
		Type:      "prompt",
		What:      fmt.Sprintf("SHARED_HOST_FIXTURE workspace=%s repos=%s secret=%s other_role_secret=%s egg_config=%s", workspace, repos, secretPath, otherRoleSecret, eggConfigPath),
		RunAt:     time.Now(),
		Agent:     "claude",
		Isolation: "standard",
		CWD:       workspace,
		Principal: "fixture-user",
	}
	if err := taskStore.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err = runTaskToWithOptions(context.Background(), cfg, taskStore, task, &output, taskRunOptions{
		UserHome:     userHome,
		SharedHost:   true,
		AllowedPaths: []string{workspace},
	})
	if err != nil {
		t.Fatalf("shared-host run failed: %v\n%s", err, output.String())
	}
	if strings.TrimSpace(output.String()) != "sealed" {
		t.Fatalf("shared-host agent output = %q, want sealed", output.String())
	}
	var saved map[string]any
	settingsData, err := os.ReadFile(filepath.Join(userHome, ".claude", "settings.json"))
	if err != nil || json.Unmarshal(settingsData, &saved) != nil || saved["model"] != "opus" || saved["theme"] != "user-theme" {
		t.Fatalf("shared run replaced user preferences: %s, %v", settingsData, err)
	}
	marker, err := os.ReadFile(filepath.Join(workspace, "agent-wrote-here"))
	if err != nil {
		t.Fatalf("agent could not write its workspace: %v", err)
	}
	if string(marker) != "workspace-visible" {
		t.Fatalf("workspace marker = %q", marker)
	}
	if data, err := os.ReadFile(filepath.Join(repos, "source.txt")); err != nil || string(data) != "source-visible" {
		t.Fatalf("read-only source changed: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(repos, "agent-write")); !os.IsNotExist(err) {
		t.Fatalf("agent wrote through read-only repo mount: %v", err)
	}
	if data, err := os.ReadFile(eggConfigPath); err != nil || string(data) != eggConfig {
		t.Fatalf("agent changed egg security policy: %q, %v", data, err)
	}
	helper, err := os.ReadFile(filepath.Join(userHome, ".anthropic_key"))
	if err != nil || string(helper) != providerKey {
		t.Fatalf("shared provider helper = %q, err=%v", helper, err)
	}
	stored, err := taskStore.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.Status != "done" || stored.Output == nil || *stored.Output != "sealed" {
		t.Fatalf("stored task = %#v", stored)
	}
}

func TestSharedAgentRuntimeRejectsSymlinkedPersistentState(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".local"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".local", "bin")); err != nil {
		t.Fatal(err)
	}
	if err := prepareSharedAgentHome(home, []string{filepath.Join(".local", "bin")}); err == nil {
		t.Fatal("shared agent home accepted a symlinked runtime directory")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := installSharedAgentBinary(executable, home, "claude"); err == nil {
		t.Fatal("shared runtime installer followed a symlinked destination directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "claude")); !os.IsNotExist(err) {
		t.Fatalf("runtime installer wrote outside the persistent agent home: %v", err)
	}
}

func TestPrepareSharedAgentHomeCreatesOwnerOnlyParentTree(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state", "user-homes", "new-user")
	if err := prepareSharedAgentHome(home, []string{filepath.Join(".local", "bin")}); err != nil {
		t.Fatalf("prepare new shared agent home: %v", err)
	}
	for _, path := range []string{home, filepath.Join(home, ".local"), filepath.Join(home, ".local", "bin")} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			t.Fatalf("shared-home path %s has unsafe mode %v", path, info.Mode())
		}
	}
}

func runSharedHostFixtureAgent(args []string) int {
	policyOK := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--settings" {
			policyOK = args[i+1] == `{"env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh"}}`
		}
	}
	const providerKey = "shared-provider-key-canary"
	prompt := argumentValue(args, "-p")
	workspace := promptFixtureValue(prompt, "workspace")
	repos := promptFixtureValue(prompt, "repos")
	secretPath := promptFixtureValue(prompt, "secret")
	otherRoleSecret := promptFixtureValue(prompt, "other_role_secret")
	eggConfig := promptFixtureValue(prompt, "egg_config")
	result := "sealed"
	if workspace == "" || repos == "" || secretPath == "" || otherRoleSecret == "" || eggConfig == "" {
		result = "fixture-input-missing"
	} else {
		if !policyOK || argumentValue(args, "--model") != "claude-sonnet-5" {
			result = "model-policy-missing-or-leaked"
		}
		ownLogin, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude", "credentials-fixture"))
		if err != nil || string(ownLogin) != "personal-login-state" {
			result = "personal-login-missing"
		}
		if _, err := os.ReadFile(secretPath); err == nil {
			result = "filesystem-leaked"
		}
		if _, err := os.ReadFile(otherRoleSecret); err == nil {
			result = "other-role-leaked"
		}
		source, err := os.ReadFile(filepath.Join(workspace, "repos", "source.txt"))
		if err != nil || string(source) != "source-visible" {
			result = "external-read-only-mount-missing"
		}
		if err := os.WriteFile(filepath.Join(repos, "agent-write"), []byte("must-fail"), 0o600); err == nil {
			result = "external-read-only-mount-writable"
		}
		if err := os.WriteFile(eggConfig, []byte("must-fail"), 0o600); err == nil {
			result = "egg-security-policy-writable"
		}
		if os.Getenv("WT_SHARED_HOST_SECRET") != "" {
			result = "environment-leaked"
		}
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			result = "provider-environment-leaked"
		}
		settingsData, settingsErr := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude", "settings.json"))
		var settings map[string]any
		if settingsErr != nil || json.Unmarshal(settingsData, &settings) != nil {
			result = "provider-helper-settings-missing"
		} else {
			helper, _ := settings["apiKeyHelper"].(string)
			wantHelper := "cat " + filepath.Join(os.Getenv("HOME"), ".anthropic_key")
			key, keyErr := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".anthropic_key"))
			if helper != wantHelper || keyErr != nil || string(key) != providerKey {
				result = "provider-helper-unusable"
			}
		}
		if err := os.WriteFile(filepath.Join(workspace, "agent-wrote-here"), []byte("workspace-visible"), 0o600); err != nil {
			result = "workspace-read-only"
		}
	}
	event := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]string{{"type": "text", "text": result}},
		},
	}
	if err := json.NewEncoder(os.Stdout).Encode(event); err != nil {
		return 1
	}
	return 0
}

func argumentValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func promptFixtureValue(prompt, name string) string {
	prefix := name + "="
	for _, field := range strings.Fields(prompt) {
		if strings.HasPrefix(field, prefix) {
			return strings.TrimPrefix(field, prefix)
		}
	}
	return ""
}
