package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/worktree"
	"github.com/spf13/cobra"
)

func worktreeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "worktree", Short: "Manage isolated Git worktrees"}
	var repo, base string
	var force bool
	cmd.PersistentFlags().StringVar(&repo, "repo", "", "repository directory (default: current directory)")
	manager := func() (worktree.Manager, error) {
		cfg, err := config.Load()
		if err != nil {
			return worktree.Manager{}, err
		}
		wc, err := config.LoadWingConfig(cfg.Dir)
		if err != nil {
			return worktree.Manager{}, err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return worktree.Manager{}, err
		}
		paths := wingpolicy.PathsForRequest(wc.Paths, "", "owner", home)
		if len(paths) == 0 {
			paths = []string{"/"}
		}
		return worktree.Manager{AllowedPaths: paths, Root: os.Getenv("WINGTHING_WORKTREE_ROOT")}, nil
	}
	newCmd := &cobra.Command{
		Use: "new NAME", Short: "Create an unpopulated worktree on a new wt/NAME branch", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			created, err := m.Create(repo, args[0], base)
			if err != nil {
				return err
			}
			if created.CheckoutRequired {
				if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "checkout_required: true; run git reset --hard HEAD in the child sandbox before working"); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), created.Path)
			return err
		},
	}
	newCmd.Flags().StringVar(&base, "base", "", "starting Git ref (default: current HEAD)")
	ls := &cobra.Command{
		Use: "ls", Short: "List managed checkouts", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			entries, err := m.List(repo)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "NAME\tBRANCH\tPATH"); err != nil {
				return err
			}
			for _, entry := range entries {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", entry.Name, entry.Branch, entry.Path); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
	rm := &cobra.Command{
		Use: "rm NAME", Short: "Remove a checkout while retaining its branch", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			return m.Remove(repo, args[0], force)
		},
	}
	rm.Flags().BoolVar(&force, "force", false, "discard uncommitted checkout changes")
	cmd.AddCommand(newCmd, ls, rm)
	return cmd
}
