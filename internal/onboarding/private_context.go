package onboarding

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func UserTerminalContext() error {
	for _, name := range []string{"WT_SESSION_ID", "WT_SESSION_DIR", "WT_TOOL_SOCKET", "WT_MCP_CLIENT", "CLAUDECODE", "CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE"} {
		if os.Getenv(name) != "" {
			return errors.New("provider login refuses detected Wingthing or managed-agent context; run it directly in a private user terminal")
		}
	}
	if strings.Contains(os.Getenv("BROWSER"), "wt-browser") {
		return errors.New("provider login refuses the Wingthing browser shim context")
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Lstat(filepath.Join(dir, "wt-browser")); err == nil {
			return errors.New("provider login refuses a PATH containing a Wingthing browser shim")
		}
	}
	// Inspect command names and parent IDs only, never argv or environment (which
	// can contain credentials). Fail closed if the ancestry cannot be checked.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pid := os.Getppid()
	for depth := 0; pid > 1 && depth < 32; depth++ {
		command := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "ppid=", "-o", "comm=")
		command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		metadata, err := command.Output()
		fields := strings.Fields(string(metadata))
		if err != nil || len(fields) < 2 {
			return errors.New("provider login cannot verify parent process metadata; use a private user terminal")
		}
		parent, err := strconv.Atoi(fields[0])
		if err != nil || parent < 0 || parent == pid {
			return errors.New("provider login cannot verify process ancestry")
		}
		if ManagedProcess(strings.Join(fields[1:], " ")) {
			return errors.New("provider login refuses detected agent or Wingthing process ancestry; captured agent execution is unsuitable for login")
		}
		pid = parent
	}
	if pid > 1 {
		return errors.New("provider login could not verify complete process ancestry")
	}
	return nil
}

func ManagedProcess(command string) bool {
	name := strings.ToLower(filepath.Base(command))
	switch name {
	case "wt", "wt-preview", "claude", "codex", "herdr", "aider", "opencode", "gemini", "cursor-agent", "codex-exec":
		return true
	}
	return strings.Contains(strings.ToLower(command), "codex.app/") || strings.Contains(strings.ToLower(command), "claude.app/")
}
