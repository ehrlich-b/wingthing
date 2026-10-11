package egg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/ehrlich-b/wingthing/internal/egg/pb"
	"golang.org/x/sys/unix"
)

// Reproduce the provider/MCP shutdown ordering without sandbox-exec: TERM
// exits the provider, closes its MCP pipe, and leaves the MCP child in Darwin's
// kernel exit path when terminateSession sends its final group SIGKILL.
func TestSessionGroupExitHelper(t *testing.T) {
	switch os.Getenv("WT_SESSION_GROUP_EXIT_HELPER") {
	case "mcp":
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "provider":
		child := exec.Command(os.Args[0], "-test.run=^TestSessionGroupExitHelper$")
		child.Env = append(os.Environ(), "WT_SESSION_GROUP_EXIT_HELPER=mcp")
		input, err := child.StdinPipe()
		if err != nil {
			panic(err)
		}
		defer input.Close()
		output, err := child.StdoutPipe()
		if err != nil {
			panic(err)
		}
		if err := child.Start(); err != nil {
			panic(err)
		}
		if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
			panic(fmt.Sprintf("MCP readiness: %q %v", line, err))
		}
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}

func TestTerminateSessionWithExitingProcessGroup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionGroupExitHelper$")
	cmd.Env = append(os.Environ(), "WT_SESSION_GROUP_EXIT_HELPER=provider")
	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	pgid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = terminal.Close()
		<-done
	})
	if line, err := bufio.NewReader(terminal).ReadString('\n'); err != nil || line != "ready\r\n" {
		t.Fatalf("provider readiness: %q %v", line, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	server := &Server{session: &Session{PID: pgid, processGroupID: pgid, cmd: cmd, done: done}}
	if _, err := server.Kill(ctx, &pb.KillRequest{}); err != nil {
		members, inventoryErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
		for _, member := range members {
			t.Logf("group member pid=%d state=%d flags=%#x", member.Proc.P_pid, member.Proc.P_stat, member.Proc.P_flag)
		}
		t.Fatalf("stop provider with exiting MCP child: %v (inventory: %v)", err, inventoryErr)
	}
	select {
	case <-done:
	default:
		t.Fatal("Kill returned before the provider exited")
	}
}

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

func TestSessionGroupExitStates(t *testing.T) {
	// Native reproductions return SRUN (2), P_LP64|P_CONTROLT|P_EXEC|P_WEXIT
	// (0x6006) for the exiting Go MCP child. Keep this regression deterministic
	// even on kernels that finish exit before the final group signal.
	tests := []struct {
		name    string
		members []unix.ExternProc
		live    bool
	}{
		{"empty", nil, false},
		{"zombie", []unix.ExternProc{{P_stat: 5}}, false},
		{"exiting_runnable_mcp", []unix.ExternProc{{P_stat: 2, P_flag: 0x6006}}, false},
		{"exiting_sleeping", []unix.ExternProc{{P_stat: 3, P_flag: 0x2000}}, false},
		{"zombie_and_exiting", []unix.ExternProc{{P_stat: 5}, {P_stat: 2, P_flag: 0x6006}}, false},
		{"runnable", []unix.ExternProc{{P_stat: 2}}, true},
		{"sleeping", []unix.ExternProc{{P_stat: 3}}, true},
		{"stopped", []unix.ExternProc{{P_stat: 4}}, true},
		{"being_created", []unix.ExternProc{{P_stat: 1}}, true},
		{"exec_without_exit", []unix.ExternProc{{P_stat: 2, P_flag: 0x4000}}, true},
		{"unknown_state", []unix.ExternProc{{}}, true},
		{"live_and_exiting", []unix.ExternProc{{P_stat: 2}, {P_stat: 2, P_flag: 0x6006}}, true},
		{"live_and_zombie", []unix.ExternProc{{P_stat: 3}, {P_stat: 5}}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			members := make([]unix.KinfoProc, len(test.members))
			for i, member := range test.members {
				members[i].Proc = member
			}
			if got := sessionGroupHasLiveMembers(members); got != test.live {
				t.Fatalf("live group members = %t, want %t", got, test.live)
			}
		})
	}
}
