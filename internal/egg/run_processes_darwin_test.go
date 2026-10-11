package egg

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg/pb"
	"golang.org/x/sys/unix"
)

// Keep a zombie owned by the test in the provider's group, so the kernel cannot
// reap the last group member between the provider's Wait and the final SIGKILL.
// This reproduces stop/kill after a provider and its MCP subprocess both exit.
func TestTerminateSessionWithZombieOnlyProcessGroup(t *testing.T) {
	root := exec.Command("/bin/sh", "-c", "printf 'ready\\n'; read line")
	root.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := root.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := root.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { _ = root.Process.Kill(); <-done })
	go func() { _ = root.Wait(); close(done) }()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("wait for provider readiness: line=%q err=%v", line, err)
	}
	pgid := root.Process.Pid
	zombie := exec.Command("/bin/sh", "-c", "exit 0")
	zombie.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	if err := zombie.Start(); err != nil {
		t.Fatal(err)
	}
	// Deliberately defer Wait: an unreaped exited child is the failure trigger.
	t.Cleanup(func() { _ = zombie.Process.Kill(); _ = zombie.Wait() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
		if err != nil {
			t.Fatal(err)
		}
		ready := false
		for _, member := range members {
			if member.Proc.P_pid == int32(zombie.Process.Pid) && member.Proc.P_stat == processStateZombie {
				ready = true
			}
		}
		if ready {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("child did not become a zombie")
		}
	}
	server := &Server{session: &Session{PID: pgid, processGroupID: pgid, cmd: root, done: done}}
	if _, err := server.Kill(ctx, &pb.KillRequest{}); err != nil {
		t.Fatalf("stop provider with zombie descendant: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("Kill returned before the provider exited")
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("zombie-only group signal = %v, want native EPERM", err)
	}
}

func TestSessionGroupPermissionErrorWithLiveMembers(t *testing.T) {
	// Our own group is known to contain a live process. Permission failure
	// must remain failure even though Darwin also uses EPERM for zombie groups.
	if err := sessionGroupSignalError(syscall.Getpgrp(), syscall.EPERM); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("live group permission error = %v, want EPERM", err)
	}
	if err := sessionGroupSignalError(syscall.Getpgrp(), syscall.EINVAL); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("non-permission error = %v, want EINVAL", err)
	}
}
