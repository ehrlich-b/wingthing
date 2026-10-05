package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestSessionRenameCLIAndMCPShareStoreLock(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	t.Setenv("WINGTHING_DIR", cfg.Dir)
	workspace := t.TempDir()
	writeActiveRenameFixture(t, cfg, "first", "alice", workspace, "one")
	writeActiveRenameFixture(t, cfg, "second", "alice", workspace, "two")
	lock, err := os.OpenFile(filepath.Join(cfg.Dir, "eggs", ".session-name.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	type result struct {
		path string
		err  error
	}
	done := make(chan result, 2)
	command := sessionRenameCmd()
	command.SetArgs([]string{"first", "shared", "--json"})
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	go func() { done <- result{"CLI", command.Execute()} }()
	server := &localMCPServer{cfg: cfg}
	go func() {
		_, err := server.toolTerminalRename(context.Background(), json.RawMessage(`{"session":"second","name":"shared"}`))
		done <- result{"MCP", err}
	}()
	select {
	case got := <-done:
		t.Fatalf("%s rename bypassed the store lock: %v", got.path, got.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}

	succeeded, rejected := 0, 0
	for range 2 {
		select {
		case got := <-done:
			switch {
			case got.err == nil:
				succeeded++
			case errors.Is(got.err, eggclient.ErrSessionNameInUse):
				rejected++
			default:
				t.Fatalf("%s rename failed: %v", got.path, got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("renames did not finish after the store lock was released")
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("racing renames: %d succeeded, %d rejected; want one of each", succeeded, rejected)
	}
	claims := 0
	for _, id := range []string{"first", "second"} {
		if eggclient.ReadSessionName(filepath.Join(cfg.Dir, "eggs", id)) == "shared" {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("%d sessions durably claimed the shared name, want 1", claims)
	}
}

func TestEggLaunchLabelChecksAvailabilityUnderStoreLock(t *testing.T) {
	state, err := os.MkdirTemp("", "wt-names-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	cfg := &config.Config{Dir: state}
	workspace := t.TempDir()
	writeActiveRenameFixture(t, cfg, "first", "alice", workspace, "one")
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
		// A sealed launch with no policy must fail before spawning a process.
		_, err := eggclient.SpawnEgg(cfg, "new", "claude", nil, 24, 80, workspace, false, false, false, eggclient.EggIdentity{SealedFS: true}, 0, eggclient.SpawnEggOpts{Label: "shared"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("launch label bypassed the store lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := eggclient.WriteSessionName(filepath.Join(state, "eggs", "first"), "shared"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, eggclient.ErrSessionNameInUse) {
			t.Fatalf("launch did not check name availability under the store lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("launch did not finish after the store lock was released")
	}
}

func TestSessionRenameCreatesEggsDirectoryForStoreLock(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	workspace := t.TempDir()
	sessions := []ws.SessionInfo{{SessionID: "first", UserID: "alice", CWD: workspace}}
	err := renameTunnelSession(cfg, ws.TunnelRequest{SenderUserID: "alice"}, "first", "shared", sessions, []string{workspace})
	if err == nil || !strings.Contains(err.Error(), "write session name") {
		t.Fatalf("rename should reach the durable write after creating the lock directory: %v", err)
	}
	info, err := os.Stat(filepath.Join(cfg.Dir, "eggs"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("eggs directory: mode %v, want directory with mode 700", info.Mode())
	}
}

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
	if err := eggclient.WriteSessionName(filepath.Join(cfg.Dir, "eggs", "second"), "shared"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, eggclient.ErrSessionNameInUse) {
			t.Fatalf("rename did not check uniqueness under the store lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rename did not finish after the store lock was released")
	}
	if got := eggclient.ReadSessionName(filepath.Join(cfg.Dir, "eggs", "first")); got != "one" {
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
	if err := eggclient.WriteSessionName(dir, name); err != nil {
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
	if !errors.Is(err, eggclient.ErrSessionNameInUse) {
		t.Fatalf("cross-owner collision error = %v", err)
	}
	if got := err.Error(); got != "session name is already in use" || strings.Contains(got, "bob-private-session") || strings.Contains(got, "shared-name") {
		t.Fatalf("cross-owner collision exposed session information: %q", got)
	}
	if got := eggclient.ReadSessionName(filepath.Join(cfg.Dir, "eggs", "alice-session")); got != "alice-old" {
		t.Fatalf("rejected rename changed requester session name to %q", got)
	}

	if err := renameTunnelSession(cfg, req, "alice-session", "alice-new", sessions, []string{workspace}); err != nil {
		t.Fatalf("owned session rename failed: %v", err)
	}
	if got := eggclient.ReadSessionName(filepath.Join(cfg.Dir, "eggs", "alice-session")); got != "alice-new" {
		t.Fatalf("owned session name = %q, want alice-new", got)
	}
	if got := eggclient.ReadSessionName(filepath.Join(cfg.Dir, "eggs", "bob-private-session")); got != "shared-name" {
		t.Fatalf("other owner's session name changed to %q", got)
	}
}
