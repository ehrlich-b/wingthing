package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCodexReturnsExactThreadAndExistingRollout(t *testing.T) {
	const thread = "01998952-827c-7000-8000-123456789abc"
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "10", "08")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-10-08T12-00-00-"+thread+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"`+thread+`"}}`+"\n"), 0600); err != nil {
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
	if got := codexRolloutPath(home, thread); got != "" {
		t.Fatalf("mismatched rollout returned: %q", got)
	}
	if got := codexRolloutPath(home, "missing-thread"); got != "" {
		t.Fatalf("missing rollout guessed: %q", got)
	}
}

func TestCodexRolloutRejectsAmbiguityAndSymlinks(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "rollout-thread.jsonl")
	meta := []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread\"}}\n")
	if err := os.WriteFile(outside, meta, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "rollout-link-thread.jsonl")); err != nil {
		t.Fatal(err)
	}
	if got := codexRolloutPath(home, "thread"); got != "" {
		t.Fatalf("outside rollout returned: %q", got)
	}
	for _, name := range []string{"rollout-one-thread.jsonl", "rollout-two-thread.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), meta, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got := codexRolloutPath(home, "thread"); got != "" {
		t.Fatalf("ambiguous rollout returned: %q", got)
	}
}
