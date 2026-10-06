package egg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// Linux sandboxes re-exec the test binary through the same wrapper as wt.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
		return
	}
	os.Exit(m.Run())
}

func TestRecoveryExitDistinguishesShutdownFromDeliberateExit(t *testing.T) {
	for _, tc := range []struct {
		name               string
		code               int
		cancelled, stopped bool
	}{
		{"exit", 0, false, true}, {"shutdown", 0, true, false}, {"crash", -1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := recordSessionProcessExit(dir, tc.code, tc.cancelled); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(filepath.Join(dir, DeliberateStopFile))
			if (err == nil) != tc.stopped {
				t.Fatalf("stop marker: %v", err)
			}
		})
	}
}

func TestRecoveryKillPersistsDeliberateStop(t *testing.T) {
	dir := t.TempDir()
	s := &Server{dir: dir, session: &Session{}}
	if _, err := s.Kill(context.Background(), &pb.KillRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, DeliberateStopFile)); err != nil {
		t.Fatal(err)
	}
	if !s.session.cancelled {
		t.Fatal("kill not recorded as cancelled")
	}
}

func TestRecoveryIdleTerminationPersistsStopBeforeSignal(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "eggs", "idle")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	record, err := NewRecoveryRecord(LaunchIntent{Version: 1, Agent: "claude", CWD: state, Started: true}, UnsandboxedEggConfig(), state)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteRecoveryRecord(dir, record); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(state, "ready")
	witness := filepath.Join(state, "observed")
	cmd := exec.Command("sh", "-c", `trap '[ -f "$1" ] && printf observed > "$3"; exit 0' TERM; printf ready > "$2"; while :; do sleep 0.01; done`, "idle-test", filepath.Join(dir, DeliberateStopFile), ready, witness)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle fixture not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	sess := &Session{cmd: cmd, done: done}
	s := &Server{dir: dir}
	if err := s.stopIdleSession(sess); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(witness); err != nil || string(data) != "observed" {
		t.Fatalf("signal preceded stop marker: %q %v", data, err)
	}
	protected, err := ReadRecoveryRecord(dir)
	if err != nil || !protected.Stopped || !sess.cancelled {
		t.Fatalf("idle stop not durable: %+v %v", protected, err)
	}
}

func TestRecoveryIdleTerminationRefusesUnpersistedStop(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, DeliberateStopFile), 0700); err != nil {
		t.Fatal(err)
	}
	sess := &Session{}
	if err := (&Server{dir: dir}).stopIdleSession(sess); err == nil || sess.cancelled {
		t.Fatalf("unpersisted idle stop admitted: cancelled=%v, %v", sess.cancelled, err)
	}
}

func TestRecoveryStorageCannotBeWrittenOrReplacedBySandbox(t *testing.T) {
	if ok, help := sandbox.CheckCapability(); !ok {
		t.Skipf("platform sandbox unavailable: %s", help)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state := t.TempDir()
	dir := filepath.Join(state, "eggs", "sandbox")
	guard := sandbox.Config{Mounts: []sandbox.Mount{{Source: state, Target: state}}, NetworkNeed: sandbox.NetworkNone}
	if err := protectRecoveryStorage(&guard, dir); err != nil {
		t.Fatal(err)
	}
	if guard.RecoveryDir == "" {
		t.Fatal("egg sandbox lacks a mandatory recovery storage guard")
	}
	if runtime.GOOS == "darwin" {
		baseline := guard
		baseline.RecoveryDir = ""
		sb, err := sandbox.New(baseline)
		if err != nil {
			t.Fatal(err)
		}
		defer sb.Destroy()
		cmd, err := sb.Exec(ctx, "/usr/bin/true", nil)
		if err != nil {
			t.Fatal(err)
		}
		cmd.WaitDelay = time.Second
		if output, err := cmd.CombinedOutput(); err != nil {
			if strings.Contains(string(output), "sandbox_apply: Operation not permitted") {
				t.Skip("unguarded Seatbelt also cannot run in this environment")
			}
			t.Fatalf("baseline sandbox failed: %v %s", err, output)
		}
	}
	path := filepath.Join(RecoveryDir(dir), "sandbox.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.New(guard)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Destroy()
	// A deliberately broad writable mount still cannot reopen recovery state
	// or rename its ancestor. A writable sibling remains usable.
	cmd, err := sb.Exec(ctx, "/bin/sh", []string{"-c", `printf forged > "$1" 2>/dev/null; mv "$2" "$2-moved" 2>/dev/null; printf sibling > "$2/sibling"`, "storage-test", path, state})
	if err != nil {
		t.Fatal(err)
	}
	cmd.WaitDelay = time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("storage guard: %v %s", err, output)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "original" {
		t.Fatalf("sandbox overwrote authority: %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(state, "sibling")); err != nil || !strings.Contains(string(data), "sibling") {
		t.Fatalf("guard blocked writable sibling: %q %v", data, err)
	}
}
