package egg

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func terminationFixture(t *testing.T) (*Session, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "trap '' TERM; printf 'ready\\n'; read line")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = input.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("termination fixture child was not reaped")
		}
	})
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(output).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatalf("termination fixture readiness: %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("termination fixture readiness timed out")
	}
	return &Session{PID: cmd.Process.Pid, processGroupID: cmd.Process.Pid, cmd: cmd, done: done}, done
}

func TestTerminateSessionBoundsMissingReaperNotification(t *testing.T) {
	sess, reaped := terminationFixture(t)
	// Model a lost reaper notification. The child is still reaped by its one
	// real Wait goroutine, but terminateSession must not wait forever for done.
	sess.done = make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- terminateSession(context.Background(), sess, 20*time.Millisecond) }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "child reaped=false") {
			t.Fatalf("missing reaper notification: %v", err)
		}
		select {
		case <-reaped:
		default:
			t.Fatal("SIGKILL did not terminate and reap the child")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("termination hung without a caller deadline")
	}
}

func TestTerminateSessionCancellationStillKillsAndReaps(t *testing.T) {
	sess, reaped := terminationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() { result <- terminateSession(ctx, sess, time.Hour) }()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("termination cancellation: %v", err)
		}
		select {
		case <-reaped:
		default:
			t.Fatal("cancelled termination left a TERM-ignoring child alive")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cancelled termination hung")
	}
}
