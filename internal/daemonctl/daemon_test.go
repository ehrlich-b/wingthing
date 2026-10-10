package daemonctl

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/procinfo"
)

func TestWingStartupSeparatesRelay401FromDaemonExit(t *testing.T) {
	t.Setenv("WINGTHING_DIR", t.TempDir())
	WriteWingStatusForRoost("auth_failed", "relay rejected authentication (401)", "https://relay.example")
	if state := WaitForWingStatus(os.Getpid(), time.Second); state != "auth_failed" {
		t.Fatalf("live local daemon with relay 401: %s", state)
	}
	if state := WaitForWingStatus(0, time.Second); state != "exited" {
		t.Fatalf("dead daemon with stale relay status: %s", state)
	}
}

func TestDaemonArgvMatchesOnlyExpectedForegroundProcess(t *testing.T) {
	for _, test := range []struct {
		name string
		argv []string
		kind DaemonKind
		want bool
	}{
		{name: "wing", argv: []string{"/usr/local/bin/wt", "wing", "start", "--foreground"}, kind: WingDaemon, want: true},
		{name: "daemon alias", argv: []string{"wt", "daemon", "start", "--roost", "wss://example.test", "--foreground"}, kind: WingDaemon, want: true},
		{name: "roost", argv: []string{"wt", "roost", "start", "--foreground", "--addr", ":8080"}, kind: RoostDaemon, want: true},
		{name: "wrong kind", argv: []string{"wt", "roost", "start", "--foreground"}, kind: WingDaemon},
		{name: "interactive parent", argv: []string{"wt", "wing", "start"}, kind: WingDaemon},
		{name: "wrong command", argv: []string{"wt", "wing", "status", "--foreground"}, kind: WingDaemon},
		{name: "lookalike argument", argv: []string{"sleep", "1", "wing", "start", "--foreground"}, kind: WingDaemon},
		{name: "unknown kind", argv: []string{"wt", "wing", "start", "--foreground"}, kind: DaemonKind("future")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := daemonArgvMatches(test.argv, test.kind); got != test.want {
				t.Fatalf("daemonArgvMatches(%q, %q) = %v, want %v", test.argv, test.kind, got, test.want)
			}
		})
	}
}

func TestParseSavedDaemonArgsValidatesKindAndForeground(t *testing.T) {
	got, err := ParseSavedDaemonArgs([]byte("wing\nstart\n--foreground\n--paths\n/tmp/project\n"), WingDaemon)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "wing|start|--foreground|--paths|/tmp/project" {
		t.Fatalf("saved args = %#v", got)
	}
	for _, invalid := range []struct {
		data string
		kind DaemonKind
	}{
		{data: "", kind: WingDaemon},
		{data: "wing\nstart", kind: WingDaemon},
		{data: "roost\nstart\n--foreground", kind: WingDaemon},
		{data: "update\n--foreground", kind: WingDaemon},
	} {
		if args, err := ParseSavedDaemonArgs([]byte(invalid.data), invalid.kind); err == nil {
			t.Fatalf("invalid saved args accepted: %#v", args)
		}
	}
}

func TestReadPidFromRejectsAndRemovesUnrelatedLiveProcess(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "wing.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if pid, err := ReadPidFrom(pidPath, WingDaemon); err == nil {
		t.Fatalf("unrelated live process accepted as daemon pid %d", pid)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("stale PID file was not removed: %v", err)
	}
}

func TestReadPidFromRejectsAndRemovesMalformedMetadata(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "wing.pid")
	if err := os.WriteFile(pidPath, []byte("not-a-pid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPidFrom(pidPath, WingDaemon); !errors.Is(err, errStaleDaemonPID) {
		t.Fatalf("malformed PID error = %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("malformed PID file was not removed: %v", err)
	}
}

func TestStopDaemonAndWaitNeverSignalsUnrelatedProcess(t *testing.T) {
	if err := StopDaemonAndWait(os.Getpid(), WingDaemon, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if !procinfo.OwnedProcessIsAlive(os.Getpid()) {
		t.Fatal("test process was signaled")
	}
}

func TestWriteDaemonMetadataCommitsArgsBeforePIDWithPrivateModes(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "wing.pid")
	argsPath := filepath.Join(dir, "wing.args")
	args := []string{"wing", "start", "--foreground", "--paths", "/private/project"}
	if err := WriteDaemonMetadata(pidPath, argsPath, 1234, args); err != nil {
		t.Fatal(err)
	}
	pidData, err := os.ReadFile(pidPath)
	if err != nil || string(pidData) != "1234" {
		t.Fatalf("PID metadata = %q, %v", pidData, err)
	}
	argsData, err := os.ReadFile(argsPath)
	if err != nil || string(argsData) != strings.Join(args, "\n") {
		t.Fatalf("args metadata = %q, %v", argsData, err)
	}
	if info, err := os.Stat(argsPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("args mode = %v, %v", infoMode(info), err)
	}
	if info, err := os.Stat(pidPath); err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("PID mode = %v, %v", infoMode(info), err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".wt-daemon-") {
			t.Fatalf("temporary metadata file leaked: %s", entry.Name())
		}
	}
}

func TestWriteDaemonMetadataRollsBackArgsWhenPIDCommitFails(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "wing.pid")
	if err := os.Mkdir(pidPath, 0700); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(dir, "wing.args")
	if err := WriteDaemonMetadata(pidPath, argsPath, 1234, []string{"wing", "start", "--foreground"}); err == nil {
		t.Fatal("PID metadata commit unexpectedly succeeded over a directory")
	}
	if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
		t.Fatalf("args metadata survived failed PID commit: %v", err)
	}
}

func TestDaemonLifecycleLockSerializesCompetingOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	first, err := AcquireDaemonLifecycleLockAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := AcquireDaemonLifecycleLockAt(path); err == nil {
		_ = second.Close()
		t.Fatal("competing daemon lifecycle operation acquired the lock")
	} else if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("competing lock error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := AcquireDaemonLifecycleLockAt(path)
	if err != nil {
		t.Fatalf("lock was not released on close: %v", err)
	}
	_ = reopened.Close()
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

func TestWingStatusRoundTrip(t *testing.T) {
	// writeWingStatus/readWingStatus use wingStatusPath() which depends on config.Load().
	// We test the JSON struct directly for unit isolation.
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "wing.status")

	s := WingStatus{State: "connected", Error: "", TS: "2026-02-21T00:00:00Z", RoostURL: "https://roost.example"}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	var got WingStatus
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "connected" {
		t.Errorf("state = %q, want connected", got.State)
	}
	if got.RoostURL != "https://roost.example" {
		t.Errorf("roost URL = %q", got.RoostURL)
	}
}

func TestWriteWingStatusForRoostUsesPrivateMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	WriteWingStatusForRoost("connected", "", "wss://user:secret@roost.example/?token=private#fragment")

	status, err := ReadWingStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.RoostURL != "https://roost.example" {
		t.Fatalf("status roost = %q", status.RoostURL)
	}
	info, err := os.Stat(WingStatusPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("status mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestWingStatusAuthFailed(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "wing.status")

	s := WingStatus{State: "auth_failed", Error: "relay rejected authentication (401)", TS: "2026-02-21T00:00:00Z"}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	var got WingStatus
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "auth_failed" {
		t.Errorf("state = %q, want auth_failed", got.State)
	}
	if got.Error == "" {
		t.Error("expected non-empty error")
	}
}

func TestWingRoostFlags(t *testing.T) {
	tests := []struct {
		args      []string
		wantRoost string
		wantLocal bool
	}{
		{[]string{"wing", "start", "--foreground", "--roost", "https://one.example"}, "https://one.example", false},
		{[]string{"wing", "start", "--foreground", "--local"}, "", true},
		{[]string{"wing", "start", "--foreground", "--roost=https://two.example", "--local"}, "https://two.example", true},
	}
	for _, test := range tests {
		roost, local := wingRoostFlags(test.args)
		if roost != test.wantRoost || local != test.wantLocal {
			t.Errorf("wingRoostFlags(%v) = (%q, %v), want (%q, %v)", test.args, roost, local, test.wantRoost, test.wantLocal)
		}
	}
}

func TestActiveWingRelayHTTPURLPrefersStatusAndSupportsOldDaemonArgs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WINGTHING_DIR", dir)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(WingArgsPath(), []byte("wing\nstart\n--foreground\n--roost\nhttps://saved.example\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if got := ActiveWingRelayHTTPURL(cfg, &WingStatus{RoostURL: "wss://live.example/"}); got != "https://live.example" {
		t.Fatalf("status roost = %q", got)
	}
	if got := ActiveWingRelayHTTPURL(cfg, &WingStatus{}); got != "https://saved.example" {
		t.Fatalf("saved-args roost = %q", got)
	}
}
