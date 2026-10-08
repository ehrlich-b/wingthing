package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCodexReturnsExactThreadAndExistingRollout(t *testing.T) {
	const thread = "01998952-827c-7000-8000-123456789abc"
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	dir := filepath.Join(home, "sessions", startedAt.Format("2006/01/02"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-10-08T12-00-00-"+thread+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"`+thread+`","base_instructions":{"text":"`+strings.Repeat("instruction ", 2048)+`"}}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stream, err := NewCodex(0).Run(context.Background(), "review", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%s\n' '{"type":"thread.started","thread_id":"`+thread+`"}' '{"type":"turn.failed","error":{"message":"unknown failure"}}'`)
		cmd.Env = []string{"CODEX_HOME=" + home, "HOME=" + t.TempDir()}
		return cmd, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, ok := stream.Next(); !ok {
			break
		}
	}
	id, rollout := stream.ProviderSession()
	if id != thread || rollout != path || FailureKind(stream.Err()) != ProviderError {
		t.Fatalf("provider session = %q, %q, %v", id, rollout, stream.Err())
	}
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"other-thread"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := codexRolloutPath(home, thread, startedAt); got != "" {
		t.Fatalf("mismatched rollout returned: %q", got)
	}
	if got := codexRolloutPath(home, "missing-thread", startedAt); got != "" {
		t.Fatalf("missing rollout guessed: %q", got)
	}
}

func TestCodexValidatesThreadID(t *testing.T) {
	const thread = "01998952-827c-7000-8000-123456789abc"
	for _, tc := range []struct {
		name, id string
		valid    bool
	}{
		{"lowercase", thread, true},
		{"uppercase", strings.ToUpper(thread), true},
		{"provider-text", "provider-error-canary", false},
		{"empty", "", false},
		{"compact", strings.ReplaceAll(thread, "-", ""), false},
		{"braced", "{" + thread + "}", false},
		{"urn", "urn:uuid:" + thread, false},
		{"whitespace", thread + " ", false},
		{"nonhex", thread[:35] + "z", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			data, err := json.Marshal(map[string]string{"type": "thread.started", "thread_id": tc.id})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "stdout")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			stream, err := NewCodex(0).Run(context.Background(), "review", RunOpts{CmdFactory: func(ctx context.Context, _ string, _ []string) (*exec.Cmd, error) {
				cmd := exec.CommandContext(ctx, "cat", path)
				cmd.Env = []string{"CODEX_HOME=" + home}
				return cmd, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if tc.valid {
				want = tc.id
			}
			for {
				chunk, ok := stream.Next()
				if !ok {
					break
				}
				if chunk.ThreadID != want {
					t.Errorf("thread chunk = %q, want %q", chunk.ThreadID, want)
				}
			}
			id, rollout := stream.ProviderSession()
			if id != want || rollout != "" || stream.Err() != nil {
				t.Fatalf("provider session = %q, %q, %v", id, rollout, stream.Err())
			}
		})
	}
}

func TestCodexRolloutRejectsAmbiguityAndSymlinks(t *testing.T) {
	const thread = "01998952-827c-7000-8000-123456789abc"
	home := t.TempDir()
	startedAt := time.Now()
	dir := filepath.Join(home, "sessions", startedAt.Format("2006/01/02"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "rollout-"+thread+".jsonl")
	meta := []byte(`{"type":"session_meta","payload":{"id":"` + thread + `"}}` + "\n")
	if err := os.WriteFile(outside, meta, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "rollout-link-"+thread+".jsonl")); err != nil {
		t.Fatal(err)
	}
	if got := codexRolloutPath(home, thread, startedAt); got != "" {
		t.Fatalf("outside rollout returned: %q", got)
	}
	for _, name := range []string{"rollout-one-" + thread + ".jsonl", "rollout-two-" + thread + ".jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), meta, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got := codexRolloutPath(home, thread, startedAt); got != "" {
		t.Fatalf("ambiguous rollout returned: %q", got)
	}
}

func TestCodexRolloutOpenRejectsSwappedSymlinkAndFIFO(t *testing.T) {
	const thread = "01998952-827c-7000-8000-123456789abc"
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-"+thread+".jsonl")
	meta := []byte(`{"type":"session_meta","payload":{"id":"` + thread + `"}}` + "\n")
	if err := os.WriteFile(path, meta, 0600); err != nil {
		t.Fatal(err)
	}
	entry, err := os.Lstat(path)
	if err != nil || !entry.Mode().IsRegular() {
		t.Fatalf("original candidate: %v %v", entry, err)
	}
	if err := os.Rename(path, filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), path); err != nil {
		t.Fatal(err)
	}
	if codexRolloutMatchesThread(path, thread) {
		t.Fatal("opened a symlink swapped in after candidate inspection")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	result := make(chan bool, 1)
	go func() { result <- codexRolloutMatchesThread(path, thread) }()
	select {
	case matched := <-result:
		if matched {
			t.Fatal("FIFO was accepted as a rollout")
		}
	case <-time.After(time.Second):
		// Unblock a regressed blocking open so the fixture does not leak it.
		fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK, 0)
		if err == nil {
			_ = unix.Close(fd)
		}
		t.Fatal("rollout open blocked on a FIFO")
	}
	if codexRolloutMatchesThread(dir, thread) {
		t.Fatal("directory accepted as a rollout")
	}
}

func TestCodexRolloutHeaderLimit(t *testing.T) {
	const thread = "01998952-827c-7000-8000-123456789abc"
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	prefix := `{"type":"session_meta","payload":{"id":"` + thread + `","instructions":"`
	suffix := `"}}` + "\n"
	for _, size := range []int{maxCodexRolloutHeader, maxCodexRolloutHeader + 1} {
		if err := os.WriteFile(path, []byte(sizedEvent(prefix, suffix, size)), 0600); err != nil {
			t.Fatal(err)
		}
		if got := codexRolloutMatchesThread(path, thread); got != (size == maxCodexRolloutHeader) {
			t.Fatalf("header size %d accepted = %v", size, got)
		}
	}
}

func TestCodexRolloutSearchUsesLocalRunDatesAndDeadline(t *testing.T) {
	const thread = "01998952-827c-7000-8000-123456789abc"
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Cross local midnight, including the DST boundary. UTC dates do not
	// necessarily match Codex's local YYYY/MM/DD directories.
	start := time.Date(2026, 3, 7, 23, 59, 0, 0, time.Local)
	now := start.Add(2 * time.Minute)
	write := func(day time.Time, name string) string {
		t.Helper()
		dir := filepath.Join(home, "sessions", day.Format("2006/01/02"))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "rollout-"+name+"-"+thread+".jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"`+thread+`"}}`+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write(start.AddDate(0, 0, -1), "old")
	write(now.AddDate(0, 0, 1), "future")
	path := write(now, "current")
	if got := findCodexRollout(context.Background(), home, thread, start.UTC(), now.UTC()); got != path {
		t.Fatalf("local run date lookup = %q, want %q", got, path)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := findCodexRollout(ctx, home, thread, start, now); got != "" {
		t.Fatalf("expired lookup returned a path: %q", got)
	}
	write(start, "started")
	if got := findCodexRollout(context.Background(), home, thread, start, now); got != "" {
		t.Fatalf("ambiguity across run dates returned a path: %q", got)
	}
}
