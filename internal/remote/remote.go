package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Invocation.State is a lexical absolute path on the remote host. It is
// never resolved locally: the receiving executable owns its own state checks.
type Invocation struct {
	Target      string
	Binary      string
	CWD         string
	State       string
	WingID      string
	Args        []string
	AllocateTTY bool
	// NonInteractive prevents SSH from prompting via /dev/tty in background work.
	NonInteractive bool
}

// ProfileInvocation preserves the saved host, executable and remote state. The
// receiving binary still applies the channel guard and attachment authorization.
func ProfileInvocation(profile config.Remote, args []string) Invocation {
	return Invocation{Target: profile.SSHTarget, Binary: profile.Binary(), State: profile.WingthingDir, WingID: profile.WingID, Args: append([]string(nil), args...)}
}

// ProfileArgs is literal local CLI argv; RunRemoteInvocation alone quotes it for
// the receiving POSIX shell. Labels and remote paths never become shell source.
func ProfileArgs(profile config.Remote) []string {
	route := ProfileInvocation(profile, nil)
	args := []string{"--remote", route.Target, "--remote-binary", route.Binary}
	if route.State != "" {
		args = append(args, "--remote-state", route.State)
	}
	if route.WingID != "" {
		args = append(args, "--remote-wing-id", route.WingID)
	}
	return args
}

type IO struct {
	In        io.Reader
	Out       io.Writer
	ErrOut    io.Writer
	StdinTTY  bool
	StdoutTTY bool
	SSHPath   string
}

func ProcessIO() IO {
	return IO{
		In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr,
		StdinTTY:  term.IsTerminal(int(os.Stdin.Fd())),
		StdoutTTY: term.IsTerminal(int(os.Stdout.Fd())),
		SSHPath:   "ssh",
	}
}

func ParseRemoteInvocation(argv []string, interactive bool, newRootCommand func() *cobra.Command) (Invocation, bool, error) {
	invocation := Invocation{Binary: config.BinaryName()}
	root := newRootCommand()
	// Cobra Find must know this transport flag is boolean; otherwise it can
	// consume the following command and lose that command's flag-value rules.
	if root.PersistentFlags().Lookup("remote-batch") == nil {
		root.PersistentFlags().Bool("remote-batch", false, "internal background SSH mode")
	}
	command, _, _ := root.Find(argv)
	args := make([]string, 0, len(argv))
	transportSeen := false
	stateSeen := false
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			args = append(args, argv[i:]...)
			break
		}

		// Internal transport option used by background dashboard children. It is
		// stripped before dispatch, including when Cobra never parses the root flags.
		if arg == "--remote-batch" || strings.HasPrefix(arg, "--remote-batch=") {
			transportSeen = true
			invocation.NonInteractive = true
			if raw, ok := strings.CutPrefix(arg, "--remote-batch="); ok {
				parsed, err := strconv.ParseBool(raw)
				if err != nil {
					return Invocation{}, false, fmt.Errorf("--remote-batch: %w", err)
				}
				invocation.NonInteractive = parsed
			}
			continue
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
				return Invocation{}, false, fmt.Errorf("%s requires a value", name)
			}
			value = argv[i]
		}
		switch name {
		case "--remote":
			if invocation.Target != "" {
				return Invocation{}, false, errors.New("--remote may be specified only once")
			}
			invocation.Target = value
		case "--remote-binary":
			invocation.Binary = value
		case "--remote-cwd":
			invocation.CWD = value
		case "--remote-wing-id":
			if invocation.WingID != "" || value == "" || strings.ContainsAny(value, "\x00\r\n:") {
				return Invocation{}, false, errors.New("invalid or repeated --remote-wing-id")
			}
			invocation.WingID = value
		case "--remote-state":
			if stateSeen {
				return Invocation{}, false, errors.New("--remote-state may be specified only once")
			}
			stateSeen = true
			invocation.State = value
		}
	}

	if !transportSeen {
		return Invocation{}, false, nil
	}
	if invocation.Target == "" {
		return Invocation{}, false, errors.New("--remote requires a non-empty SSH host or alias")
	}
	if err := ValidateRemoteTarget(invocation.Target); err != nil {
		return Invocation{}, false, err
	}
	if err := validateRemoteBinary(invocation.Binary); err != nil {
		return Invocation{}, false, err
	}
	if strings.IndexByte(invocation.CWD, 0) >= 0 {
		return Invocation{}, false, errors.New("remote working directory contains a NUL byte")
	}
	if stateSeen {
		if err := ValidateRemoteState(invocation.State); err != nil {
			return Invocation{}, false, err
		}
	}
	interactive = interactive && !invocation.NonInteractive
	bareEntry := len(args) == 0 || remoteJSONOnly(args)
	if invocation.CWD != "" && !bareEntry {
		return Invocation{}, false, errors.New("--remote-cwd is only valid with bare 'wt --remote HOST'; use the command's --cwd flag for launches")
	}

	if bareEntry {
		invocation.Args = []string{"_remote", "enter"}
		invocation.Args = append(invocation.Args, args...)
		if invocation.CWD != "" {
			invocation.Args = append(invocation.Args, "--cwd", invocation.CWD)
		}
		if !interactive {
			invocation.Args = append(invocation.Args, "--json")
		}
		invocation.AllocateTTY = interactive && !BoolFlagBeforeDash(invocation.Args[2:], "json", "")
		return invocation, true, nil
	}
	if err := ValidateRemoteCommand(args); err != nil {
		return Invocation{}, false, err
	}
	invocation.Args = args
	invocation.AllocateTTY = interactive && remoteCommandNeedsTTY(args)
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
	case "--remote-binary", "--remote-cwd", "--remote-state", "--remote-wing-id":
		return arg, "", false, true
	}
	for _, name := range []string{"--remote", "--remote-binary", "--remote-cwd", "--remote-state", "--remote-wing-id"} {
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

func ValidateRemoteTarget(target string) error {
	return config.ValidateSSHTarget(target)
}

func validateRemoteBinary(binary string) error {
	if binary == "" || strings.ContainsAny(binary, "\r\n\x00") {
		return errors.New("invalid remote binary")
	}
	return nil
}

// ValidateRemoteState accepts only a POSIX absolute path for the remote host.
// It deliberately avoids filepath.Abs, Clean, and EvalSymlinks: the client's
// filesystem says nothing about the remote one, and the receiving executable
// applies its own overlap, alias, and channel-marker checks before any write.
//
// As with --remote-binary, NUL cannot reach an
// environment value, and CR or LF would split the one-line remote command for
// SSH logs and non-POSIX login shells. Every other byte, including tab and
// other control bytes, is legal in a POSIX path and stays inert inside the
// single-quoted assignment, so it is passed through literally.
func ValidateRemoteState(state string) error {
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

func ValidateRemoteCommand(args []string) error {
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
	case "pin", "unpin", "claude", "terminal", "new", "attach", "channel", "help", "--help", "-h", "--version":
		return nil
	case "session":
		if len(args) == 1 || remoteSessionCommand(args[1]) {
			return nil
		}
		return fmt.Errorf("session subcommand %q is not available over SSH; supported subcommands: list, ps, active, read, send, wait, rename, kill, and stop", args[1])
	}
	return fmt.Errorf("command %q is not available over SSH; supported commands: claude, egg, terminal, attach, pin, unpin, session, and channel", command)
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
	case "claude":
		// Claude always attaches its scoped parent. Provider flags after --
		// cannot turn this transport into a detached or JSON launch.
		return true
	case "terminal", "new":
		return !BoolFlagBeforeDash(args[1:], "detach", "d") && !BoolFlagBeforeDash(args[1:], "json", "")
	case "attach":
		if BoolFlagBeforeDash(args[1:], "json", "") {
			return false
		}
		if BoolFlagBeforeDash(args[1:], "select", "s") || firstPositionalBeforeDash(args[1:]) != "" {
			return true
		}
		// Attach owns its positional target after --; unlike provider argv, even
		// a target beginning with '-' is a literal session reference here.
		for i, arg := range args[1:] {
			if arg == "--" {
				return i+2 < len(args) && args[i+2] != ""
			}
		}
		return false
	case "egg", "sandbox":
		if BoolFlagBeforeDash(args[1:], "detach", "d") || BoolFlagBeforeDash(args[1:], "json", "") {
			return false
		}
		positional := firstPositionalBeforeDash(args[1:])
		return positional != "" && positional != "list" && positional != "stop" && positional != "explain" && positional != "run"
	default:
		return false
	}
}

// BoolFlagBeforeDash mirrors pflag's boolean forms closely enough to choose the
// SSH TTY mode before the remote Cobra command parses the arguments. Repeated
// flags use the last value, and agent arguments after -- are never inspected.
func BoolFlagBeforeDash(args []string, long, shorthand string) bool {
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

func RunRemoteInvocation(ctx context.Context, invocation Invocation, streams IO) error {
	if err := ValidateRemoteTarget(invocation.Target); err != nil {
		return err
	}
	if invocation.State != "" {
		if err := ValidateRemoteState(invocation.State); err != nil {
			return err
		}
	}
	remoteArgs := []string{invocation.Binary}
	if config.Channel() == "preview" || invocation.State != "" {
		// The receiving executable checks this before opening config, tokens,
		// sockets, or sessions. Stable receivers released before this flag
		// reject it as unknown during argument parsing, before they could honor
		// WINGTHING_DIR without the state channel-marker check. Stable argv
		// without --remote-state stays unchanged for existing installations.
		remoteArgs = append(remoteArgs, "--expected-channel", config.Channel())
	}
	if invocation.WingID != "" {
		if strings.ContainsAny(invocation.WingID, "\x00\r\n:") {
			return errors.New("invalid expected remote wing ID")
		}
		remoteArgs = append(remoteArgs, "--expected-wing-id", invocation.WingID)
	}
	remoteArgs = append(remoteArgs, remoteCommandArgs(invocation.Args)...)
	quoted := make([]string, len(remoteArgs))
	for i, arg := range remoteArgs {
		if strings.IndexByte(arg, 0) >= 0 {
			return fmt.Errorf("remote argument %d contains a NUL byte", i)
		}
		quoted[i] = ShellQuote(arg)
	}
	command := strings.Join(quoted, " ")
	if invocation.State != "" {
		// POSIX assignment prefixes apply only to this one remote process. Both
		// names carry the same lexical path: stable reads WINGTHING_DIR, preview
		// requires the pair to agree. Without --remote-state the command stays
		// byte-identical to the legacy argv.
		state := ShellQuote(invocation.State)
		command = "WINGTHING_DIR=" + state + " WINGTHING_PREVIEW_DIR=" + state + " " + command
	}
	sshArgs := []string{"-T"}
	if invocation.AllocateTTY && !invocation.NonInteractive {
		sshArgs[0] = "-t"
	}
	if invocation.NonInteractive {
		sshArgs = append(sshArgs, "-o", "BatchMode=yes")
	}
	sshArgs = append(sshArgs, invocation.Target, command)
	sshPath := streams.SSHPath
	if sshPath == "" {
		sshPath = "ssh"
	}
	child := exec.CommandContext(ctx, sshPath, sshArgs...)
	// SSH helpers (for example a ProxyCommand) can inherit these pipes. Bound
	// the drain after SSH exits or is canceled so a remote deadline stays short.
	child.WaitDelay = 100 * time.Millisecond
	child.Stdin = streams.In
	child.Stdout = streams.Out
	child.Stderr = streams.ErrOut
	if err := child.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := ""
			if exitErr.ExitCode() == 255 {
				reconnect := "reconnect"
				if invocation.State != "" {
					reconnect = "reconnect with the same --remote-state"
				}
				message = fmt.Sprintf("SSH to %s ended with status 255; check the SSH diagnostic above. Session state is unknown; %s and list sessions before relaunching", invocation.Target, reconnect)
			}
			return &cmdutil.CommandExitError{Code: exitErr.ExitCode(), Message: message}
		}
		return fmt.Errorf("start ssh for %s: %w", invocation.Target, err)
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

// ShellQuote quotes one argument for the remote user's POSIX shell. OpenSSH
// combines the remote command into a shell string even when local argv is safe.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
