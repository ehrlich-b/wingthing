package main

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestReusableLoginForExplicitRoostVerifiesItsAuthority(t *testing.T) {
	store := auth.NewTokenStore(t.TempDir())
	existing := &auth.DeviceToken{Token: "current-token"}

	called := false
	reusable, err := reusableLoginForTarget(store, existing, "https://private.example/", func(target, token string) error {
		called = true
		if target != "https://private.example" || token != existing.Token {
			t.Fatalf("validation target/token = %q/%q", target, token)
		}
		return nil
	})
	if err != nil || !reusable || !called {
		t.Fatalf("accepted explicit login: reusable=%v called=%v err=%v", reusable, called, err)
	}

	reusable, err = reusableLoginForTarget(store, existing, "https://other.example", func(string, string) error {
		return auth.ErrAuthFailed
	})
	if err != nil || reusable {
		t.Fatalf("foreign login: reusable=%v err=%v", reusable, err)
	}

	sentinel := errors.New("network down")
	if reusable, err = reusableLoginForTarget(store, existing, "https://offline.example", func(string, string) error {
		return sentinel
	}); reusable || !errors.Is(err, sentinel) {
		t.Fatalf("unverifiable explicit login: reusable=%v err=%v", reusable, err)
	}
}

func TestReusableLoginKeepsCompatibleImplicitAndExpiredBehavior(t *testing.T) {
	store := auth.NewTokenStore(t.TempDir())
	called := false
	validate := func(string, string) error {
		called = true
		return nil
	}
	if reusable, err := reusableLoginForTarget(store, &auth.DeviceToken{Token: "current-token"}, "", validate); err != nil || !reusable || called {
		t.Fatalf("implicit login: reusable=%v called=%v err=%v", reusable, called, err)
	}
	expired := &auth.DeviceToken{Token: "expired", ExpiresAt: time.Now().Add(-time.Minute).Unix()}
	if reusable, err := reusableLoginForTarget(store, expired, "https://new.example", validate); err != nil || reusable || called {
		t.Fatalf("expired login: reusable=%v called=%v err=%v", reusable, called, err)
	}
}

func TestRootHelpLeadsWithAgentManager(t *testing.T) {
	root := newRootCommand()
	if !strings.Contains(strings.ToLower(root.Short), "agent manager") ||
		!strings.Contains(strings.ToLower(root.Long), "agent manager") {
		t.Fatalf("root help does not lead with the product's agent-manager role: short=%q long=%q", root.Short, root.Long)
	}
}

func TestNewRuntimeIDHasSixtyFourBitsOfReadableEntropy(t *testing.T) {
	want := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := newRuntimeID()
		if !want.MatchString(id) {
			t.Fatalf("runtime ID %q is not 16 lowercase hex characters", id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate runtime ID %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestRunTaskPersistsFailureFromEveryEarlyExit(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "claude", WingID: "test-wing"}
	taskStore, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	task := &store.Task{
		ID:        "early-failure",
		Type:      "prompt",
		What:      "must not run",
		RunAt:     time.Now(),
		Agent:     "claude",
		Isolation: "privileged",
		CWD:       t.TempDir(),
	}
	if err := taskStore.CreateTask(task); err != nil {
		t.Fatal(err)
	}

	err = runTaskToWithOptions(context.Background(), cfg, taskStore, task, io.Discard, taskRunOptions{SharedHost: true})
	if err == nil {
		t.Fatal("expected shared-host task to fail closed")
	}
	stored, getErr := taskStore.GetTask(task.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored == nil || stored.Status != "failed" || stored.Error == nil || *stored.Error == "" {
		t.Fatalf("failed task state was not persisted: %#v", stored)
	}
}
