package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexReturnsExactThreadAndExistingRollout(t *testing.T) {
	const thread = "01998952-827c-7000-8000-123456789abc"
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "sessions", "2026", "10", "08")
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
	if got := codexRolloutPath(home, thread); got != "" {
		t.Fatalf("mismatched rollout returned: %q", got)
	}
	if got := codexRolloutPath(home, "missing-thread"); got != "" {
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
