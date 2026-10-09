package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestAgentCommandClassifiesAndDiscardsStderr(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "printf 'authentication failed: test credential missing' >&2; exit 7")
	diagnostics, err := startAgentCommand(cmd)
	if err != nil {
		t.Fatalf("startAgentCommand: %v", err)
	}
	err = waitAgentCommand(cmd, diagnostics, "fixture")
	if err == nil {
		t.Fatal("waitAgentCommand succeeded, want exit error")
	}
	if FailureKind(err) != AuthFailed || strings.Contains(err.Error(), "test credential missing") || diagnostics.stderr.String() != "" {
		t.Fatalf("stderr escaped classification: %v", err)
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
	if err := waitAgentCommand(cmd, diagnostics, "fixture"); err == nil {
		t.Fatal("waitAgentCommand succeeded after cancellation")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("process group cancellation took %s", elapsed)
	}
}
