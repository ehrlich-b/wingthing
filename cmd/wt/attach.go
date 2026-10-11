package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"

	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func attachCmd() *cobra.Command {
	var selectFlag bool
	var jsonFlag bool
	var readOnlyFlag, takeoverFlag bool

	cmd := &cobra.Command{
		Use:   "attach [session]",
		Short: "Attach this terminal to a running egg session",
		Long: "Attach the current terminal to a persistent egg session, through the wing when one is running. " +
			"Use NAME:SESSION for a configured SSH remote, or a plain SESSION for a local session. " +
			"Use --remote with an SSH host from ~/.ssh/config to attach without opening the web app.\n\n" +
			"Detach without stopping the session with Ctrl+B, then Q. Send a literal Ctrl+B with Ctrl+B twice.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sessionID string
			if len(args) == 1 {
				sessionID = args[0]
			}

			if selectFlag && jsonFlag {
				return errors.New("--select and --json are mutually exclusive")
			}
			if sessionID != "" && jsonFlag {
				return errors.New("--json lists sessions and cannot be used with a session")
			}
			remoteName, remoteSession, err := parseRemoteSession(sessionID)
			if err != nil {
				return err
			}

			dir, err := config.StateDir()
			if err != nil {
				return err
			}
			if remoteName != "" {
				remote, err := remotepkg.ConfiguredRemote(dir, remoteName)
				if err != nil {
					return err
				}
				remoteArgs := []string{"attach"}
				if readOnlyFlag {
					remoteArgs = append(remoteArgs, "--read-only")
				}
				if takeoverFlag {
					remoteArgs = append(remoteArgs, "--takeover")
				}
				remoteArgs = append(remoteArgs, "--", remoteSession)
				streams := remotepkg.Streams(cmd.Context())
				invocation := remotepkg.ProfileInvocation(remote, remoteArgs)
				invocation.AllocateTTY = streams.StdinTTY && streams.StdoutTTY
				err = runRemoteAttachment(cmd.Context(), invocation, streams)
				var exitErr *cmdutil.CommandExitError
				if err != nil && cmd.Context().Err() == nil && (!errors.As(err, &exitErr) || exitErr.Code == 255) {
					reconnectArgs := append(remotepkg.ProfileArgs(remote), remoteArgs...)
					profileArgs := []string{"attach"}
					if readOnlyFlag {
						profileArgs = append(profileArgs, "--read-only")
					}
					profileArgs = append(profileArgs, "--", remoteName+":"+remoteSession)
					fmt.Fprintf(cmd.ErrOrStderr(), "Connection to %s (%s) ended; session outcome unknown. Reconnect: %s\nPinned route: %s\n", remoteName, remote.SSHTarget, attachReconnectCommand(dir, profileArgs), attachReconnectCommand(dir, reconnectArgs))
				}
				return err
			}
			clientName := os.Getenv("WT_MCP_CLIENT")
			if sessionID == "" {
				sessions, err := attachSessions(cmd.Context(), dir, clientName)
				if err != nil {
					return err
				}
				if !selectFlag {
					return writeLocalSessions(cmd.OutOrStdout(), sessions, jsonFlag)
				}
				selected, selectErr := selectSession(sessions)
				if selectErr != nil {
					return selectErr
				}
				sessionID = selected.ID
			}

			options := egg.AttachOptions{ReadOnly: readOnlyFlag, Takeover: takeoverFlag}
			ctx := eggclient.WithAttachDiagnostics(cmd.Context(), cmd.ErrOrStderr())
			detached, err := localmcp.AttachWingIO(ctx, dir, clientName, sessionID, cmd.InOrStdin(), cmd.OutOrStdout(), options)
			if standaloneAttachAvailable(err, clientName) {
				options.Claim, options.Owner = !readOnlyFlag, "cli"
				detached, err = eggclient.AttachLocalIO(ctx, &config.Config{Dir: dir}, sessionID, cmd.InOrStdin(), cmd.OutOrStdout(), options)
			}
			if detached {
				fmt.Fprintf(cmd.ErrOrStderr(), "\r\n[detached from %s]\r\n", sessionID)
			}
			var lost *eggclient.AttachConnectionError
			if errors.As(err, &lost) && cmd.Context().Err() == nil {
				if lost.SessionID != "" {
					sessionID = lost.SessionID
				}
				args := []string{"attach"}
				if readOnlyFlag {
					args = append(args, "--read-only")
				}
				// Retrying never repeats a takeover or any uncertain input.
				args = append(args, "--", sessionID)
				fmt.Fprintf(cmd.ErrOrStderr(), "Reconnect: %s\n", attachReconnectCommand(dir, args))
				return &cmdutil.CommandExitError{Code: 255, Message: err.Error()}
			}
			// Preserve the CLI's ordinary failure status for confirmed provider
			// exits, including provider code 255. Status 255 here is reserved
			// for an unknown attachment outcome carried back over SSH.
			return err
		},
	}

	cmd.Flags().BoolVarP(&selectFlag, "select", "s", false, "choose a session interactively")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print active sessions as JSON")
	cmd.Flags().BoolVar(&readOnlyFlag, "read-only", false, "observe output without writing or resizing")
	cmd.Flags().BoolVar(&takeoverFlag, "takeover", false, "explicitly take input control from another attachment (preview)")
	cmd.MarkFlagsMutuallyExclusive("read-only", "takeover")
	return cmd
}

// SSH may be killed before it restores its own raw terminal. This outer
// snapshot belongs to the local attachment and also covers failed startup.
func runRemoteAttachment(ctx context.Context, invocation remotepkg.Invocation, streams remotepkg.IO) (err error) {
	input, inOK := streams.In.(*os.File)
	output, outOK := streams.Out.(*os.File)
	if invocation.AllocateTTY && inOK && outOK && term.IsTerminal(int(input.Fd())) && term.IsTerminal(int(output.Fd())) {
		previous, stateErr := term.GetState(int(input.Fd()))
		if stateErr != nil {
			return stateErr
		}
		defer func() {
			_, resetErr := output.Write(egg.NativeTerminalReset())
			restoreErr := term.Restore(int(input.Fd()), previous)
			err = errors.Join(err, resetErr, restoreErr)
		}()
		if streams.ErrOut != nil {
			_, _ = io.WriteString(streams.ErrOut, "[remote attachment; Ctrl+B then q detaches; Ctrl+B twice sends Ctrl+B]\n")
		}
	}
	return remotepkg.RunRemoteInvocation(ctx, invocation, streams)
}

// Standalone terminals predate wing control and survive without a daemon.
// A wing's policy/handshake refusal must never fall back to direct egg access,
// and a named client explicitly selects the wing's authority contract.
func standaloneAttachAvailable(err error, clientName string) bool {
	var lost *eggclient.AttachConnectionError
	if errors.As(err, &lost) {
		return false
	}
	return clientName == "" && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED))
}

func attachReconnectCommand(dir string, args []string) string {
	executable, err := os.Executable()
	if err != nil {
		executable = config.BinaryName()
	}
	quoted := []string{remotepkg.ShellQuote(executable)}
	flags := true
	for _, arg := range args {
		// Taking control always needs a new, explicit decision.
		if !flags || arg != "--takeover" {
			quoted = append(quoted, remotepkg.ShellQuote(arg))
		}
		if arg == "--" {
			flags = false
		}
	}
	prefix := "WINGTHING_DIR=" + remotepkg.ShellQuote(dir) + " WINGTHING_PREVIEW_DIR=" + remotepkg.ShellQuote(dir) + " "
	if client := os.Getenv("WT_MCP_CLIENT"); client != "" {
		prefix += "WT_MCP_CLIENT=" + remotepkg.ShellQuote(client) + " "
	}
	return prefix + strings.Join(quoted, " ")
}

func attachSessions(ctx context.Context, dir, clientName string) ([]eggclient.LocalSession, error) {
	result, err := localmcp.CallLocalWingTool(ctx, dir, clientName, "terminal_list", json.RawMessage(`{}`))
	if standaloneAttachAvailable(err, clientName) {
		return eggclient.DiscoverActiveSessions(ctx, &config.Config{Dir: dir})
	}
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(result["sessions"])
	if err != nil {
		return nil, err
	}
	var sessions []eggclient.LocalSession
	err = json.Unmarshal(data, &sessions)
	return sessions, err
}
