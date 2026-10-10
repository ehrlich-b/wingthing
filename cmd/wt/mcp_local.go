package main

import (
	"errors"
	"os"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/spf13/cobra"
)

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Expose Wingthing to LLM clients",
	}
	var clientName string
	var conversationID string
	var executionID string
	var hostMailbox string
	var unsandboxed bool
	stdioCmd := &cobra.Command{
		Use:          "stdio",
		SilenceUsage: true,
		Short:        "Run the local Wingthing MCP server over stdin/stdout",
		Long: "Run a newline-delimited MCP server that lets a local LLM client discover agents, " +
			"control persistent terminals and run agents through an independently running local wing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if hostMailbox != "" || executionID != "" {
				// A sandboxed parent's injected client. It forwards to the host
				// broker registered for this execution and never opens Wingthing
				// state; --client is informational, authority is host-captured.
				if hostMailbox == "" || executionID == "" || conversationID == "" || unsandboxed {
					return errors.New("--host-mailbox requires --conversation and --execution and cannot be combined with --unsandboxed")
				}
				return localmcp.ServeConversationMailboxClient(cmd.Context(), os.Stdin, os.Stdout, hostMailbox, conversationID, executionID)
			}
			dir, err := config.StateDir()
			if err != nil {
				return err
			}
			name := strings.TrimSpace(clientName)
			if name == "" {
				name = strings.TrimSpace(os.Getenv("WT_MCP_CLIENT"))
			}
			return localmcp.ServeLocalWingClient(cmd.Context(), version, dir, name, conversationID, unsandboxed, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	stdioCmd.Flags().StringVar(&clientName, "client", "", "local MCP principal name (or WT_MCP_CLIENT)")
	stdioCmd.Flags().StringVar(&conversationID, "conversation", "", "bind child launches to an existing owner conversation")
	stdioCmd.Flags().StringVar(&executionID, "execution", "", "exact parent execution session served by --host-mailbox")
	stdioCmd.Flags().StringVar(&hostMailbox, "host-mailbox", "", "forward to the host broker registered for this parent execution")
	stdioCmd.Flags().BoolVar(&unsandboxed, "unsandboxed", false, "request the outer VM/container boundary (requires allow_unsandboxed in wing.yaml)")
	cmd.AddCommand(stdioCmd)
	cmd.AddCommand(connectMCPCmd())
	return cmd
}
