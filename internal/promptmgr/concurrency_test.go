package promptmgr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type processRequest struct {
	Dir, Expected, Template string
	HoldLock, Cancel        bool
	TimeoutMS               int
}

type processResult struct {
	Revision, Error string
}

// Each child has its own Store and address space. The pipe barrier starts all
// writers together; a substantial template keeps the old check/write race open.
func TestCompetingProcessWriters(t *testing.T) {
	store := New(t.TempDir())
	initial, err := store.Save(Asset{Name: "review", Template: "original"}, "")
	if err != nil {
		t.Fatal(err)
	}
	const writers = 12
	children := make([]*promptProcess, writers)
	for i := range children {
		children[i] = startPromptProcess(t, processRequest{
			Dir: store.dir, Expected: initial.Revision,
			Template: strings.Repeat("instructions ", 32*1024) + fmt.Sprint(i),
		})
	}
	for _, child := range children {
		child.start(t)
	}
	accepted := 0
	var winner string
	for _, child := range children {
		result := child.result(t)
		if result.Error == "" {
			accepted++
			winner = result.Revision
		} else if !strings.Contains(result.Error, "revision conflict") {
			t.Errorf("unexpected writer error: %s", result.Error)
		}
	}
	if accepted != 1 {
		t.Fatalf("%d independent processes accepted the same expected revision; want 1", accepted)
	}
	current, err := store.Get("review", "")
	if err != nil || current.Revision != winner || current.CreatedAt != initial.CreatedAt {
		t.Fatalf("current = %+v, err = %v; winner = %s", current, err, winner)
	}
	for _, revision := range []string{initial.Revision, winner} {
		if _, err := store.Get("review", revision); err != nil {
			t.Errorf("history %s: %v", revision, err)
		}
	}
	entries, err := os.ReadDir(store.dir + "/_history/review")
	if err != nil || len(entries) != 2 {
		t.Fatalf("history has %d entries, err = %v; want 2", len(entries), err)
	}
}

func TestForceProcessWritersPreserveHistory(t *testing.T) {
	store := New(t.TempDir())
	initial, err := store.Save(Asset{Name: "review", Template: "original"}, "")
	if err != nil {
		t.Fatal(err)
	}
	const writers = 8
	children := make([]*promptProcess, writers)
	for i := range children {
		children[i] = startPromptProcess(t, processRequest{
			Dir: store.dir, Template: fmt.Sprintf("forced instructions %d", i),
		})
	}
	for _, child := range children {
		child.start(t)
	}
	for i, child := range children {
		result := child.result(t)
		if result.Error != "" {
			t.Fatalf("force writer %d: %s", i, result.Error)
		}
		historical, err := store.Get("review", result.Revision)
		if err != nil || historical.Template != fmt.Sprintf("forced instructions %d", i) || historical.CreatedAt != initial.CreatedAt {
			t.Fatalf("force writer %d history = %+v, err = %v", i, historical, err)
		}
	}
	if _, err := store.Get("review", initial.Revision); err != nil {
		t.Fatal(err)
	}
	current, err := store.Get("review", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("review", current.Revision); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(store.dir, "_history", "review"))
	if err != nil || len(entries) != writers+1 {
		t.Fatalf("history has %d entries, err = %v; want %d", len(entries), err, writers+1)
	}
}

func TestProcessLockCancellationAndCrashRecovery(t *testing.T) {
	store := New(t.TempDir())
	initial, err := store.Save(Asset{Name: "review", Template: "original"}, "")
	if err != nil {
		t.Fatal(err)
	}
	holder := startPromptProcess(t, processRequest{Dir: store.dir, HoldLock: true})
	for _, cancel := range []bool{false, true} {
		child := startPromptProcess(t, processRequest{
			Dir: store.dir, Expected: initial.Revision, Template: "must not save",
			TimeoutMS: 100, Cancel: cancel,
		})
		child.start(t)
		result := child.result(t)
		want := context.DeadlineExceeded.Error()
		if cancel {
			want = context.Canceled.Error()
		}
		if !strings.Contains(result.Error, want) {
			t.Fatalf("waiting writer error = %q; want %s", result.Error, want)
		}
	}
	// Save's existing API must also have a finite wait even without a caller context.
	if _, err := store.Save(Asset{Name: "review", Template: "must not save"}, initial.Revision); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Save while locked = %v; want deadline exceeded", err)
	}
	// A different prompt does not wait for review's lock.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := store.SaveContext(ctx, Asset{Name: "other", Template: "independent"}, ""); err != nil {
		t.Fatal(err)
	}
	if current, err := store.Get("review", ""); err != nil || current.Revision != initial.Revision {
		t.Fatalf("cancelled writers changed current: %+v, %v", current, err)
	}
	entries, err := os.ReadDir(filepath.Join(store.dir, "_history", "review"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("cancelled writers changed history: %d entries, %v", len(entries), err)
	}
	if err := holder.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := holder.cmd.Wait(); err == nil {
		t.Fatal("lock holder unexpectedly exited cleanly")
	}
	// The persistent lock file remains; the kernel must release its lock on death.
	if _, err := os.Stat(filepath.Join(store.dir, ".review.lock")); err != nil {
		t.Fatal(err)
	}
	restarted := startPromptProcess(t, processRequest{
		Dir: store.dir, Expected: initial.Revision, Template: "after crash", TimeoutMS: 1000,
	})
	restarted.start(t)
	result := restarted.result(t)
	if result.Error != "" {
		t.Fatalf("restart save: %s", result.Error)
	}
	for _, revision := range []string{initial.Revision, result.Revision} {
		if _, err := store.Get("review", revision); err != nil {
			t.Fatal(err)
		}
	}
	if current, err := store.Get("review", ""); err != nil || current.Revision != result.Revision {
		t.Fatalf("restart current = %+v, %v", current, err)
	}
}

func TestCancelledSaveDoesNotWrite(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "prompts"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.SaveContext(ctx, Asset{Name: "review", Template: "cancelled"}, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled SaveContext = %v", err)
	}
	if _, err := os.Stat(store.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled save created directory: %v", err)
	}
}

type promptProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	stderr bytes.Buffer
}

func startPromptProcess(t *testing.T, request processRequest) *promptProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	child := &promptProcess{cmd: exec.CommandContext(ctx, executable, "-test.run=^TestPromptProcessHelper$")}
	child.cmd.Env = append(os.Environ(), "PROMPTMGR_PROCESS_HELPER=1")
	child.cmd.Stderr = &child.stderr
	child.input, err = child.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.output = bufio.NewReader(output)
	if err := child.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.cmd.Process.Kill(); _ = child.cmd.Wait() })
	if err := json.NewEncoder(child.input).Encode(request); err != nil {
		t.Fatal(err)
	}
	if line, err := child.output.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child readiness = %q, err = %v", line, err)
	}
	return child
}

func (p *promptProcess) start(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(p.input, "start\n"); err != nil {
		t.Fatal(err)
	}
}

func (p *promptProcess) result(t *testing.T) processResult {
	t.Helper()
	var result processResult
	if err := json.NewDecoder(p.output).Decode(&result); err != nil {
		t.Fatalf("child result: %v", err)
	}
	if err := p.cmd.Wait(); err != nil {
		t.Fatalf("child exit: %v: %s", err, p.stderr.String())
	}
	return result
}

func TestPromptProcessHelper(t *testing.T) {
	if os.Getenv("PROMPTMGR_PROCESS_HELPER") != "1" {
		return
	}
	input := bufio.NewReader(os.Stdin)
	line, err := input.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var request processRequest
	if err := json.Unmarshal(line, &request); err != nil {
		t.Fatal(err)
	}
	if request.HoldLock {
		lock, err := acquirePromptLock(context.Background(), filepath.Join(request.Dir, ".review.lock"))
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		fmt.Println("ready")
		_, _ = input.ReadString('\n')
		return
	}
	fmt.Println("ready")
	if _, err := input.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if request.Cancel {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		timer := time.AfterFunc(time.Duration(request.TimeoutMS)*time.Millisecond, cancel)
		defer timer.Stop()
	} else if request.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(request.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	saved, err := New(request.Dir).SaveContext(ctx, Asset{Name: "review", Template: request.Template}, request.Expected)
	var result processResult
	if err != nil {
		result.Error = err.Error()
	} else {
		result.Revision = saved.Revision
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
