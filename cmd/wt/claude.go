package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func claudeCmd() *cobra.Command {
	var name, cwd string
	cmd := &cobra.Command{
		Use:   "claude [--name NAME] [--cwd DIR] [-- claude args]",
		Short: "Start and attach to a wing-owned Claude parent with scoped MCP",
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			diagnosticPipe := claudeDiagnosticPipe()
			defer func() { publishClaudeDiagnostic(diagnosticPipe, cmd.ErrOrStderr(), runErr) }()
			dir, err := config.StateDir()
			if err != nil {
				return diagnoseClaudeFailure(err, "connect/startup", "preparation_failed", "")
			}
			// Resolve only in the receiving CLI. SSH transports the cwd flag
			// literally, so relative paths belong to that machine's process.
			launchCWD := cwd
			if launchCWD == "" {
				launchCWD, err = os.Getwd()
			} else {
				launchCWD, err = filepath.Abs(launchCWD)
			}
			if err != nil {
				return diagnoseClaudeFailure(err, "admission/policy", "invalid_workspace", "")
			}
			wire, err := json.Marshal(map[string]any{"agent": "claude", "cwd": launchCWD, "label": name, "args": args, "conversation_role": "parent", "request_id": uuid.NewString(), "scoped_mcp": true})
			if err != nil {
				return err
			}
			stopProgress := claudeStartupProgress(cmd.ErrOrStderr())
			clientName := os.Getenv("WT_MCP_CLIENT")
			client, err := ensureClaudeWing(cmd.Context(), dir, clientName)
			if err != nil {
				stopProgress()
				return diagnoseClaudeFailure(err, "connect/startup", "startup_failed", "")
			}
			// Send the original request ID exactly once. A lost acknowledgement
			// has unknown admission and must never cause a second launch.
			result, isError, err := client.Call(cmd.Context(), "agent_start", wire)
			_ = client.Close()
			stopProgress()
			if err != nil {
				if result != nil && isError {
					return diagnoseClaudeFailure(err, "admission/policy", "policy_refused", "")
				}
				return diagnoseClaudeFailure(err, "admission/policy", "admission_unknown", "")
			}
			if isError {
				failure := control.ToolError(result)
				if failure == nil {
					failure = fmt.Errorf("%v", result["error"])
				}
				return diagnoseClaudeFailure(failure, "admission/policy", "policy_refused", "")
			}
			session, _ := result["session"].(string)
			if !ws.ValidSessionID(session) {
				return diagnoseClaudeFailure(fmt.Errorf("wing did not return a parent session"), "admission/policy", "admission_unknown", "")
			}
			detached, err := localmcp.AttachWingIO(cmd.Context(), dir, clientName, session, cmd.InOrStdin(), cmd.OutOrStdout(), egg.AttachOptions{})
			if detached {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\r\n[detached from %s; reattach with wt attach %s]\r\n", session, session)
			}
			if err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\r\n[attach-after-admission: session %s admitted; reattach with wt attach %s]\r\n", session, session)
				return diagnoseClaudeFailure(err, "attach-after-admission", "attach_failed", session)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "persistent session name")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory (default: current directory)")
	return cmd
}
