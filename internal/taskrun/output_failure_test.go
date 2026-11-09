//go:build linux

package taskrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

var errOutputDestination = errors.New("output destination closed")

type outputFailureWriter struct{}

func (outputFailureWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "rejected output") {
		return 0, errOutputDestination
	}
	return len(p), nil
}

func TestRunTaskOutputFailureStopsProvider(t *testing.T) {
	for _, timeout := range []string{"0s", "1m"} {
		t.Run(timeout, func(t *testing.T) {
			if config.Channel() == "preview" {
				t.Skip("direct execution is unavailable in preview")
			}
			cfg := &config.Config{Dir: testsocket.Dir(t), DefaultAgent: "codex", WingID: "test-wing"}
			if err := os.MkdirAll(cfg.SkillsDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			skill := fmt.Sprintf("---\nname: output-failure\ntimeout: %s\nmemory: []\n---\nPrint output.\n", timeout)
			if err := os.WriteFile(filepath.Join(cfg.SkillsDir(), "output-failure.md"), []byte(skill), 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(cfg.DBPath())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			workDir := testsocket.Dir(t)
			binDir := testsocket.Dir(t)
			// The provider waits indefinitely with a child and grandchild in its
			// process group. Publish every PID before emitting any output.
			script := `#!/bin/sh
printf '%s\n' "$$" > provider.pid
/bin/sh -c 'sleep 600 & printf "%s\n" "$!" > grandchild.pid; printf "%s\n" "$$" > child.pid; wait' &
while [ ! -s child.pid ] || [ ! -s grandchild.pid ]; do sleep 0.01; done
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"partial output"}}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"rejected output"}}'
# Fill more than the stream buffer to exercise cancellation while sending.
i=0
while [ "$i" -lt 100 ]; do
  printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"buffered output"}}'
  i=$((i + 1))
done
wait
`
			if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+":/usr/bin:/bin")
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() {
				cancel()
				if data, err := os.ReadFile(filepath.Join(workDir, "provider.pid")); err == nil {
					pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
					if pid > 0 {
						_ = syscall.Kill(-pid, syscall.SIGKILL)
					}
				}
			})
			task := &store.Task{ID: "output-failure", Type: "skill", What: "output-failure", RunAt: time.Now(), Agent: "codex", Isolation: "privileged", CWD: workDir}
			if err := db.CreateTask(task); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			err = RunTaskTo(ctx, cfg, db, task, outputFailureWriter{})
			if !errors.Is(err, errOutputDestination) {
				t.Fatalf("expected destination failure, got %v", err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("output failure took %s; should not wait for timeout %s", elapsed, timeout)
			}
			for _, name := range []string{"provider", "child", "grandchild"} {
				data, err := os.ReadFile(filepath.Join(workDir, name+".pid"))
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil || pid <= 0 {
					t.Fatalf("invalid %s PID: %q", name, data)
				}
				if name == "provider" {
					// Wait must have reaped the direct provider before returning.
					if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
						t.Errorf("provider %d was not joined before return: %v", pid, err)
					}
				}
				// Orphan descendants can remain zombies until init reaps them.
				deadline := time.Now().Add(2 * time.Second)
				for {
					stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
					if errors.Is(err, os.ErrNotExist) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					_, state, _ := strings.Cut(string(stat), ") ")
					if strings.HasPrefix(state, "Z ") || strings.HasPrefix(state, "X ") {
						break
					}
					if time.Now().After(deadline) {
						t.Errorf("%s %d survived output failure: %s", name, pid, stat)
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			saved, err := db.GetTask(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status != "failed" || saved.FinishedAt == nil || saved.Error == nil || !strings.Contains(*saved.Error, errOutputDestination.Error()) {
				t.Errorf("destination failure was not persisted: %#v", saved)
			}
			if saved.Output == nil || !strings.Contains(*saved.Output, "partial output") {
				t.Errorf("partial provider output was not persisted: %#v", saved.Output)
			}
		})
	}
}

type finishFailureWriter struct{}

func (finishFailureWriter) Write(p []byte) (int, error) {
	if string(p) == "\n" {
		return 0, errOutputDestination
	}
	return len(p), nil
}

func TestRunTaskCompletedProviderReceipts(t *testing.T) {
	for _, mode := range []string{"success", "provider-failure", "finish-failure"} {
		t.Run(mode, func(t *testing.T) {
			if config.Channel() == "preview" {
				t.Skip("direct execution is unavailable in preview")
			}
			cfg := &config.Config{Dir: testsocket.Dir(t), DefaultAgent: "codex", WingID: "test-wing"}
			db, err := store.Open(cfg.DBPath())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			binDir := testsocket.Dir(t)
			script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"partial output\"}}'\n"
			if mode == "provider-failure" {
				script += "echo 'provider failed' >&2\nexit 1\n"
			}
			if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+":/usr/bin:/bin")
			task := &store.Task{ID: "completed-provider", Type: "prompt", What: "Print output.", RunAt: time.Now(), Agent: "codex", Isolation: "privileged", CWD: testsocket.Dir(t)}
			if err := db.CreateTask(task); err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			var destination io.Writer = &out
			if mode == "finish-failure" {
				destination = finishFailureWriter{}
			}
			err = RunTaskTo(context.Background(), cfg, db, task, destination)
			saved, getErr := db.GetTask(task.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if saved.Output == nil || !strings.Contains(*saved.Output, "partial output") {
				t.Fatalf("lost output: %#v", saved.Output)
			}
			if mode == "success" {
				if err != nil || saved.Status != "done" || saved.Error != nil || *saved.Output != "partial output" {
					t.Fatalf("successful result changed: err=%v task=%#v", err, saved)
				}
				return
			}
			if err == nil || saved.Status != "failed" || saved.Error == nil || saved.FinishedAt == nil {
				t.Fatalf("missing failure receipt: err=%v task=%#v", err, saved)
			}
			if mode == "finish-failure" && !errors.Is(err, errOutputDestination) {
				t.Fatalf("lost destination error: %v", err)
			}
			if mode == "provider-failure" && (!strings.Contains(*saved.Output, "provider failed") || !strings.Contains(*saved.Error, "provider failed")) {
				t.Fatalf("lost provider diagnostics: %#v", saved)
			}
		})
	}
}

// Each fixture owns a process group whose descendants deliberately close the
// provider pipes, allowing Wait to finish while they are still running.
func outputFailureFixture(t *testing.T, provider, mode string) (*config.Config, *store.Store, string, string) {
	t.Helper()
	if config.Channel() == "preview" {
		t.Skip("direct execution is unavailable in preview")
	}
	cfg := &config.Config{Dir: testsocket.Dir(t), DefaultAgent: provider, WingID: "test-wing"}
	if err := os.MkdirAll(cfg.SkillsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.SkillsDir(), "output-failure.md"), []byte("---\nname: output-failure\ntimeout: 0s\nmemory: []\n---\nPrint output.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workDir, binDir := testsocket.Dir(t), testsocket.Dir(t)
	event := func(text string) string {
		switch provider {
		case "codex":
			return fmt.Sprintf(`{"type":"item.completed","item":{"type":"agent_message","text":%q}}`, text)
		case "claude":
			return fmt.Sprintf(`{"type":"content_block_delta","delta":{"type":"text_delta","text":%q}}`, text)
		default:
			return text
		}
	}
	script := `#!/bin/sh
printf '%s\n' "$$" > provider.pid
/bin/sh -c 'sleep 600 & printf "%s\n" "$!" > grandchild.pid; printf "%s\n" "$$" > child.pid; wait' </dev/null >/dev/null 2>&1 &
while [ ! -s child.pid ] || [ ! -s grandchild.pid ]; do sleep 0.01; done
`
	script += "printf '%s\\n' '" + event("partial output") + "'\n"
	if mode == "pipe" {
		script += "while [ ! -f release ]; do sleep 0.01; done\n"
		script += "printf '%s\\n' '" + event("rejected output") + "'\nwait\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, provider), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Cleanup(func() {
		if data, err := os.ReadFile(filepath.Join(workDir, "provider.pid")); err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	})
	return cfg, db, workDir, binDir
}

func outputFailurePID(t *testing.T, workDir, name string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workDir, name+".pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid %s PID: %q", name, data)
	}
	return pid
}

// A process can disappear after procfs opens its stat file but before it is read.
func procStatMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func TestProcStatMissingAfterProcessExit(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	_, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
	if !procStatMissing(err) {
		t.Fatalf("exited process stat: %v", err)
	}
	if !procStatMissing(&os.PathError{Op: "read", Path: "stat", Err: syscall.ESRCH}) {
		t.Fatal("ESRCH from a procfs read must mean the process exited")
	}
	if procStatMissing(syscall.EACCES) {
		t.Fatal("permission errors do not prove a process exited")
	}
}

func assertOutputFailureTreeStopped(t *testing.T, workDir string) {
	t.Helper()
	for _, name := range []string{"provider", "child", "grandchild"} {
		pid := outputFailurePID(t, workDir, name)
		if name == "provider" && !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			t.Errorf("provider %d was not reaped", pid)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if procStatMissing(err) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			_, state, _ := strings.Cut(string(stat), ") ")
			if strings.HasPrefix(state, "Z ") || strings.HasPrefix(state, "X ") {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("%s %d survived output failure", name, pid)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func assertOutputFailureReceipt(t *testing.T, db *store.Store, id, message string) {
	t.Helper()
	saved, err := db.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != "failed" || saved.FinishedAt == nil || saved.Error == nil || !strings.Contains(*saved.Error, message) {
		t.Errorf("missing durable failure: %#v", saved)
	}
	if saved.Output == nil || !strings.Contains(*saved.Output, "partial output") {
		t.Errorf("missing partial output: %#v", saved.Output)
	}
}

type reapedProviderFailureWriter struct {
	t       *testing.T
	workDir string
	finish  bool
}

func (w reapedProviderFailureWriter) Write(p []byte) (int, error) {
	if w.finish {
		if string(p) != "\n" {
			return len(p), nil
		}
	} else if !strings.Contains(string(p), "partial output") {
		return len(p), nil
	}
	pid := outputFailurePID(w.t, w.workDir, "provider")
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			w.t.Errorf("provider was not reaped before write failed")
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0, errOutputDestination
}

func TestRunTaskOutputFailureAfterProviderExit(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "ollama"} {
		for _, mode := range []string{"chunk", "finish"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				cfg, db, workDir, _ := outputFailureFixture(t, provider, "exit")
				task := &store.Task{ID: "exited-provider", Type: "skill", What: "output-failure", RunAt: time.Now(), Agent: provider, Isolation: "privileged", CWD: workDir}
				if err := db.CreateTask(task); err != nil {
					t.Fatal(err)
				}
				err := RunTaskTo(context.Background(), cfg, db, task, reapedProviderFailureWriter{t: t, workDir: workDir, finish: mode == "finish"})
				if !errors.Is(err, errOutputDestination) {
					t.Errorf("lost writer error: %v", err)
				}
				assertOutputFailureTreeStopped(t, workDir)
				assertOutputFailureReceipt(t, db, task.ID, errOutputDestination.Error())
			})
		}
	}
}

func TestRunTaskCLIBrokenPipe(t *testing.T) {
	if config.Channel() == "preview" {
		t.Skip("direct execution is unavailable in preview")
	}
	binary := filepath.Join(testsocket.Dir(t), "wt")
	build := exec.Command("go", "build", "-p", "2", "-o", binary, "./cmd/wt")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	for _, provider := range []string{"codex", "claude", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			cfg, db, workDir, _ := outputFailureFixture(t, provider, "pipe")
			t.Setenv("WINGTHING_DIR", cfg.Dir)
			t.Setenv("WINGTHING_PREVIEW_DIR", cfg.Dir)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "run", "--skill", "output-failure", "--agent", provider, "--unsandboxed")
			cmd.Dir = workDir
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			cmd.Stdout = writer
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = writer.Close()
			// Deadlines ensure a broken fixture cannot leave a blocked test reader.
			if err := reader.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
				t.Fatal(err)
			}
			var receivedOutput strings.Builder
			buf := make([]byte, 4096)
			received := false
			for {
				n, readErr := reader.Read(buf)
				receivedOutput.Write(buf[:n])
				if strings.Contains(receivedOutput.String(), "partial output") {
					received = true
					break
				}
				if readErr != nil {
					break
				}
			}
			_ = reader.Close()
			if err := os.WriteFile(filepath.Join(workDir, "release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			if !received {
				t.Fatalf("did not receive provider output: %s", stderr.String())
			}
			if ctx.Err() != nil {
				t.Fatalf("CLI failed to stop without provider timeout: %s", stderr.String())
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Errorf("expected ordinary CLI failure, got %v", err)
			}
			if !strings.Contains(stderr.String(), "broken pipe") {
				t.Errorf("missing output failure: %s", stderr.String())
			}
			assertOutputFailureTreeStopped(t, workDir)
			tasks, err := db.ListRecent(1)
			if err != nil {
				t.Fatal(err)
			}
			if len(tasks) != 1 {
				t.Fatalf("got %d tasks", len(tasks))
			}
			assertOutputFailureReceipt(t, db, tasks[0].ID, "broken pipe")
		})
	}
}

// Capturing a command must not change the provider's native approval policy,
// model, prompt or other arguments on the unsandboxed path.
func TestRunTaskDirectProviderArguments(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "ollama", "gemini", "opencode", "hermes", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			cfg, db, workDir, binDir := outputFailureFixture(t, provider, "exit")
			definition, _ := agent.LookupDefinition(provider)
			script := "#!/bin/sh\nprintf '%s\\0' \"$@\" > argv\n"
			if err := os.WriteFile(filepath.Join(binDir, definition.Command), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			task := &store.Task{ID: "direct-arguments", Type: "prompt", What: "--dangerously-bypass-approvals-and-sandbox --dangerously-skip-permissions --yolo --auto", RunAt: time.Now(), Agent: provider, Isolation: "privileged", CWD: workDir, Model: "test-model"}
			if provider == "cursor" {
				task.Model = ""
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := newAgent(provider).Run(ctx, task.What+"\n\n"+orchestrator.FormatDocs, agent.RunOpts{WorkDir: workDir, Model: task.Model})
			if err != nil {
				t.Fatal(err)
			}
			for {
				if _, ok := stream.Next(); !ok {
					break
				}
			}
			if err := stream.Err(); err != nil {
				t.Fatal(err)
			}
			native, err := os.ReadFile(filepath.Join(workDir, "argv"))
			if err != nil {
				t.Fatal(err)
			}
			if err := db.CreateTask(task); err != nil {
				t.Fatal(err)
			}
			if err := RunTaskTo(ctx, cfg, db, task, io.Discard); err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(filepath.Join(workDir, "argv"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(native, actual) {
				t.Fatalf("direct provider arguments changed:\nnative %q\ntask   %q", native, actual)
			}
		})
	}
}

func TestRunTaskDetachedOutputWriter(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Fatal("setsid is required for detached output coverage")
	}
	for _, provider := range []string{"codex", "claude", "ollama"} {
		for _, mode := range []string{"write-failure", "leader-exited", "parent-cancel"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				cfg, db, workDir, binDir := outputFailureFixture(t, provider, "exit")
				path := filepath.Join(binDir, provider)
				script, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				// Spawn before publishing output; keep stdout but close stderr so
				// exec's stderr-copy WaitDelay cannot release the parser's read.
				script = bytes.Replace(script, []byte("#!/bin/sh\n"), []byte(`#!/bin/sh
setsid /bin/sh -c 'echo $$ > detached.pid; exec sleep 600' 2>/dev/null &
while [ ! -s detached.pid ]; do sleep 0.01; done
`), 1)
				if mode != "leader-exited" {
					script = append(script, []byte("wait\n")...)
				}
				if err := os.WriteFile(path, script, 0700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if data, err := os.ReadFile(filepath.Join(workDir, "detached.pid")); err == nil {
						pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
						if pid > 0 {
							_ = syscall.Kill(pid, syscall.SIGKILL)
						}
					}
				})
				task := &store.Task{ID: "detached", Type: "skill", What: "output-failure", RunAt: time.Now(), Agent: provider, Isolation: "privileged", CWD: workDir}
				if err := db.CreateTask(task); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				destination := detachedOutputWriter{cancel: cancel, parentCancel: mode == "parent-cancel", workDir: workDir, leaderExited: mode == "leader-exited"}
				go func() { done <- RunTaskTo(ctx, cfg, db, task, destination) }()
				select {
				case err := <-done:
					message := errOutputDestination.Error()
					if mode == "parent-cancel" {
						if err == nil {
							t.Fatal("cancellation succeeded unexpectedly")
						}
						message = err.Error()
					} else if !errors.Is(err, errOutputDestination) {
						t.Fatalf("lost writer error: %v", err)
					}
					assertOutputFailureReceipt(t, db, task.ID, message)
				case <-time.After(8 * time.Second):
					cancel()
					t.Fatal("detached stdout blocked task failure")
				}
				assertOutputFailureTreeStopped(t, workDir)
				pid := outputFailurePID(t, workDir, "detached")
				deadline := time.Now().Add(2 * time.Second)
				for {
					stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
					_, state, _ := strings.Cut(string(stat), ") ")
					if errors.Is(err, os.ErrNotExist) || strings.HasPrefix(state, "Z ") || strings.HasPrefix(state, "X ") {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("detached child %d survived: %s, %v", pid, stat, err)
					}
					time.Sleep(10 * time.Millisecond)
				}
			})
		}
	}
}

type detachedOutputWriter struct {
	cancel       context.CancelFunc
	parentCancel bool
	workDir      string
	leaderExited bool
}

func (w detachedOutputWriter) Write(p []byte) (int, error) {
	if !strings.Contains(string(p), "partial output") {
		return len(p), nil
	}
	if w.leaderExited {
		// /proc distinguishes an exited-but-not-yet-reaped leader from one
		// still running; the parser cannot reap it until its pipe is closed.
		data, _ := os.ReadFile(filepath.Join(w.workDir, "provider.pid"))
		pid := strings.TrimSpace(string(data))
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			stat, err := os.ReadFile("/proc/" + pid + "/stat")
			_, state, _ := strings.Cut(string(stat), ") ")
			if errors.Is(err, os.ErrNotExist) || strings.HasPrefix(state, "Z ") {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if w.parentCancel {
		w.cancel()
		return len(p), nil
	}
	return 0, errOutputDestination
}

func TestRunTaskSuccessPreservesBackgroundChildren(t *testing.T) {
	cfg, db, workDir, _ := outputFailureFixture(t, "codex", "exit")
	task := &store.Task{ID: "successful-children", Type: "skill", What: "output-failure", RunAt: time.Now(), Agent: "codex", Isolation: "privileged", CWD: workDir}
	if err := db.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunTaskTo(context.Background(), cfg, db, task, &output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"child", "grandchild"} {
		pid := outputFailurePID(t, workDir, name)
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		_, state, _ := strings.Cut(string(stat), ") ")
		if err != nil || strings.HasPrefix(state, "Z ") || strings.HasPrefix(state, "X ") {
			t.Errorf("successful task stopped %s: %s, %v", name, stat, err)
		}
	}
	saved, err := db.GetTask(task.ID)
	if err != nil || saved.Status != "done" || saved.Error != nil {
		t.Fatalf("successful receipt changed: %#v, %v", saved, err)
	}
}
