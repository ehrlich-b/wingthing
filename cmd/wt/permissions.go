package main

import (
	"encoding/json"
	"fmt"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/spf13/cobra"
)

func permissionCommand() *cobra.Command {
	root := &cobra.Command{Use: "permission", Short: "Review a live Claude Bash request on the local personal wing"}
	for _, mode := range []string{"list", "respond"} {
		var wing, session, request, digest, attempt, decision string
		var asJSON bool
		cmd := &cobra.Command{Use: mode, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			if !asJSON {
				return fmt.Errorf("use --json for the structured permission contract")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			fields := map[string]string{"wing_id": wing, "session_id": session}
			if mode == "respond" {
				fields["request_id"] = request
				fields["digest"] = digest
				fields["attempt_id"] = attempt
				fields["decision"] = decision
			}
			wire, _ := json.Marshal(fields)
			result, err := localmcp.CallHumanPermission(cmd.Context(), cfg.Dir, "tui", "tui", "permission_"+mode, wire)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}}
		cmd.Flags().StringVar(&wing, "wing-id", "", "Exact wing ID")
		cmd.Flags().StringVar(&session, "session-id", "", "Exact execution session ID")
		cmd.Flags().BoolVar(&asJSON, "json", false, "Structured output")
		if mode == "respond" {
			cmd.Flags().StringVar(&request, "request-id", "", "Exact request ID")
			cmd.Flags().StringVar(&digest, "digest", "", "Displayed digest")
			cmd.Flags().StringVar(&attempt, "attempt-id", "", "Decision attempt ID")
			cmd.Flags().StringVar(&decision, "decision", "", "allow or deny")
		}
		root.AddCommand(cmd)
	}
	return root
}
