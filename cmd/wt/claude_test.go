package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeWithoutWingFailsWithGuidance(t *testing.T) {
	scratch, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(scratch, "claude-none-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("HOME", root)
	t.Setenv("WINGTHING_DIR", filepath.Join(root, "absent"))
	t.Setenv("WT_MCP_CLIENT", "")
	cmd := newRootCommand()
	cmd.SetArgs([]string{"claude", "--name", "weekend", "--", "--model", "fake"})
	cmd.SetIn(bytes.NewReader(nil))
	cmd.SetOut(new(bytes.Buffer))
	err = cmd.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "wt wing start --local-only") {
		t.Fatalf("missing guidance: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "absent")); !os.IsNotExist(err) {
		t.Fatalf("CLI created state without a wing: %v", err)
	}
}
