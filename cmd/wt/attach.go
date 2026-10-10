package main

import (
	"errors"
	"fmt"

	"os"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"

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
		Long: "Attach the current terminal directly to a persistent egg session. " +
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

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if remoteName != "" {
				remote, err := remotepkg.ConfiguredRemote(cfg.Dir, remoteName)
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
			if sessionID == "" {
				if !selectFlag {
					return printActiveSessions(cmd.Context(), cfg, jsonFlag)
				}
				selected, selectErr := selectActiveSession(cmd.Context(), cfg)
				if selectErr != nil {
					return selectErr
				}
				sessionID = selected.ID
			}

			detached, err := eggclient.AttachLocalOptions(cmd.Context(), cfg, sessionID, egg.AttachOptions{ReadOnly: readOnlyFlag, Takeover: takeoverFlag, Claim: !readOnlyFlag, Owner: "cli"})
			if detached {
				fmt.Fprintf(os.Stderr, "\r\n[detached from %s]\r\n", sessionID)
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
