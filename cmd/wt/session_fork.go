package main

import (
	"encoding/json"
	"fmt"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/spf13/cobra"
)

func sessionForkCmd() *cobra.Command {
	var name, clientName string
	var jsonFlag bool
	cmd := &cobra.Command{Use: "fork SESSION", Short: "Fork an owned Claude conversation into a new named session", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		arguments, err := json.Marshal(map[string]string{"session": args[0], "name": name})
		if err != nil {
			return err
		}
		server, err := newLocalMCPServer(cfg, clientName, false)
		if err != nil {
			return err
		}
		server.Actor = "cli:session-fork"
		result, err := server.ToolSessionFork(cmd.Context(), arguments)
		if err != nil {
			return err
		}
		if jsonFlag {
			return writeSessionJSON(result)
		}
		fmt.Printf("%s (%s), forked from %s\n", result["label"], result["session"], result["source_session"])
		return nil
	}}
	cmd.Flags().StringVar(&clientName, "client", "", "local MCP client for linked forks (or WT_MCP_CLIENT)")
	cmd.Flags().StringVar(&name, "name", "", "new session name (generated if omitted)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}
