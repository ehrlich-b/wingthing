package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/spf13/cobra"
)

// The seams are internal test dependencies, never public flags or environment
// overrides. Production always uses its own OS streams and platform checks.
type previewProviderLoginIO struct {
	in, out, errOut *os.File
	platform        string
	terminal        func(*os.File, *os.File, *os.File) error
	managedContext  func() error
	run             func(*exec.Cmd) error
}

func previewProviderLoginProcessIO() previewProviderLoginIO {
	return previewProviderLoginIO{in: os.Stdin, out: os.Stdout, errOut: os.Stderr, platform: runtime.GOOS,
		terminal: previewProviderLoginTerminal, managedContext: previewProviderLoginManagedContext, run: func(cmd *exec.Cmd) error { return cmd.Run() }}
}

func previewProviderLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use: "login claude", Short: "Sign in directly from a private user terminal", Args: previewClaudeArgument,
		Long: "Run the vendor's subscription login directly in a private controlling terminal. Requires prepared preview state. Refuses pipes and detected managed-agent contexts; Wingthing does not capture vendor streams. External terminal software and vendor tools may record their own output. macOS uses the same explicit OS context and config namespace as the matching preview runtime.",
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, err := resolvePreviewProviderProfile()
			if err != nil {
				return err
			}
			return runPreviewProviderLogin(cmd.Context(), profile, previewProviderLoginProcessIO())
		},
	}
}

func runPreviewProviderLogin(ctx context.Context, profile previewProviderProfile, streams previewProviderLoginIO) error {
	if config.Channel() != "preview" {
		return errors.New("provider login requires the preview executable")
	}
	if err := config.ValidateStateDirectory(profile.StateDir); err != nil {
		return err
	}
	// Re-resolve immediately before login so a binding changed since profile
	// resolution cannot redirect the vendor to another credential namespace.
	home, bound, err := config.ResolvePreviewProviderHome(profile.StateDir)
	if err != nil {
		return err
	}
	if home != profile.Home || bound != (profile.HomeBinding != "") {
		return errors.New("provider login data home changed after profile resolution; inspect provider setup-guide first")
	}
	required := []string{profile.StateDir, filepath.Join(profile.StateDir, ".release-channel"), filepath.Join(profile.StateDir, "provider-home"), profile.Home}
	if bound {
		required = []string{profile.StateDir, filepath.Join(profile.StateDir, ".release-channel"), profile.Home}
	}
	for _, path := range required {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !previewProviderLoginOwns(info) ||
			(path == filepath.Join(profile.StateDir, ".release-channel") && !info.Mode().IsRegular()) ||
			(path != filepath.Join(profile.StateDir, ".release-channel") && !info.IsDir()) {
			return errors.New("provider login requires already-prepared preview state with a regular preview marker and real provider home owned by this user; inspect provider setup-guide first")
		}
	}
	if info, err := os.Lstat(profile.ConfigDirectory); err == nil {
		if !info.IsDir() || !previewProviderLoginOwns(info) {
			return errors.New("provider login requires its exact configuration directory to be a real directory owned by this user")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("provider login cannot inspect the selected configuration directory")
	}
	if err := streams.terminal(streams.in, streams.out, streams.errOut); err != nil {
		return err
	}
	if err := streams.managedContext(); err != nil {
		return err
	}
	if streams.platform != "linux" && streams.platform != "darwin" {
		return errors.New("provider login terminal checks are supported only on Linux and macOS")
	}
	if profile.Executable == "" {
		return errors.New("Claude is not installed on PATH; no login was run")
	}
	command := exec.CommandContext(ctx, profile.Executable, "auth", "login", "--claudeai")
	command.Env, command.Dir = previewClaudeStatusEnv(profile), profile.Home
	// Actual *os.File streams go directly to the vendor process. No egg, PTY,
	// browser shim, hooks, MCP, transcript, log writer, or output buffer is used.
	command.Stdin, command.Stdout, command.Stderr = streams.in, streams.out, streams.errOut
	if err := streams.run(command); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			return &cmdutil.CommandExitError{Code: exitErr.ExitCode()}
		}
		if ctx.Err() != nil {
			return &cmdutil.CommandExitError{Code: 130}
		}
		return errors.New("the vendor login process could not execute or finish; its streams were not captured by Wingthing")
	}
	return nil
}

func previewProviderLoginManagedContext() error {
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
		if previewProviderLoginManagedProcess(strings.Join(fields[1:], " ")) {
			return errors.New("provider login refuses detected agent or Wingthing process ancestry; captured agent execution is unsuitable for login")
		}
		pid = parent
	}
	if pid > 1 {
		return errors.New("provider login could not verify complete process ancestry")
	}
	return nil
}

func previewProviderLoginManagedProcess(command string) bool {
	name := strings.ToLower(filepath.Base(command))
	switch name {
	case "wt", "wt-preview", "claude", "codex", "herdr", "aider", "opencode", "gemini", "cursor-agent", "codex-exec":
		return true
	}
	return strings.Contains(strings.ToLower(command), "codex.app/") || strings.Contains(strings.ToLower(command), "claude.app/")
}
