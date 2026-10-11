package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func claudeCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "claude [--name NAME] [-- claude args]",
		Short: "Start and attach to a wing-owned Claude parent with scoped MCP",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.StateDir()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			wire, err := json.Marshal(map[string]any{"agent": "claude", "cwd": cwd, "label": name, "args": args, "conversation_role": "parent", "request_id": uuid.NewString(), "scoped_mcp": true})
			if err != nil {
				return err
			}
			stopProgress := claudeStartupProgress(cmd.ErrOrStderr())
			clientName := os.Getenv("WT_MCP_CLIENT")
			client, err := ensureClaudeWing(cmd.Context(), dir, clientName)
			if err != nil {
				stopProgress()
				return err
			}
			// Send the original request ID exactly once. A lost acknowledgement
			// has unknown admission and must never cause a second launch.
			result, isError, err := client.Call(cmd.Context(), "agent_start", wire)
			_ = client.Close()
			stopProgress()
			if err != nil {
				return err
			}
			if isError {
				return fmt.Errorf("%v", result["error"])
			}
			session, _ := result["session"].(string)
			if session == "" {
				return fmt.Errorf("wing did not return a parent session: %v", result)
			}
			detached, err := localmcp.AttachWingIO(cmd.Context(), dir, clientName, session, cmd.InOrStdin(), cmd.OutOrStdout(), egg.AttachOptions{})
			if detached {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\r\n[detached from %s; reattach with wt attach %s]\r\n", session, session)
			}
			if err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\r\n[session %s admitted; reattach with wt attach %s]\r\n", session, session)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "persistent session name")
	return cmd
}
