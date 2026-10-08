package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentCommandErrorIncludesStderr(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "printf 'authentication failed: test credential missing' >&2; exit 7")
	diagnostics, err := startAgentCommand(cmd)
	if err != nil {
		t.Fatalf("startAgentCommand: %v", err)
	}
	err = waitAgentCommand(cmd, diagnostics)
	if err == nil {
		t.Fatal("waitAgentCommand succeeded, want exit error")
	}
	if !strings.Contains(err.Error(), "authentication failed: test credential missing") {
		t.Fatalf("error %q does not include stderr", err)
	}
}

func TestCappedBufferRetainsPrefix(t *testing.T) {
	buffer := cappedBuffer{limit: 5}
	n, err := buffer.Write([]byte("abcdefgh"))
	if err != nil || n != 8 {
		t.Fatalf("Write = (%d, %v), want (8, nil)", n, err)
	}
	if got := buffer.String(); got != "abcde" {
		t.Fatalf("String = %q, want abcde", got)
	}
}

func TestAgentCommandCancellationStopsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 60 & wait")
	diagnostics, err := startAgentCommand(cmd)
	if err != nil {
		t.Fatalf("startAgentCommand: %v", err)
	}

	started := time.Now()
	cancel()
	if err := waitAgentCommand(cmd, diagnostics); err == nil {
		t.Fatal("waitAgentCommand succeeded after cancellation")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("process group cancellation took %s", elapsed)
	}
}

func TestAgentCommandRedactsBeforeStderrCap(t *testing.T) {
	const canary = "7Qn3-stderr-credential-canary"
	path := filepath.Join(t.TempDir(), "stderr")
	data := strings.Repeat("x", maxAgentStderr-8) + canary + strings.Repeat("x", maxAgentStderr)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", `cat "$1" >&2; exit 7`, "fixture", path)
	// The credential was supplied through an egg helper, outside the env.
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	diagnostics, err := startAgentCommand(cmd, canary)
	if err != nil {
		t.Fatal(err)
	}
	err = waitAgentCommand(cmd, diagnostics)
	if err == nil || strings.Contains(err.Error(), canary[:8]) || len(err.Error()) > maxAgentStderr+64 {
		t.Fatal("stderr truncation exposed a credential prefix or lost its bound")
	}
	assertProviderErrorChainRedacted(t, err, canary[:8])
}
