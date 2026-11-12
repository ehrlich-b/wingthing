package main

import (
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/spf13/cobra"
)

func contextCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "context", Short: "Group this machine and saved remotes into Home or Work"}
	cmd.AddCommand(&cobra.Command{
		Use: "set <machine> <home|work|none>", Short: "Set a machine's context; local means this machine", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.StateDir()
			if err != nil {
				return err
			}
			if err := config.SetMachineContext(dir, args[0], args[1]); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", args[0], args[1])
			return err
		},
	}, &cobra.Command{
		Use: "list", Short: "List each machine and its context", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.StateDir()
			if err != nil {
				return err
			}
			contexts, err := config.MachineContexts(dir)
			if err != nil {
				return err
			}
			var names []string
			for name := range contexts {
				if name != "local" {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			names = append([]string{"local"}, names...)
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "MACHINE\tCONTEXT"); err != nil {
				return err
			}
			for _, name := range names {
				value := contexts[name]
				if value == "" {
					value = "none"
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\n", name, value); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	})
	return cmd
}
