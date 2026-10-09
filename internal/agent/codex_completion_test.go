//go:build !windows

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/procinfo"
)

// A real process fixture keeps the provider and inherited descriptors alive
// independently of the final JSON event. It never calls a model or MCP server.
func TestCodexCompletionProcess(t *testing.T) {
	mode := os.Getenv("WT_CODEX_COMPLETION_MODE")
	if mode == "" {
		return
	}
	if strings.Contains(mode, "child") {
		duration := "60"
		if mode == "escaped-stderr-child" {
			duration = "4"
		}
		child := exec.Command("/bin/sleep", duration)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if mode == "stderr-child" || mode == "escaped-stderr-child" || mode == "self-killed-stderr-child" {
			child.Stdout = nil
		}
		if mode == "escaped-stderr-child" {
			child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		}
		if mode == "escaped-no-terminal-child" {
			child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			child.Stderr = nil
		}
		if err := child.Start(); err != nil {
			panic(err)
		}
		if err := os.WriteFile(os.Getenv("WT_CODEX_CHILD_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			panic(err)
		}
	}
	if mode == "flood" {
		// Both pipes exceed their kernel buffers and the stderr retention cap.
		for i := 0; i < 512; i++ {
			fmt.Fprintln(os.Stdout, `{"type":"item.completed","item":{"type":"command_execution","output":"`+strings.Repeat("x", 4096)+`"}}`)
			fmt.Fprintln(os.Stderr, strings.Repeat("x", 4096))
		}
	}
	if mode == "oversized-response" {
		fmt.Fprintln(os.Stdout, `{"type":"item.completed","item":{"type":"agent_message","text":"`+strings.Repeat("x", maxProviderLine)+`"}}`)
	} else {
		fmt.Fprintln(os.Stdout, `{"type":"item.completed","item":{"type":"agent_message","text":"finished"}}`)
	}
	switch mode {
	case "no-terminal", "escaped-no-terminal-child":
		// An assistant message alone is not proof that the turn has finished.
	case "malformed-terminal":
		fmt.Fprintln(os.Stdout, `{"type":"turn.completed"`)
	case "failed":
		fmt.Fprintln(os.Stdout, `{"type":"turn.failed","error":{"message":"Provider refused the request."}}`)
	case "no-usage":
		fmt.Fprintln(os.Stdout, `{"type":"turn.completed"}`)
	default:
		fmt.Fprintln(os.Stdout, `{"type":"turn.completed","usage":{"input_tokens":3,"output_tokens":5}}`)
	}
	if mode == "stdout-child" || mode == "stderr-child" || mode == "escaped-stderr-child" {
		os.Exit(0)
	}
	if mode == "nonzero" || mode == "nonzero-child" {
		os.Exit(7)
	}
	if mode == "signaled-child" {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}
	if mode == "self-killed-child" || mode == "self-killed-stderr-child" {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	if mode == "closed-stdout" {
		_ = os.Stdout.Close()
	}
	// SIGKILL must stop this process even if ordinary termination is ignored.
	for {
		time.Sleep(time.Minute)
	}
}

func runCodexCompletionFixture(t *testing.T, ctx context.Context, mode string) (*Stream, *exec.Cmd, string) {
	t.Helper()
	return runCodexCompletionFixtureWithProvider(t, ctx, mode, NewCodex(0))
}

func runCodexCompletionFixtureWithProvider(t *testing.T, ctx context.Context, mode string, provider *Codex) (*Stream, *exec.Cmd, string) {
	t.Helper()
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	var cmd *exec.Cmd
	stream, err := provider.Run(ctx, "lifecycle fixture", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
		cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCodexCompletionProcess$")
		cmd.Env = append(os.Environ(), "WT_CODEX_COMPLETION_MODE="+mode, "WT_CODEX_CHILD_PID="+pidPath)
		return cmd, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return stream, cmd, pidPath
}

func TestCodexCompletionNeverSignalsReapedGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	provider := NewCodex(0)
	signals := make(chan error, 2)
	provider.signalGroup = func(process *os.Process) error {
		// Process.Signal refuses an already-reaped leader. Observe every actual
		// group signal, including cancellation, without trying to reuse a PGID.
		err := process.Signal(syscall.Signal(0))
		signals <- err
		if err != nil {
			return err
		}
		return syscall.Kill(-process.Pid, syscall.SIGKILL)
	}
	stream, cmd, pidPath := runCodexCompletionFixtureWithProvider(t, ctx, "escaped-stderr-child", provider)
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidPath); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil && codexFixtureChildRunning(t, pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	if stream.Err() != nil || ctx.Err() != nil || cmd.ProcessState == nil {
		t.Fatalf("completed provider was not successfully reaped: %v, %v", stream.Err(), ctx.Err())
	}
	select {
	case err := <-signals:
		if err != nil {
			t.Fatalf("group signal attempted after leader reap: %v", err)
		}
	default:
		t.Fatal("completion cleanup did not signal the owned group")
	}
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("cancellation after reap = %v, want os.ErrProcessDone", err)
	}
	select {
	case err := <-signals:
		t.Fatalf("another group signal attempted after reap: %v", err)
	default:
	}
}

func TestCodexCompletionPreservesProviderSIGKILL(t *testing.T) {
	for _, mode := range []string{"self-killed-child", "self-killed-stderr-child"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			stream, cmd, _ := runCodexCompletionFixture(t, ctx, mode)
			for {
				if _, ok := stream.Next(); !ok {
					break
				}
			}
			var exit *exec.ExitError
			if ctx.Err() != nil || FailureKind(stream.Err()) != ProviderExit || !errors.As(stream.Err(), &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("provider's own SIGKILL was lost: %v, context %v", stream.Err(), ctx.Err())
			}
			if cmd.ProcessState == nil || stream.Text() != "finished" {
				t.Errorf("provider was not reaped with its final output: %v, %q", cmd.ProcessState, stream.Text())
			}
		})
	}
}

func TestCodexCompletedTurnReapsProvider(t *testing.T) {
	for _, mode := range []string{"live-child", "stdout-child", "stderr-child", "closed-stdout", "no-usage", "flood", "failed", "nonzero", "nonzero-child", "signaled-child", "oversized-response"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			started := time.Now()
			stream, cmd, pidPath := runCodexCompletionFixture(t, ctx, mode)
			for {
				if _, ok := stream.Next(); !ok {
					break
				}
			}
			if ctx.Err() != nil {
				t.Errorf("terminal event did not complete before the run timeout: %v", ctx.Err())
			}
			if elapsed := time.Since(started); elapsed > 7*time.Second {
				t.Errorf("completion cleanup took %s", elapsed)
			}
			switch mode {
			case "failed":
				if FailureKind(stream.Err()) != ProviderRefused {
					t.Errorf("terminal failure was lost: %v", stream.Err())
				}
			case "nonzero", "nonzero-child":
				var exit *exec.ExitError
				if !errors.As(stream.Err(), &exit) || exit.ExitCode() != 7 {
					t.Errorf("natural nonzero exit was lost: %v", stream.Err())
				}
			case "signaled-child":
				var exit *exec.ExitError
				if !errors.As(stream.Err(), &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
					t.Errorf("natural signal exit was lost: %v", stream.Err())
				}
			case "oversized-response":
				if FailureKind(stream.Err()) != ProviderError || !strings.Contains(stream.Err().Error(), "final response event skipped") {
					t.Errorf("incomplete result was accepted: %v", stream.Err())
				}
			default:
				if stream.Err() != nil {
					t.Errorf("completed turn failed: %v", stream.Err())
				}
			}
			if mode != "oversized-response" && stream.Text() != "finished" {
				t.Errorf("final output = %q", stream.Text())
			}
			if mode != "failed" && mode != "no-usage" {
				if input, output := stream.Tokens(); input != 3 || output != 5 {
					t.Errorf("usage = %d/%d", input, output)
				}
			}
			if cmd.ProcessState == nil {
				t.Error("provider was not reaped")
			}
			if data, err := os.ReadFile(pidPath); err == nil {
				pid, err := strconv.Atoi(string(data))
				if err != nil {
					t.Fatal(err)
				}
				// Give init a short bounded interval to reap the killed child.
				deadline := time.Now().Add(time.Second)
				for codexFixtureChildRunning(t, pid) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if codexFixtureChildRunning(t, pid) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					t.Error("completed provider left its process-group child alive")
				}
			} else if strings.Contains(mode, "child") {
				t.Errorf("child fixture did not start: %v", err)
			}
		})
	}
}

func codexFixtureChildRunning(t *testing.T, pid int) bool {
	t.Helper()
	if !procinfo.OwnedProcessIsAlive(pid) {
		return false
	}
	if runtime.GOOS == "linux" {
		// The child's new parent may not reap promptly in a container. A zombie
		// is already terminated; the provider itself must still have been reaped.
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		end := strings.LastIndexByte(string(stat), ')')
		fields := strings.Fields(string(stat)[end+1:])
		if end < 0 || len(fields) == 0 {
			t.Fatalf("invalid child process stat: %q", stat)
		}
		return fields[0] != "Z"
	}
	return true
}

func TestCodexCompletionRequiresTerminalEvent(t *testing.T) {
	for _, mode := range []string{"no-terminal", "malformed-terminal"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			stream, _, _ := runCodexCompletionFixture(t, ctx, mode)
			for {
				if _, ok := stream.Next(); !ok {
					break
				}
			}
			if ctx.Err() == nil || stream.Err() == nil {
				t.Fatalf("nonterminal stream completed successfully: %v", stream.Err())
			}
		})
	}
}

func TestCodexCompletedTurnDoesNotOverrideCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	stream, _, _ := runCodexCompletionFixture(t, ctx, "live")
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	if ctx.Err() == nil || FailureKind(stream.Err()) != ProviderExit || stream.Text() != "finished" {
		t.Fatalf("canceled completed turn = %q, %v, context %v", stream.Text(), stream.Err(), ctx.Err())
	}
}

func TestCodexCancellationClosesEscapedChildStdout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	stream, cmd, pidPath := runCodexCompletionFixture(t, ctx, "escaped-no-terminal-child")
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidPath); err == nil {
			pid, err := strconv.Atoi(string(data))
			if err == nil && codexFixtureChildRunning(t, pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("escaped stdout child held cancellation for %s", elapsed)
	}
	if ctx.Err() == nil || stream.Err() == nil || cmd.ProcessState == nil {
		t.Errorf("cancellation did not fail and reap provider: %v, %v", ctx.Err(), stream.Err())
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil || !codexFixtureChildRunning(t, pid) {
		t.Fatalf("escaped child fixture was not alive: %d, %v", pid, err)
	}
}
