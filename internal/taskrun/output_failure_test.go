//go:build linux

package taskrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/store"
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
			cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "codex", WingID: "test-wing"}
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
			workDir := t.TempDir()
			binDir := t.TempDir()
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
			cfg := &config.Config{Dir: t.TempDir(), DefaultAgent: "codex", WingID: "test-wing"}
			db, err := store.Open(cfg.DBPath())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			binDir := t.TempDir()
			script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"partial output\"}}'\n"
			if mode == "provider-failure" {
				script += "echo 'provider failed' >&2\nexit 1\n"
			}
			if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+":/usr/bin:/bin")
			task := &store.Task{ID: "completed-provider", Type: "prompt", What: "Print output.", RunAt: time.Now(), Agent: "codex", Isolation: "privileged", CWD: t.TempDir()}
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
