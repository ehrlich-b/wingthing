package main

import (
	"errors"
	"github.com/ehrlich-b/wingthing/internal/launchplan"
	"github.com/spf13/cobra"
)

func doctorLaunchPlanCmd() *cobra.Command {
	var req launchplan.Request
	var jsonOutput bool
	cmd := &cobra.Command{
		Use: "launch-plan", Short: "Explain an explicit launch without invoking a provider or probing native capabilities",
		Long: "Read-only launch diagnosis from explicit policy, paths and saved evidence. Does not load a wing, login, fix policy, bind sockets, or execute a provider. Missing native evidence is REQUIRED-UNRUN.\n\nterminal_start is a command session; agent_start uses the interactive adapter; agent_run requires native run support. No prompt, credential values, passthrough argv, or --fix is accepted.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errors.New("launch-plan does not accept positional arguments (values redacted)")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Parent() != nil && cmd.Parent().Flags().Changed("fix") {
				return errors.New("launch-plan has no --fix path")
			}
			report, err := launchplan.Inspect(req)
			if err != nil {
				return err
			}
			if jsonOutput {
				return launchplan.WriteJSON(cmd.OutOrStdout(), report)
			}
			return launchplan.Render(cmd.OutOrStdout(), report)
		},
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return errors.New("invalid launch-plan option (values redacted)")
	})
	flags := cmd.Flags()
	flags.StringVar(&req.Route, "route", "", "terminal_start, agent_start, or agent_run (required)")
	flags.StringVar(&req.Agent, "agent", "", "explicit adapter for agent_start or agent_run; omit for terminal_start")
	flags.StringVar(&req.CWD, "cwd", "", "proposed workspace (required)")
	flags.StringVar(&req.PathBase, "path-base", "", "absolute base for a relative cwd; never inferred")
	flags.StringVar(&req.Home, "home", "", "explicit absolute provider HOME (required)")
	flags.StringVar(&req.MergeHome, "merge-home", "", "absolute host HOME for inheritance; omitted explicitly uses --home")
	flags.StringVar(&req.PolicyBase, "policy-base", "", "absolute configured root for relative FS rules; omitted uses --cwd for a direct launch")
	flags.StringVar(&req.AllowedHomeRoot, "allowed-home-root", "", "absolute allowed provider-home scope (required)")
	flags.StringVar(&req.Executable, "executable", "", "absolute provider/command executable (required; never run)")
	flags.StringVar(&req.ProviderURL, "provider-url", "", "explicit provider routing URL; only derived host is exported")
	flags.StringVar(&req.ProviderStateDir, "provider-state-dir", "", "explicit provider state override (CODEX_HOME, etc.); never inferred")
	flags.StringVar(&req.PolicyPath, "config", "", "explicit egg policy; omitted explicitly selects built-in defaults")
	flags.StringVar(&req.BasesDir, "bases-dir", "", "explicit named-base directory; no global fallback")
	flags.StringVar(&req.CapabilitiesPath, "facts", "", "absolute saved capability JSON (no probe)")
	flags.StringVar(&req.EvidencePath, "provider-evidence", "", "absolute saved version/help JSON bound to executable SHA256")
	flags.StringArrayVar(&req.CredentialPaths, "credential-path", nil, "explicit credential path within provider home; stat only (repeatable)")
	flags.BoolVar(&req.Unattended, "unattended", false, "plan catalog unattended arguments inside the configured boundary")
	flags.BoolVar(&jsonOutput, "json", false, "print structured diagnosis")
	return cmd
}
