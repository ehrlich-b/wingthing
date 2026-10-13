package main

import (
	"encoding/json"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/onboarding"
	"github.com/spf13/cobra"
)

func setupCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "setup", Short: "Detect local agents and show their user-terminal setup commands", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			agents := onboarding.Discover()
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(agents)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Local PATH detection only; provider authentication is separate. Open wt, then a for setup.")
			for _, a := range agents {
				found := a.Path
				if found == "" {
					found = "not installed"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n  Source: %s\n  Installer (explicit user consent required): %s\n", a.Name, found, a.Source, a.Installer)
			}
			return nil
		}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "structured local agent discovery")
	return cmd
}

// Codex keeps the existing generic egg policy and persistent attachment path.
func codexCmd() *cobra.Command {
	cmd := eggCmd()
	cmd.RemoveCommand(cmd.Commands()...)
	run := cmd.RunE
	cmd.Use = "codex [--cwd DIR] [--name NAME] [-- codex args]"
	cmd.Aliases = nil
	cmd.Short = "Start and attach to a persistent Codex session"
	cmd.Example = "  wt codex --cwd /path/to/project --name review"
	cmd.RunE = func(c *cobra.Command, args []string) error { return run(c, append([]string{"codex"}, args...)) }
	return cmd
}
