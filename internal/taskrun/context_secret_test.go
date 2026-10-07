package taskrun

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func contextSecretFixture(t *testing.T) (root string, c *config.ContextConfig) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"home", "work", "private", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(root, "private", "context.secret")
	if err := os.WriteFile(secret, []byte("headless-context-secret-must-stay-private"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "context.secret")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	return root, &config.ContextConfig{URL: "https://context.example", ClientID: "wing", SecretFile: link}
}

func TestHeadlessContextSecretPolicy(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	root, c := contextSecretFixture(t)
	home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
	alias := filepath.Join(work, "alias")
	if err := os.Symlink(filepath.Join(root, "private"), alias); err != nil {
		t.Fatal(err)
	}
	// Agent profile grants also become bind mounts on Linux.
	profileAlias := filepath.Join(home, ".codex")
	if err := os.Symlink(filepath.Join(root, "private"), profileAlias); err != nil {
		t.Fatal(err)
	}
	for _, shared := range []bool{false, true} {
		for _, mode := range []string{"ro", "rw"} {
			t.Run(mode+"/shared="+boolKey(shared), func(t *testing.T) {
				policy := &egg.EggConfig{FS: []string{mode + ":alias"}}
				if shared {
					policy.FS = append(policy.FS, "deny:/", "rw:"+work)
				}
				cfg, err := directAgentSandboxConfigForTask(policy, "codex", "standard", home, work, nil, shared, c)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS == "linux" {
					if !eggclient.ContainsExactPath(cfg.Deny, "/") || !eggclient.ContainsExactPath(cfg.DenyRename, filepath.Join(root, "private")) {
						t.Fatal("Context masks lost shared controller isolation or secret ancestor pins")
					}
				} else if runtime.GOOS == "darwin" && (len(cfg.ControlDenyPaths) == 0 || !cfg.DenyOtherProcessInfo) {
					t.Fatal("Context masks lost shared controller isolation")
				}
				for _, path := range []string{c.SecretFile, filepath.Join(root, "private", "context.secret"), filepath.Join(alias, "context.secret"), filepath.Join(profileAlias, "context.secret")} {
					if !eggclient.ContainsExactPath(cfg.Deny, path) {
						t.Errorf("effective headless policy does not deny %q: %v", path, cfg.Deny)
					}
					if runtime.GOOS == "darwin" && !eggclient.ContainsExactPath(cfg.ProtectedWriteTargets, path) {
						t.Errorf("final write policy does not guard %q", path)
					}
				}
			})
		}
	}
	// Prompt-derived mounts pass through the same alias checks as egg.yaml.
	cfg, err := directAgentSandboxConfigForTask(nil, "custom", "standard", home, work, []string{alias}, false, c)
	if err != nil {
		t.Fatal(err)
	}
	if !eggclient.ContainsExactPath(cfg.Deny, filepath.Join(alias, "context.secret")) {
		t.Fatal("prompt mount exposes the secret through an ancestor alias")
	}
}

func TestHeadlessContextSecretRefusesHardLink(t *testing.T) {
	root, c := contextSecretFixture(t)
	if err := os.Link(filepath.Join(root, "private", "context.secret"), filepath.Join(root, "work", "ordinary")); err != nil {
		t.Fatal(err)
	}
	_, err := directAgentSandboxConfigForTask(nil, "custom", "standard", filepath.Join(root, "home"), filepath.Join(root, "work"), []string{filepath.Join(root, "work")}, false, c)
	if err == nil || !strings.Contains(err.Error(), "hard links") {
		t.Fatalf("workspace link exposes Context secret: %v", err)
	}
}

func TestHeadlessContextSecretRejectsDirectGrant(t *testing.T) {
	root, c := contextSecretFixture(t)
	for _, mode := range []string{"ro:", "rw:"} {
		t.Run(mode, func(t *testing.T) {
			policy := &egg.EggConfig{FS: []string{mode + c.SecretFile}}
			_, err := directAgentSandboxConfigForTask(policy, "custom", "standard", filepath.Join(root, "home"), filepath.Join(root, "work"), nil, false, c)
			if err == nil || !strings.Contains(err.Error(), "exposes protected secret path") {
				t.Fatalf("direct secret grant accepted: %v", err)
			}
		})
	}
	_, err := directAgentSandboxConfigForTask(nil, "custom", "standard", filepath.Join(root, "home"), filepath.Join(root, "work"), []string{c.SecretFile}, false, c)
	if err == nil || !strings.Contains(err.Error(), "exposes protected secret path") {
		t.Fatalf("prompt secret grant accepted: %v", err)
	}
}

func TestRunTaskRefusesUnprotectableContextSecret(t *testing.T) {
	for _, tc := range []struct {
		name, isolation, want string
	}{
		{"privileged", "privileged", "cannot protect secret_file with privileged isolation"},
		{"missing secret", "standard", "cannot resolve secret_file"},
		{"direct grant", "standard", "exposes protected secret path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, c := contextSecretFixture(t)
			home, work := filepath.Join(root, "home"), filepath.Join(root, "work")
			t.Setenv("HOME", home)
			t.Setenv("WT_PROVIDER_BASE_URL", "")
			marker := filepath.Join(work, "launched")
			t.Setenv("WT_CONTEXT_LAUNCH_MARKER", marker)
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nprintf launched > \"$WT_CONTEXT_LAUNCH_MARKER\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if tc.name == "missing secret" {
				c.SecretFile = filepath.Join(root, "missing")
			}
			cfg := &config.Config{Dir: home, DefaultAgent: "claude", WingID: "test-wing"}
			if err := config.SaveWingConfig(cfg.Dir, &config.WingConfig{Context: c}); err != nil {
				t.Fatal(err)
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			task := &store.Task{ID: "context-refusal", Type: "prompt", What: "must not run", RunAt: time.Now(), Agent: "claude", Isolation: tc.isolation, CWD: work}
			if tc.name == "direct grant" {
				task.EggConfigYAML = "base: none\nfs:\n  - ro:" + c.SecretFile + "\n"
			}
			if err := s.CreateTask(task); err != nil {
				t.Fatal(err)
			}
			err = RunTaskToWithOptions(context.Background(), cfg, s, task, io.Discard, TaskRunOptions{UserHome: home})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unprotectable Context configuration was not refused: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("agent ran before refusal: %v", err)
			}
			stored, err := s.GetTask(task.ID)
			if err != nil || stored == nil || stored.Status != "failed" || stored.Error == nil || !strings.Contains(*stored.Error, tc.want) {
				t.Fatalf("Context refusal was not persisted: %#v, %v", stored, err)
			}
		})
	}
}
