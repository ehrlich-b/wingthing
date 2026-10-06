package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
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
		Use:   "stdio",
		Short: "Run the local Wingthing MCP server over stdin/stdout",
		Long: "Run a newline-delimited MCP server that lets a local LLM client discover agents, " +
			"control persistent terminals, run prompts, and coordinate bounded loops and swarms.",
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
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			server, err := newLocalMCPServer(cfg, clientName, unsandboxed)
			if err != nil {
				return err
			}
			server.BoundConversation = conversationID
			if err := localmcp.ValidateBoundConversation(server); err != nil {
				return err
			}
			return server.Serve(cmd.Context())
		},
	}
	stdioCmd.Flags().StringVar(&clientName, "client", "", "local MCP principal name (or WT_MCP_CLIENT)")
	stdioCmd.Flags().StringVar(&conversationID, "conversation", "", "bind child launches to an existing owner conversation")
	stdioCmd.Flags().StringVar(&executionID, "execution", "", "exact parent execution session served by --host-mailbox")
	stdioCmd.Flags().StringVar(&hostMailbox, "host-mailbox", "", "forward to the host broker registered for this parent execution")
	stdioCmd.Flags().BoolVar(&unsandboxed, "unsandboxed", false, "trust an outer VM/container boundary for all sessions and prompt runs")
	cmd.AddCommand(stdioCmd)
	cmd.AddCommand(connectMCPCmd())
	return cmd
}

func newLocalMCPServer(cfg *config.Config, clientName string, unsandboxed bool) (*localmcp.Server, error) {
	principal := strings.TrimSpace(clientName)
	if principal == "" {
		principal = strings.TrimSpace(os.Getenv("WT_MCP_CLIENT"))
	}
	explicitClient := principal != ""
	if principal == "" {
		principal = "default"
	}
	if err := eggclient.ValidateSessionName(principal); err != nil {
		return nil, fmt.Errorf("invalid MCP client name: %w", err)
	}
	clientsConfig, err := localmcp.LoadLocalMCPClientsConfig(cfg)
	if err != nil {
		return nil, err
	}
	if clientsConfig.RequireClient && !explicitClient {
		return nil, errors.New("clients.yaml requires an explicit MCP client; pass --client or WT_MCP_CLIENT")
	}
	clientConfig, configured := clientsConfig.Clients[principal]
	if clientsConfig.RequireClient && !configured {
		return nil, fmt.Errorf("MCP client %q is not configured in clients.yaml", principal)
	}
	// Once an operator defines any clients, every principal must have an
	// explicit entry. An omitted --client resolves to the literal
	// "default" entry rather than acquiring the nil-grants full-access
	// behavior intended only for installations without clients.yaml.
	if len(clientsConfig.Clients) > 0 && !configured {
		return nil, fmt.Errorf("MCP client %q is not configured in clients.yaml", principal)
	}
	clientID := principal
	owner := clientID
	if configured && strings.TrimSpace(clientConfig.Owner) != "" {
		owner = strings.TrimSpace(clientConfig.Owner)
		if err := eggclient.ValidateSessionName(owner); err != nil {
			return nil, fmt.Errorf("invalid MCP owner name: %w", err)
		}
	}
	server := &localmcp.Server{Version: version,
		Cfg: cfg, In: os.Stdin, Out: os.Stdout, Logs: os.Stderr,
		Principal: owner, Actor: clientID, MCPClient: clientID, Unsandboxed: unsandboxed,
		Surface: control.SurfaceLocalMCP,
	}
	if configured {
		server.Grants = localmcp.GrantSet(clientConfig.Grants)
		server.MaxSessions = clientConfig.Bounds.MaxSessions
		server.MaxSpawnsPerHour = clientConfig.Bounds.MaxSpawnsPerHour
	}
	return server, nil
}
