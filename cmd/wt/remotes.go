package main

import (
	"fmt"

	"text/tabwriter"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/spf13/cobra"
)

func remoteCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "remote", Short: "Manage configured SSH remotes"}
	var wingthingDir string
	add := &cobra.Command{
		Use: "add NAME SSH_TARGET", Short: "Add an SSH remote", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.ValidateRemoteName(args[0]); err != nil {
				return err
			}
			if err := remotepkg.ValidateRemoteTarget(args[1]); err != nil {
				return err
			}
			if cmd.Flags().Changed("wingthing-dir") {
				if err := remotepkg.ValidateRemoteState(wingthingDir); err != nil {
					return err
				}
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return config.UpdateRemotes(cfg.Dir, func(remotes map[string]config.Remote) error {
				if _, exists := remotes[args[0]]; exists {
					return fmt.Errorf("remote %q already exists; remove it before adding it again", args[0])
				}
				remotes[args[0]] = config.Remote{SSHTarget: args[1], WingthingDir: wingthingDir}
				return nil
			})
		},
	}
	add.Flags().StringVar(&wingthingDir, "wingthing-dir", "", "absolute Wingthing state directory on the remote host")
	ls := &cobra.Command{
		Use: "ls", Short: "List configured SSH remotes", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			remotes, err := config.LoadRemotes(cfg.Dir)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "NAME\tSSH_TARGET\tWINGTHING_DIR\tWT_BINARY"); err != nil {
				return err
			}
			for _, name := range eggclient.SortedRemoteNames(remotes) {
				remote := remotes[name]
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, remote.SSHTarget, remote.WingthingDir, remote.Binary()); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
	rm := &cobra.Command{
		Use: "rm NAME", Short: "Remove an SSH remote", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.ValidateRemoteName(args[0]); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return config.UpdateRemotes(cfg.Dir, func(remotes map[string]config.Remote) error {
				if _, exists := remotes[args[0]]; !exists {
					return fmt.Errorf("unknown remote %q", args[0])
				}
				delete(remotes, args[0])
				return nil
			})
		},
	}
	cmd.AddCommand(add, ls, rm)
	return cmd
}
