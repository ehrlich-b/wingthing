package testprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

// CompletionGate holds exactly one fake Codex turn in its granted workspace.
type CompletionGate struct {
	path     string
	file     *os.File
	ready    bool
	released bool
}

func NewCompletionGate(t *testing.T, workspace string) *CompletionGate {
	t.Helper()
	path := filepath.Join(workspace, ".fixture-completion-gate")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// Keep both FIFO ends open so startup never blocks this test and the fake
	// never mistakes an absent writer for release. Only the fake reads bytes.
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return &CompletionGate{path: path, file: file}
}

// WaitReady waits for the fake's marker, published after UserPromptSubmit and
// opening its own FIFO. Unlike a shared writer open, this accounts for each fake.
func (g *CompletionGate) WaitReady(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(g.path)); err != nil {
		return err
	}
	for {
		if _, err := os.Stat(g.path + ".ready"); err == nil {
			g.ready = true
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-watcher.Events:
		case err := <-watcher.Errors:
			return err
		case <-ctx.Done():
			return fmt.Errorf("waiting for fake at %s: %w", g.path, ctx.Err())
		}
	}
}

func (g *CompletionGate) Release() error {
	if !g.ready || g.released {
		return fmt.Errorf("fake at %s must be ready and released exactly once", g.path)
	}
	if _, err := g.file.Write([]byte{1}); err != nil {
		return err
	}
	g.released = true
	return nil
}

// WaitParentMailboxExit waits for the broker to observe the parent's egg exit,
// so survival assertions run after its mailbox cancellation, not just detach.
func WaitParentMailboxExit(ctx context.Context, mailbox, session string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	if err := watcher.Add(mailbox); err != nil {
		return err
	}
	var ready struct {
		SessionID string `json:"session_id"`
		HostReady bool   `json:"host_ready"`
		Reason    string `json:"reason"`
	}
	for {
		data, err := os.ReadFile(filepath.Join(mailbox, "host-ready.json"))
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &ready); err != nil {
			return err
		}
		if ready.SessionID != session {
			return fmt.Errorf("mailbox belongs to %q, expected parent %q", ready.SessionID, session)
		}
		if !ready.HostReady && ready.Reason == "parent execution ended" {
			return nil
		}
		select {
		case <-watcher.Events:
		case err := <-watcher.Errors:
			return err
		case <-ctx.Done():
			return fmt.Errorf("waiting for parent mailbox exit: %+v: %w", ready, ctx.Err())
		}
	}
}

// LogEggTails captures fixture evidence before cleanup can stop or reap eggs.
func LogEggTails(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		for _, name := range []string{"egg.log", "lifecycle.jsonl"} {
			path := filepath.Join(dir, name)
			tail, err := fileTail(path)
			if err != nil {
				t.Logf("%s: %v", path, err)
				continue
			}
			t.Logf("%s tail:\n%s", path, tail)
		}
	}
}

func fileTail(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	const maxBytes = 32 << 10
	if info.Size() > maxBytes {
		if _, err := file.Seek(-maxBytes, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	tail, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(bytes.TrimSuffix(tail, []byte("\n")), []byte("\n"))
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return bytes.Join(lines, []byte("\n")), nil
}
