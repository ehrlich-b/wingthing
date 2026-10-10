package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/spf13/cobra"
)

func inspectMCPCmd() *cobra.Command {
	var dir, client string
	cmd := &cobra.Command{Use: "inspect", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if dir == "" {
			var err error
			dir, err = config.StateDir()
			if err != nil {
				return err
			}
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		metadata, err := sshcontrol.InspectLocal(ctx, dir, client)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(metadata)
	}}
	cmd.Flags().StringVar(&dir, "wingthing-dir", "", "existing remote wing state directory")
	cmd.Flags().StringVar(&client, "client", "", "MCP client for the read-only handshake")
	return cmd
}
func addConnectMCPCmd(client *string, timeout *time.Duration) *cobra.Command {
	var host, dir string
	cmd := &cobra.Command{Use: "add NAME --ssh HOST", Short: "Remember and verify an already running SSH wing", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.ValidateRemoteName(args[0]); err != nil {
			return err
		}
		if err := config.ValidateSSHTarget(host); err != nil {
			return err
		}
		state, err := config.StateDir()
		if err != nil {
			return err
		}
		remotes, err := config.LoadRemotes(state)
		if err != nil {
			return err
		}
		if _, ok := remotes[args[0]]; ok {
			return fmt.Errorf("remote %q already exists; remove it before adding it again", args[0])
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), *timeout)
		defer cancel()
		transport := sshcontrol.Transport{SSHPath: remotepkg.Streams(ctx).SSHPath, Timeout: *timeout}
		r, err := transport.Inspect(ctx, host, dir, *client)
		if err != nil {
			return err
		}
		return config.UpdateRemotes(state, func(remotes map[string]config.Remote) error {
			if _, ok := remotes[args[0]]; ok {
				return fmt.Errorf("remote %q already exists", args[0])
			}
			remotes[args[0]] = r
			return nil
		})
	}}
	cmd.Flags().StringVar(&host, "ssh", "", "existing SSH host alias or user@host")
	cmd.Flags().StringVar(&dir, "wingthing-dir", "", "remote state path; a leading ~/ is resolved once on the host")
	return cmd
}
