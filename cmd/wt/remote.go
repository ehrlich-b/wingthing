package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/spf13/cobra"
)

func executeCLI(ctx context.Context, args []string, streams remotepkg.IO) error {
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
		invocation, remote, err := remotepkg.ParseRemoteInvocation(args, streams.StdinTTY && streams.StdoutTTY, newRootCommand)
		if err != nil {
			return err
		}
		if remote {
			return remotepkg.RunRemoteInvocation(ctx, invocation, streams)
		}
	}
	root := newRootCommand()
	root.SetArgs(args)
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.ErrOut)
	return root.ExecuteContext(context.WithValue(ctx, remoteIOContextKey{}, streams))
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
			sessions, err := eggclient.DiscoverActiveSessions(cmd.Context(), cfg)
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
					sessions = []eggclient.LocalSession{}
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
			detached, err := eggclient.AttachLocal(cmd.Context(), cfg, selected.ID)
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
