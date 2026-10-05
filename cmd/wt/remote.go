package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// remoteInvocation.state is a lexical absolute path on the remote host. It is
// never resolved locally: the receiving executable owns its own state checks.
type remoteInvocation struct {
	target      string
	binary      string
	cwd         string
	state       string
	args        []string
	allocateTTY bool
}

type remoteIO struct {
	in        io.Reader
	out       io.Writer
	errOut    io.Writer
	stdinTTY  bool
	stdoutTTY bool
	sshPath   string
}

func remoteProcessIO() remoteIO {
	return remoteIO{
		in: os.Stdin, out: os.Stdout, errOut: os.Stderr,
		stdinTTY:  term.IsTerminal(int(os.Stdin.Fd())),
		stdoutTTY: term.IsTerminal(int(os.Stdout.Fd())),
		sshPath:   "ssh",
	}
}

func executeCLI(ctx context.Context, args []string, streams remoteIO) error {
	var err error
	args, err = channelInvocationArgs(args)
	if err != nil {
		return err
	}
	if err := validatePreviewInvocation(args); err != nil {
		return err
	}
	// tool-call is an internal transport for generated privileged-tool shims.
	// Everything after it belongs to the native tool, including values that look
	// like Wingthing's global remote flags.
	if len(args) == 0 || args[0] != "tool-call" {
		invocation, remote, err := parseRemoteInvocation(args, streams.stdinTTY && streams.stdoutTTY)
		if err != nil {
			return err
		}
		if remote {
			return runRemoteInvocation(ctx, invocation, streams)
		}
	}
	root := newRootCommand()
	root.SetArgs(args)
	root.SetIn(streams.in)
	root.SetOut(streams.out)
	root.SetErr(streams.errOut)
	return root.ExecuteContext(context.WithValue(ctx, remoteIOContextKey{}, streams))
}

func parseRemoteInvocation(argv []string, interactive bool) (remoteInvocation, bool, error) {
	invocation := remoteInvocation{binary: config.BinaryName()}
	command, _, _ := newRootCommand().Find(argv)
	args := make([]string, 0, len(argv))
	transportSeen := false
	stateSeen := false
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			args = append(args, argv[i:]...)
			break
		}

		name, value, inline, matched := remoteTransportFlag(arg)
		if !matched {
			args = append(args, arg)
			if remoteCommandFlagConsumesValue(command, arg) && i+1 < len(argv) {
				i++
				args = append(args, argv[i])
			}
			continue
		}
		transportSeen = true
		if !inline {
			i++
			if i >= len(argv) {
				return remoteInvocation{}, false, fmt.Errorf("%s requires a value", name)
			}
			value = argv[i]
		}
		switch name {
		case "--remote":
			if invocation.target != "" {
				return remoteInvocation{}, false, errors.New("--remote may be specified only once")
			}
			invocation.target = value
		case "--remote-binary":
			invocation.binary = value
		case "--remote-cwd":
			invocation.cwd = value
		case "--remote-state":
			if stateSeen {
				return remoteInvocation{}, false, errors.New("--remote-state may be specified only once")
			}
			stateSeen = true
			invocation.state = value
		}
	}

	if !transportSeen {
		return remoteInvocation{}, false, nil
	}
	if invocation.target == "" {
		return remoteInvocation{}, false, errors.New("--remote requires a non-empty SSH host or alias")
	}
	if err := validateRemoteTarget(invocation.target); err != nil {
		return remoteInvocation{}, false, err
	}
	if err := validateRemoteBinary(invocation.binary); err != nil {
		return remoteInvocation{}, false, err
	}
	if strings.IndexByte(invocation.cwd, 0) >= 0 {
		return remoteInvocation{}, false, errors.New("remote working directory contains a NUL byte")
	}
	if stateSeen {
		if err := validateRemoteState(invocation.state); err != nil {
			return remoteInvocation{}, false, err
		}
	}
	bareEntry := len(args) == 0 || remoteJSONOnly(args)
	if invocation.cwd != "" && !bareEntry {
		return remoteInvocation{}, false, errors.New("--remote-cwd is only valid with bare 'wt --remote HOST'; use the command's --cwd flag for launches")
	}

	if bareEntry {
		invocation.args = []string{"_remote", "enter"}
		invocation.args = append(invocation.args, args...)
		if invocation.cwd != "" {
			invocation.args = append(invocation.args, "--cwd", invocation.cwd)
		}
		if !interactive {
			invocation.args = append(invocation.args, "--json")
		}
		invocation.allocateTTY = interactive && !boolFlagBeforeDash(invocation.args[2:], "json", "")
		return invocation, true, nil
	}
	if err := validateRemoteCommand(args); err != nil {
		return remoteInvocation{}, false, err
	}
	invocation.args = args
	invocation.allocateTTY = interactive && remoteCommandNeedsTTY(args)
	return invocation, true, nil
}

// Transport flags may appear anywhere before --, but a value consumed by a
// command flag belongs to that command even when it looks like -r or --remote.
// Use Cobra's definitions so new command flags inherit the same routing rule.
func remoteCommandFlagConsumesValue(command *cobra.Command, arg string) bool {
	flags := command.Flags()
	flags.AddFlagSet(command.InheritedFlags())
	if name, ok := strings.CutPrefix(arg, "--"); ok {
		if strings.Contains(name, "=") {
			return false
		}
		flag := flags.Lookup(name)
		return flag != nil && flag.NoOptDefVal == ""
	}
	if !strings.HasPrefix(arg, "-") {
		return false
	}
	for index := 1; index < len(arg); index++ {
		flag := flags.ShorthandLookup(arg[index : index+1])
		if flag == nil {
			return false
		}
		if flag.NoOptDefVal == "" {
			return index == len(arg)-1
		}
		if index+1 < len(arg) && arg[index+1] == '=' {
			return false
		}
	}
	return false
}

func remoteJSONOnly(args []string) bool {
	for _, arg := range args {
		if arg != "--json" && !strings.HasPrefix(arg, "--json=") {
			return false
		}
	}
	return len(args) > 0
}

func remoteTransportFlag(arg string) (name, value string, inline, matched bool) {
	switch arg {
	case "--remote", "-r":
		return "--remote", "", false, true
	case "--remote-binary", "--remote-cwd", "--remote-state":
		return arg, "", false, true
	}
	for _, name := range []string{"--remote", "--remote-binary", "--remote-cwd", "--remote-state"} {
		if value, ok := strings.CutPrefix(arg, name+"="); ok {
			return name, value, true, true
		}
	}
	if value, ok := strings.CutPrefix(arg, "-r="); ok {
		return "--remote", value, true, true
	}
	if strings.HasPrefix(arg, "-r") && len(arg) > 2 {
		return "--remote", arg[2:], true, true
	}
	return "", "", false, false
}

func validateRemoteTarget(target string) error {
	return config.ValidateSSHTarget(target)
}

func validateRemoteBinary(binary string) error {
	if binary == "" || strings.ContainsAny(binary, "\r\n\x00") {
		return errors.New("invalid remote binary")
	}
	return nil
}

// validateRemoteState accepts only a POSIX absolute path for the remote host.
// It deliberately avoids filepath.Abs, Clean, and EvalSymlinks: the client's
// filesystem says nothing about the remote one, and the receiving executable
// applies its own overlap, alias, and channel-marker checks before any write.
//
// As with --remote-binary, NUL cannot reach an
// environment value, and CR or LF would split the one-line remote command for
// SSH logs and non-POSIX login shells. Every other byte, including tab and
// other control bytes, is legal in a POSIX path and stays inert inside the
// single-quoted assignment, so it is passed through literally.
func validateRemoteState(state string) error {
	if state == "" {
		return errors.New("--remote-state requires a non-empty absolute path on the remote host")
	}
	if strings.ContainsAny(state, "\r\n\x00") {
		return errors.New("--remote-state must not contain NUL, CR, or LF")
	}
	if !strings.HasPrefix(state, "/") {
		return fmt.Errorf("--remote-state must be an absolute path on the remote host, got %q", state)
	}
	return nil
}

func validateRemoteCommand(args []string) error {
	command := args[0]
	switch command {
	case "egg", "sandbox":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("%s requires an explicit provider immediately after the command", command)
		}
		if args[1] == "run" {
			return fmt.Errorf("%s subcommand %q is not available over SSH", command, args[1])
		}
		return nil
	case "terminal", "new", "attach", "channel", "help", "--help", "-h", "--version":
		return nil
	case "session":
		if len(args) == 1 || remoteSessionCommand(args[1]) {
			return nil
		}
		return fmt.Errorf("session subcommand %q is not available over SSH; supported subcommands: list, ps, active, read, send, wait, rename, kill, and stop", args[1])
	}
	return fmt.Errorf("command %q is not available over SSH; supported commands: egg, terminal, attach, session, and channel", command)
}

func remoteSessionCommand(command string) bool {
	switch command {
	case "list", "ps", "active", "read", "send", "wait", "rename", "kill", "stop", "--help", "-h":
		return true
	default:
		return false
	}
}

func remoteCommandNeedsTTY(args []string) bool {
	command := args[0]
	switch command {
	case "terminal", "new":
		return !boolFlagBeforeDash(args[1:], "detach", "d") && !boolFlagBeforeDash(args[1:], "json", "")
	case "attach":
		if boolFlagBeforeDash(args[1:], "json", "") {
			return false
		}
		return boolFlagBeforeDash(args[1:], "select", "s") || firstPositionalBeforeDash(args[1:]) != ""
	case "egg", "sandbox":
		if boolFlagBeforeDash(args[1:], "detach", "d") || boolFlagBeforeDash(args[1:], "json", "") {
			return false
		}
		positional := firstPositionalBeforeDash(args[1:])
		return positional != "" && positional != "list" && positional != "stop" && positional != "explain" && positional != "run"
	default:
		return false
	}
}

// boolFlagBeforeDash mirrors pflag's boolean forms closely enough to choose the
// SSH TTY mode before the remote Cobra command parses the arguments. Repeated
// flags use the last value, and agent arguments after -- are never inspected.
func boolFlagBeforeDash(args []string, long, shorthand string) bool {
	value := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--"+long {
			value = true
			continue
		}
		if raw, ok := strings.CutPrefix(arg, "--"+long+"="); ok {
			if parsed, err := strconv.ParseBool(raw); err == nil {
				value = parsed
			}
			continue
		}
		if shorthand == "" || !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
			continue
		}

		shorts := strings.TrimPrefix(arg, "-")
		for len(shorts) > 0 {
			current := shorts[0]
			shorts = shorts[1:]
			if string(current) != shorthand {
				// This helper only knows the boolean shorthand named by the
				// caller. A different shorthand may consume the remainder.
				break
			}
			if strings.HasPrefix(shorts, "=") {
				if parsed, err := strconv.ParseBool(strings.TrimPrefix(shorts, "=")); err == nil {
					value = parsed
				}
				break
			}
			value = true
		}
	}
	return value
}

func firstPositionalBeforeDash(args []string) string {
	for _, arg := range args {
		if arg == "--" {
			return ""
		}
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

func runRemoteInvocation(ctx context.Context, invocation remoteInvocation, streams remoteIO) error {
	if err := validateRemoteTarget(invocation.target); err != nil {
		return err
	}
	if invocation.state != "" {
		if err := validateRemoteState(invocation.state); err != nil {
			return err
		}
	}
	remoteArgs := []string{invocation.binary}
	if config.Channel() == "preview" || invocation.state != "" {
		// The receiving executable checks this before opening config, tokens,
		// sockets, or sessions. Stable receivers released before this flag
		// reject it as unknown during argument parsing, before they could honor
		// WINGTHING_DIR without the state channel-marker check. Stable argv
		// without --remote-state stays unchanged for existing installations.
		remoteArgs = append(remoteArgs, "--expected-channel", config.Channel())
	}
	remoteArgs = append(remoteArgs, remoteCommandArgs(invocation.args)...)
	quoted := make([]string, len(remoteArgs))
	for i, arg := range remoteArgs {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("remote argument %d contains a NUL byte", i)
		}
		quoted[i] = shellQuote(arg)
	}
	command := strings.Join(quoted, " ")
	if invocation.state != "" {
		// POSIX assignment prefixes apply only to this one remote process. Both
		// names carry the same lexical path: stable reads WINGTHING_DIR, preview
		// requires the pair to agree. Without --remote-state the command stays
		// byte-identical to the legacy argv.
		state := shellQuote(invocation.state)
		command = "WINGTHING_DIR=" + state + " WINGTHING_PREVIEW_DIR=" + state + " " + command
	}
	sshArgs := []string{"-T"}
	if invocation.allocateTTY {
		sshArgs[0] = "-t"
	}
	sshArgs = append(sshArgs, invocation.target, command)
	sshPath := streams.sshPath
	if sshPath == "" {
		sshPath = "ssh"
	}
	child := exec.CommandContext(ctx, sshPath, sshArgs...)
	// SSH helpers (for example a ProxyCommand) can inherit these pipes. Bound
	// the drain after SSH exits or is canceled so a remote deadline stays short.
	child.WaitDelay = 100 * time.Millisecond
	child.Stdin = streams.in
	child.Stdout = streams.out
	child.Stderr = streams.errOut
	if err := child.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := ""
			if exitErr.ExitCode() == 255 {
				reconnect := "reconnect"
				if invocation.state != "" {
					reconnect = "reconnect with the same --remote-state"
				}
				message = fmt.Sprintf("SSH to %s ended with status 255; check the SSH diagnostic above. Session state is unknown; %s and list sessions before relaunching", invocation.target, reconnect)
			}
			return &commandExitError{code: exitErr.ExitCode(), message: message}
		}
		return fmt.Errorf("start ssh for %s: %w", invocation.target, err)
	}
	return nil
}

func remoteCommandArgs(args []string) []string {
	result := append([]string(nil), args...)
	if len(args) < 2 || (args[0] != "egg" && args[0] != "sandbox") {
		return result
	}
	process := args[1]
	switch process {
	case "", "list", "stop", "explain", "run":
		return result
	}
	for i, arg := range result {
		if arg == "--" {
			result = append(result[:i:i], append([]string{"--remote-exact-argv"}, result[i:]...)...)
			return result
		}
	}
	return append(result, "--remote-exact-argv")
}

// shellQuote quotes one argument for the remote user's POSIX shell. OpenSSH
// combines the remote command into a shell string even when local argv is safe.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func remoteEnterCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "_remote", Hidden: true}
	var cwd string
	var jsonOutput bool
	enter := &cobra.Command{
		Use:    "enter",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cwd != "" {
				cwd, err = filepath.Abs(cwd)
				if err != nil {
					return fmt.Errorf("resolve remote working directory: %w", err)
				}
				if info, statErr := os.Stat(cwd); statErr != nil || !info.IsDir() {
					return fmt.Errorf("remote working directory %q does not exist or is not a directory", cwd)
				}
			}
			sessions, err := discoverActiveSessions(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if cwd != "" {
				filtered := sessions[:0]
				for _, session := range sessions {
					if filepath.Clean(session.CWD) == filepath.Clean(cwd) {
						filtered = append(filtered, session)
					}
				}
				sessions = filtered
			}
			if jsonOutput {
				if sessions == nil {
					sessions = []localSession{}
				}
				encoder := json.NewEncoder(os.Stdout)
				encoder.SetIndent("", "  ")
				return encoder.Encode(sessions)
			}
			if len(sessions) == 0 {
				if cwd == "" {
					return errors.New("no active remote sessions; use --remote-cwd PATH to start a sandboxed shell")
				}
				return terminalSpawn(cmd, nil, "", "", cwd, false, false, false, false)
			}
			selected := sessions[0]
			if len(sessions) > 1 {
				selected, err = selectSession(sessions)
				if err != nil {
					return err
				}
			}
			detached, err := attachLocal(cmd.Context(), cfg, selected.ID)
			if detached {
				fmt.Fprintf(os.Stderr, "\r\n[detached from %s]\r\n", selected.ID)
			}
			return err
		},
	}
	enter.Flags().StringVar(&cwd, "cwd", "", "remote workspace directory")
	enter.Flags().BoolVar(&jsonOutput, "json", false, "list matching sessions without creating or attaching")
	cmd.AddCommand(enter)
	return cmd
}
