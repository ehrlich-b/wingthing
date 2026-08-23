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

func TestSharedHostAgentRunUsesSealedJail(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	stateDir := filepath.Join(root, "wingthing-state")
	userHome := filepath.Join(stateDir, "user-homes", "fixture-user")
	otherUserHome := filepath.Join(stateDir, "user-homes", "other-user")
	for _, path := range []string{workspace, filepath.Join(userHome, ".claude"), filepath.Join(otherUserHome, ".claude"), filepath.Join(stateDir, "memory")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"index.md", "identity.md"} {
		if err := os.WriteFile(filepath.Join(stateDir, "memory", name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(userHome, ".claude", "credentials-fixture"), []byte("personal-login-state"), 0o600); err != nil {
		t.Fatal(err)
	}
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

	cfg := &config.Config{Dir: stateDir, DefaultAgent: "claude", WingID: "fixture-wing"}
	taskStore, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer taskStore.Close()

	task := &store.Task{
		ID:        "shared-host-live-jail",
		Type:      "prompt",
		What:      fmt.Sprintf("SHARED_HOST_FIXTURE workspace=%s secret=%s", workspace, secretPath),
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
	marker, err := os.ReadFile(filepath.Join(workspace, "agent-wrote-here"))
	if err != nil {
		t.Fatalf("agent could not write its workspace: %v", err)
	}
	if string(marker) != "workspace-visible" {
		t.Fatalf("workspace marker = %q", marker)
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
	prompt := argumentValue(args, "-p")
	workspace := promptFixtureValue(prompt, "workspace")
	secretPath := promptFixtureValue(prompt, "secret")
	result := "sealed"
	if workspace == "" || secretPath == "" {
		result = "fixture-input-missing"
	} else {
		ownLogin, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude", "credentials-fixture"))
		if err != nil || string(ownLogin) != "personal-login-state" {
			result = "personal-login-missing"
		}
		if _, err := os.ReadFile(secretPath); err == nil {
			result = "filesystem-leaked"
		}
		if os.Getenv("WT_SHARED_HOST_SECRET") != "" {
			result = "environment-leaked"
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
