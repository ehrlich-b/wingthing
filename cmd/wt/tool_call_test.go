package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func TestToolCallOwnHelpAndMissingTool(t *testing.T) {
	cmd := toolCallCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("tool-call --help: %v", err)
	}
	if !strings.Contains(output.String(), "Call a privileged tool") {
		t.Fatalf("tool-call --help output = %q", output.String())
	}

	cmd = toolCallCmd()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires at least 1 arg") {
		t.Fatalf("tool-call without a tool error = %v", err)
	}
}

func TestExecuteCLIPrivilegedToolPreservesNativeArgsAndAuthority(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "wt-tool-call-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	wantArgs := []string{
		"--state",
		"-r",
		"owner",
		"--remote",
		"literal",
		"--help",
		"",
		"quote'\" value",
		" \t ",
	}
	expectEnv := make(map[string]string, len(wantArgs))
	checks := []string{`test "$#" -eq 9`}
	for i, value := range wantArgs {
		position := strconv.Itoa(i + 1)
		name := "EXPECT_" + position
		expectEnv[name] = value
		checks = append(checks, `test "$`+position+`" = "$`+name+`"`)
	}

	sockPath := filepath.Join(dir, "tool.sock")
	listener, err := egg.NewToolListener(sockPath, []*config.ToolConfig{{
		Name:    "argv-canary",
		Run:     strings.Join(checks, " && "),
		Env:     expectEnv,
		Timeout: "5s",
	}})
	if err != nil {
		t.Fatalf("start tool listener: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	t.Setenv("WT_TOOL_SOCKET", sockPath)

	args := append([]string{"tool-call", "argv-canary"}, wantArgs...)
	if err := executeCLI(context.Background(), args, remotepkg.IO{}); err != nil {
		t.Fatalf("tool-call exact argv: %v", err)
	}

	err = executeCLI(context.Background(), []string{"tool-call", "unconfigured", "--help"}, remotepkg.IO{})
	var exitErr *cmdutil.CommandExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 || exitErr.Message != "unknown tool: unconfigured" {
		t.Fatalf("unconfigured tool error = %#v, want exit 1 unknown-tool denial", err)
	}
}

func TestExecuteCLIDoesNotExposeToolCallOverRemote(t *testing.T) {
	err := executeCLI(context.Background(), []string{"--remote", "example", "tool-call", "argv-canary"}, remotepkg.IO{})
	if err == nil || !strings.Contains(err.Error(), `command "tool-call" is not available over SSH`) {
		t.Fatalf("remote tool-call error = %v, want unsupported command", err)
	}
}
