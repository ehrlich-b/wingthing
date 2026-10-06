package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/spf13/cobra"
)

// Bootstrap is deliberately output-only: it does not edit clients.yaml, add
// grants, copy provider credentials, or select a different Wingthing state dir.
func conversationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "conversation", Short: "Inspect persistent parent and child conversations"}
	var client string
	cmd.PersistentFlags().StringVar(&client, "client", "default", "existing local MCP owner/client")
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		server := &localmcp.Server{Version: version, Cfg: cfg, Principal: client, Logs: os.Stderr}
		result, err := server.ToolConversationList(json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		return writeSessionJSON(result)
	}}
	var after int64
	read := &cobra.Command{Use: "read <conversation>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		server := &localmcp.Server{Version: version, Cfg: cfg, Principal: client, Logs: os.Stderr}
		input, _ := json.Marshal(map[string]any{"conversation_id": args[0], "after_cursor": after})
		result, err := server.ToolConversationRead(cmd.Context(), input)
		if err != nil {
			return err
		}
		return writeSessionJSON(result)
	}}
	read.Flags().Int64Var(&after, "after-cursor", 0, "return durable state deliveries after this cursor")
	bootstrap := &cobra.Command{Use: "bootstrap <conversation>", Short: "Print a reproducible Claude MCP configuration without changing permissions", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		db, err := store.Open(cfg.DBPath())
		if err != nil {
			return err
		}
		defer cmdutil.CloseWithLog("conversation bootstrap store", db)
		c, err := db.GetConversation(client, args[0])
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("conversation not found or not owned by caller")
		}
		if err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		return writeSessionJSON(map[string]any{"mcpServers": map[string]any{"wingthing": map[string]any{"command": executable, "args": []string{"mcp", "stdio", "--client", client, "--conversation", c.ID}, "env": map[string]string{"WINGTHING_DIR": filepath.Clean(cfg.Dir)}}}})
	}}
	list.Flags().Bool("json", true, "print structured inventory")
	read.Flags().Bool("json", true, "print structured conversation tree")
	bootstrap.Flags().Bool("json", true, "print Claude MCP configuration")
	cmd.AddCommand(list, read, bootstrap, conversationWakeCmd(&client))
	cmd.AddCommand(conversationBrokerCmds()...)
	return cmd
}
