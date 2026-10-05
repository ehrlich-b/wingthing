package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestTunnelSessionRenameHoldsStoreLockThroughUniquenessCheck(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	workspace := t.TempDir()
	writeActiveRenameFixture(t, cfg, "first", "alice", workspace, "one")
	writeActiveRenameFixture(t, cfg, "second", "alice", workspace, "two")
	sessions := []ws.SessionInfo{{SessionID: "first", UserID: "alice", CWD: workspace}}
	lock, err := os.OpenFile(filepath.Join(cfg.Dir, "eggs", ".session-name.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- renameTunnelSession(cfg, ws.TunnelRequest{SenderUserID: "alice"}, "first", "shared", sessions, []string{workspace})
	}()
	select {
	case err := <-done:
		t.Fatalf("rename bypassed the store lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writeSessionName(filepath.Join(cfg.Dir, "eggs", "second"), "shared"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errSessionNameInUse) {
			t.Fatalf("rename did not check uniqueness under the store lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rename did not finish after the store lock was released")
	}
	if got := readSessionName(filepath.Join(cfg.Dir, "eggs", "first")); got != "one" {
		t.Fatalf("rejected rename changed the session name: %q", got)
	}
}

func writeActiveRenameFixture(t *testing.T, cfg *config.Config, sessionID, owner, cwd, name string) {
	t.Helper()
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"egg.pid":   strconv.Itoa(os.Getpid()) + "\n",
		"egg.owner": owner + "\n" + owner + "@example.com\n",
		"egg.meta":  "agent=claude\ncwd=" + cwd + "\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeSessionName(dir, name); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelSessionRenameHidesCrossOwnerCollisionAndAllowsOwnedRename(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	workspace := t.TempDir()
	writeActiveRenameFixture(t, cfg, "alice-session", "alice", workspace, "alice-old")
	writeActiveRenameFixture(t, cfg, "bob-private-session", "bob", workspace, "shared-name")

	sessions := []ws.SessionInfo{
		{SessionID: "alice-session", UserID: "alice", CWD: workspace},
		{SessionID: "bob-private-session", UserID: "bob", CWD: workspace},
	}
	req := ws.TunnelRequest{SenderUserID: "alice", SenderOrgRole: "member"}
	err := renameTunnelSession(cfg, req, "alice-session", "shared-name", sessions, []string{workspace})
	if !errors.Is(err, errSessionNameInUse) {
		t.Fatalf("cross-owner collision error = %v", err)
	}
	if got := err.Error(); got != "session name is already in use" || strings.Contains(got, "bob-private-session") || strings.Contains(got, "shared-name") {
		t.Fatalf("cross-owner collision exposed session information: %q", got)
	}
	if got := readSessionName(filepath.Join(cfg.Dir, "eggs", "alice-session")); got != "alice-old" {
		t.Fatalf("rejected rename changed requester session name to %q", got)
	}

	if err := renameTunnelSession(cfg, req, "alice-session", "alice-new", sessions, []string{workspace}); err != nil {
		t.Fatalf("owned session rename failed: %v", err)
	}
	if got := readSessionName(filepath.Join(cfg.Dir, "eggs", "alice-session")); got != "alice-new" {
		t.Fatalf("owned session name = %q, want alice-new", got)
	}
	if got := readSessionName(filepath.Join(cfg.Dir, "eggs", "bob-private-session")); got != "shared-name" {
		t.Fatalf("other owner's session name changed to %q", got)
	}
}
