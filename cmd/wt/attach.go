package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"

	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"

	"github.com/spf13/cobra"
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
				return remotepkg.RunRemoteInvocation(cmd.Context(), remotepkg.Invocation{
					Target: remote.SSHTarget, Binary: remote.Binary(), State: remote.WingthingDir,
					Args: remoteArgs, AllocateTTY: streams.StdinTTY && streams.StdoutTTY,
				}, streams)
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
			detached, err := localmcp.AttachWingIO(cmd.Context(), dir, clientName, sessionID, cmd.InOrStdin(), cmd.OutOrStdout(), options)
			if standaloneAttachAvailable(err, clientName) {
				options.Claim, options.Owner = !readOnlyFlag, "cli"
				detached, err = eggclient.AttachLocalIO(cmd.Context(), &config.Config{Dir: dir}, sessionID, cmd.InOrStdin(), cmd.OutOrStdout(), options)
			}
			if detached {
				fmt.Fprintf(cmd.ErrOrStderr(), "\r\n[detached from %s]\r\n", sessionID)
			}
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

// Standalone terminals predate wing control and survive without a daemon.
// A wing's policy/handshake refusal must never fall back to direct egg access,
// and a named client explicitly selects the wing's authority contract.
func standaloneAttachAvailable(err error, clientName string) bool {
	return clientName == "" && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED))
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
