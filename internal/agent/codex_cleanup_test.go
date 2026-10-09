//go:build !windows

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/procinfo"
)

func runCodexCleanupScript(t *testing.T, ctx context.Context, provider *Codex, script string, env ...string) (*Stream, *exec.Cmd) {
	t.Helper()
	var cmd *exec.Cmd
	stream, err := provider.Run(ctx, "lifecycle fixture", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", script)
		cmd.Env = append(os.Environ(), env...)
		return cmd, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return stream, cmd
}

func drainCodexCleanupStream(stream *Stream) {
	for {
		if _, ok := stream.Next(); !ok {
			return
		}
	}
}

func TestCodexCleanupExitObserver(t *testing.T) {
	for i := 0; i < 20; i++ {
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		observationErr := waitProcessExit(cmd.Process)
		probeErr := cmd.Process.Signal(syscall.Signal(0))
		waitErr := cmd.Wait()
		if observationErr != nil || probeErr != nil || waitErr != nil {
			t.Fatalf("exit observation %v, unreaped signal %v, wait %v", observationErr, probeErr, waitErr)
		}
	}
}

// Round-two buffered-output repro: the parser blocks on the 65th chunk while
// the provider is still writing. A consumer delay is never a cleanup deadline.
func TestCodexCleanupBufferedOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var script, want strings.Builder
	script.WriteString("printf '%s\\n' '{\"type\":\"turn.completed\"}'\n")
	for i := 0; i < 100; i++ {
		body := fmt.Sprintf("%03d:%s;", i, strings.Repeat("x", 200))
		want.WriteString(body)
		fmt.Fprintf(&script, "printf '%%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"%s\"}}'\n", body)
	}
	stream, cmd := runCodexCleanupScript(t, ctx, NewCodex(0), script.String())
	time.Sleep(codexCompletionQuiet + time.Second)
	drainCodexCleanupStream(stream)
	if stream.Err() != nil || stream.Text() != want.String() || cmd.ProcessState == nil {
		t.Fatalf("buffered output: %d/%d bytes, error %v, state %v", len(stream.Text()), want.Len(), stream.Err(), cmd.ProcessState)
	}
}

func TestCodexCleanupTracksStdoutBytes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// A single event takes longer than the quiet period to arrive, but stdout
	// keeps producing bytes. Resetting only on complete lines would truncate it.
	stream, cmd := runCodexCleanupScript(t, ctx, NewCodex(0), `
printf '%s\n' '{"type":"turn.completed"}'
printf '%s' '{"type":"item.completed","item":{"type":"agent_message","text":"'
for i in 1 2 3 4 5 6; do /bin/sleep 0.4; printf x; done
printf '%s\n' '"}}'
exec /bin/sleep 20`)
	drainCodexCleanupStream(stream)
	if stream.Err() != nil || stream.Text() != "xxxxxx" || ctx.Err() != nil || cmd.ProcessState == nil {
		t.Fatalf("active stdout truncated: %q, error %v, context %v, state %v", stream.Text(), stream.Err(), ctx.Err(), cmd.ProcessState)
	}
}

func TestCodexCleanupOutputWithinQuiet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	stream, cmd := runCodexCleanupScript(t, ctx, NewCodex(0), `printf '%s\n' '{"type":"turn.completed"}'; /bin/sleep 0.1; printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"late-final"}}' '{"type":"turn.completed","usage":{"input_tokens":9,"output_tokens":11}}'`)
	drainCodexCleanupStream(stream)
	in, out := stream.Tokens()
	if stream.Err() != nil || stream.Text() != "late-final" || in != 9 || out != 11 || cmd.ProcessState == nil {
		t.Fatalf("late output: %q, tokens %d/%d, error %v, state %v", stream.Text(), in, out, stream.Err(), cmd.ProcessState)
	}
}

func TestCodexCleanupAdditionalTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	stream, cmd := runCodexCleanupScript(t, ctx, NewCodex(0), `printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"first"}}' '{"type":"turn.completed"}' '{"type":"turn.started"}'; /bin/sleep 2.5; printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"second"}}' '{"type":"turn.completed"}'`)
	drainCodexCleanupStream(stream)
	if stream.Text() != "firstsecond" || FailureKind(stream.Err()) != ProviderError || !strings.Contains(stream.Err().Error(), "unsupported additional turn") || cmd.ProcessState == nil {
		t.Fatalf("additional turn: %q, error %v, state %v", stream.Text(), stream.Err(), cmd.ProcessState)
	}
}

func TestCodexCleanupExitBeforeSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	release := filepath.Join(t.TempDir(), "release")
	provider := NewCodex(0)
	provider.beforeTerminate = func(process *os.Process) {
		// Put the provider's own SIGKILL after the alive probe and before the
		// supervisor's stop/kill handshake, the round-two failure interleaving.
		if err := os.WriteFile(release, nil, 0600); err != nil {
			t.Error(err)
			return
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			exited, err := processExited(process)
			if err != nil {
				t.Error(err)
				return
			}
			if exited {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("provider did not self-kill before supervisor termination")
	}
	stream, cmd := runCodexCleanupScript(t, ctx, provider, `/bin/sleep 20 &
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"finished"}}' '{"type":"turn.completed"}'
while [ ! -f "$WT_CODEX_RELEASE" ]; do /bin/sleep 0.01; done
kill -KILL $$`, "WT_CODEX_RELEASE="+release)
	drainCodexCleanupStream(stream)
	var exit *exec.ExitError
	if FailureKind(stream.Err()) != ProviderExit || !errors.As(stream.Err(), &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL || stream.Text() != "finished" || ctx.Err() != nil || cmd.ProcessState == nil {
		t.Fatalf("provider's own signal became success: %v, context %v, state %v", stream.Err(), ctx.Err(), cmd.ProcessState)
	}
}

func TestCodexCleanupStderrFailure(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		kind             ErrorKind
	}{
		{"auth", "authentication failed: invalid api key", AuthFailed},
		{"refusal", "provider refused the request", ProviderRefused},
	} {
		for _, linger := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/linger=%v", tc.name, linger), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
				defer cancel()
				script := `printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"finished"}}' '{"type":"turn.completed"}'; /bin/sleep 0.1; printf '%s\n' "$WT_CODEX_DIAGNOSTIC" >&2`
				if linger {
					script += "; exec /bin/sleep 20"
				}
				stream, cmd := runCodexCleanupScript(t, ctx, NewCodex(0), script, "WT_CODEX_DIAGNOSTIC="+tc.diagnostic)
				drainCodexCleanupStream(stream)
				if FailureKind(stream.Err()) != tc.kind || stream.Text() != "finished" || cmd.ProcessState == nil || strings.Contains(stream.Err().Error(), tc.diagnostic) {
					t.Fatalf("stderr failure lost or leaked: %v, text %q, state %v", stream.Err(), stream.Text(), cmd.ProcessState)
				}
			})
		}
	}
}

func TestCodexCleanupIndependentExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	// No terminal event: only exit observation can end this read. The child
	// inherits stdout, but cannot hide the leader's nonzero exit.
	stream, cmd := runCodexCleanupScript(t, ctx, NewCodex(0), `/bin/sleep 20 &
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"finished"}}'
exit 7`)
	drainCodexCleanupStream(stream)
	var exit *exec.ExitError
	if ctx.Err() != nil || FailureKind(stream.Err()) != ProviderExit || !errors.As(stream.Err(), &exit) || exit.ExitCode() != 7 || stream.Text() != "finished" || cmd.ProcessState == nil {
		t.Fatalf("stdout hid leader exit: %v, context %v, state %v", stream.Err(), ctx.Err(), cmd.ProcessState)
	}
}

func TestCodexCleanupActiveChildAfterExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	stream, cmd := runCodexCleanupScript(t, ctx, NewCodex(0), `
printf '%s\n' '{"type":"turn.completed"}'
(
  printf '%s' '{"type":"item.completed","item":{"type":"agent_message","text":"'
  for i in 1 2 3 4 5 6; do /bin/sleep 0.4; printf x; done
  printf '%s\n' '"}}'
  exec /bin/sleep 20
) &
exit 0`)
	drainCodexCleanupStream(stream)
	if stream.Err() != nil || stream.Text() != "xxxxxx" || ctx.Err() != nil || cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatalf("active child output lost after leader exit: %q, error %v, context %v, state %v", stream.Text(), stream.Err(), ctx.Err(), cmd.ProcessState)
	}
}

func TestCodexCleanupAdditionalTurnPreservesFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	stream, _ := runCodexCleanupScript(t, ctx, NewCodex(0), `
printf '%s\n' '{"type":"turn.completed"}' '{"type":"turn.started"}' '{"type":"turn.completed"}'
printf 'authentication failed: invalid api key\n' >&2
exec /bin/sleep 20`)
	drainCodexCleanupStream(stream)
	if FailureKind(stream.Err()) != AuthFailed || !strings.Contains(stream.Err().Error(), "unsupported additional turn") {
		t.Fatalf("unsupported turn changed stderr failure kind: %v", stream.Err())
	}
}

func TestCodexCleanupGroupFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	provider := NewCodex(0)
	provider.signalGroup = func(*os.Process) error { return syscall.EPERM }
	stream, _ := runCodexCleanupScript(t, ctx, provider, `/bin/sleep 20 >/dev/null 2>/dev/null &
printf '%s' "$!" > "$WT_CODEX_CHILD"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"ready"}}' '{"type":"turn.completed"}'
wait`, "WT_CODEX_CHILD="+pidPath)
	if _, ok := stream.Next(); !ok {
		t.Fatal("provider did not become ready")
	}
	pid := codexCleanupChild(t, pidPath)
	drainCodexCleanupStream(stream)
	if FailureKind(stream.Err()) != ProviderExit || !strings.Contains(stream.Err().Error(), "group signal") || !codexFixtureChildRunning(t, pid) {
		t.Fatalf("failed group signal became success: %v", stream.Err())
	}
}

func TestCodexCleanupCancellationAfterEOF(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timeout=%v", timeout), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			closedPath := filepath.Join(t.TempDir(), "stdout.closed")
			provider := NewCodex(0)
			signals := make(chan error, 8)
			provider.signalGroup = func(process *os.Process) error {
				err := process.Signal(syscall.Signal(0))
				signals <- err
				if err != nil {
					return err
				}
				return syscall.Kill(-process.Pid, syscall.SIGKILL)
			}
			stream, cmd := runCodexCleanupScript(t, ctx, provider, `/bin/sleep 20 >/dev/null 2>/dev/null &
printf '%s' "$!" > "$WT_CODEX_CHILD"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"ready"}}'
exec 1>&-
printf closed > "$WT_CODEX_CLOSED"
wait`, "WT_CODEX_CHILD="+pidPath, "WT_CODEX_CLOSED="+closedPath)
			if chunk, ok := stream.Next(); !ok || chunk.Text != "ready" {
				t.Fatal("provider did not become ready")
			}
			pid := codexCleanupChild(t, pidPath)
			deadline := time.Now().Add(200 * time.Millisecond)
			for {
				if _, err := os.Stat(closedPath); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("provider did not close stdout")
				}
				time.Sleep(time.Millisecond)
			}
			// Let the parser observe EOF before either cancellation path.
			time.Sleep(50 * time.Millisecond)
			if !timeout {
				cancel()
			}
			drainCodexCleanupStream(stream)
			assertCodexCleanupChildStopped(t, pid)
			if FailureKind(stream.Err()) != ProviderExit || cmd.ProcessState == nil || ctx.Err() == nil {
				t.Fatalf("canceled EOF provider: %v, context %v, state %v", stream.Err(), ctx.Err(), cmd.ProcessState)
			}
			if len(signals) == 0 {
				t.Fatal("cancellation did not signal the group")
			}
			for len(signals) > 0 {
				if err := <-signals; err != nil {
					t.Errorf("group signal after reap: %v", err)
				}
			}
			if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) || len(signals) != 0 {
				t.Errorf("cancellation after reap signaled group: %v", err)
			}
		})
	}
}

func TestCodexCleanupCancellationRace(t *testing.T) {
	// The round-two cancellation repro races the stdout closer, parser and
	// exec.CommandContext watcher; repeat it without weakening group cleanup.
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		pidPath := filepath.Join(t.TempDir(), "child.pid")
		stream, _ := runCodexCleanupScript(t, ctx, NewCodex(0), `/bin/sleep 20 >/dev/null 2>/dev/null &
printf '%s' "$!" > "$WT_CODEX_CHILD"
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"ready"}}'
wait`, "WT_CODEX_CHILD="+pidPath)
		if _, ok := stream.Next(); !ok {
			cancel()
			t.Fatal("provider did not become ready")
		}
		pid := codexCleanupChild(t, pidPath)
		cancel()
		drainCodexCleanupStream(stream)
		assertCodexCleanupChildStopped(t, pid)
		if FailureKind(stream.Err()) != ProviderExit {
			t.Fatalf("cancellation became success: %v", stream.Err())
		}
	}
}

func codexCleanupChild(t *testing.T, pidPath string) int {
	t.Helper()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := procinfo.ProcessIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		current, err := procinfo.ProcessIdentity(pid)
		if err == nil && current == identity && codexFixtureChildRunning(t, pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return pid
}

func assertCodexCleanupChildStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for codexFixtureChildRunning(t, pid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if codexFixtureChildRunning(t, pid) {
		t.Errorf("cancellation left process-group child %d alive", pid)
	}
}
