package egg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func codexCaptureServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	root, err := os.MkdirTemp("", "wt-codex-capture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home, dir := filepath.Join(root, "home"), filepath.Join(root, "eggs", "ours")
	for _, path := range []string{home, dir, filepath.Join(home, Profile("codex").SessionDir)} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// This older provider has no hooks, and never contacts a model or account.
	if err := os.WriteFile(filepath.Join(root, "codex"), []byte("#!/bin/sh\nif [ \"$1\" = '--version' ]; then echo 'codex-cli 0.147.0'; fi\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(root, "state"))
	server, err := NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	return server, home, dir
}

func TestCodexHooklessResumeCapturesRecordedThread(t *testing.T) {
	server, home, dir := codexCaptureServer(t)
	sessions := filepath.Join(home, Profile("codex").SessionDir)
	for _, id := range []string{"resumed", "victim"} {
		lifecycleWrite(t, filepath.Join(sessions, "rollout-2026-10-07-"+id+".jsonl"), id+"-message\n")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := server.RunSession(ctx, RunConfig{Agent: "codex", ResumeSessionID: "resumed", CWD: home, UserHome: home, OuterBoundary: true, Network: []string{"*"}, Rows: 24, Cols: 80, OmitBrowserBridge: true}); err != nil {
		t.Fatal(err)
	}
	meta, err := os.ReadFile(filepath.Join(dir, "egg.meta"))
	if err != nil || ParseChatMeta(string(meta))["provider_session_id"] != "resumed" {
		t.Fatalf("validated resume identity was not recorded at launch: %s %v", meta, err)
	}
	chatMeta, err := os.ReadFile(filepath.Join(dir, "chat.meta"))
	if err != nil || ParseChatMeta(string(chatMeta))["agent_session_id"] != "resumed" {
		t.Fatalf("hookless resume lost history capture: %s %v", chatMeta, err)
	}
}

func TestCodexUnboundCaptureUnavailableWithoutGuessing(t *testing.T) {
	dir, home, cwd := t.TempDir(), t.TempDir(), "/fixture"
	sessions := filepath.Join(home, Profile("codex").SessionDir)
	if err := os.MkdirAll(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	lifecycleWrite(t, filepath.Join(dir, "egg.meta"), "agent=codex\nprovider_session_id=\n")
	lifecycleWrite(t, filepath.Join(sessions, "victim.jsonl"), "victim-secret\n")
	if err := CaptureSessionHistory("codex", cwd, dir, home, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "chat.jsonl.gz")); !os.IsNotExist(err) {
		t.Fatalf("unbound capture guessed another thread: %v", err)
	}
	for _, alive := range []bool{true, false} {
		view, err := ReadSessionLifecycle(dir, "codex", cwd, home, "", alive, 0, 200)
		if err != nil || view.ProviderSessionID != "" || !strings.Contains(view.Reason, "capture unavailable") {
			t.Fatalf("unbound status silently lost capture: %+v %v", view, err)
		}
	}
}

func TestCodexResumeRefusesMismatchedLaunchBinding(t *testing.T) {
	server, home, _ := codexCaptureServer(t)
	err := server.RunSession(context.Background(), RunConfig{Agent: "codex", ResumeSessionID: "resumed", ProviderSessionID: "victim", CWD: home, UserHome: home, OuterBoundary: true, Network: []string{"*"}, Rows: 24, Cols: 80, OmitBrowserBridge: true})
	if err == nil || !strings.Contains(err.Error(), "resume ID does not match") {
		t.Fatalf("resume used a different transcript binding: %v", err)
	}
}
