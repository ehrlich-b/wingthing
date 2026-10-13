package main

import (
	"encoding/json"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/spf13/cobra"
)

func searchCmd() *cobra.Command {
	var local, jsonFlag bool
	cmd := &cobra.Command{Use: "search <query>", Short: "Search live and past sessions across local and configured personal wings", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]any{"query": strings.Join(args, " "), "include_remotes": !local})
		result, err := localmcp.CallLocalWingTool(cmd.Context(), cfg.Dir, "", "session_search", raw)
		if err != nil {
			return err
		}
		if jsonFlag {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		data, _ := json.Marshal(result)
		var inv fleet.Inventory
		if err := json.Unmarshal(data, &inv); err != nil {
			return err
		}
		return renderSessionInventory(cmd.OutOrStdout(), inv)
	}}
	cmd.Flags().BoolVar(&local, "local", false, "search this wing only")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print structured results, routes and coverage")
	return cmd
}
