package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/runinventory"
	"github.com/spf13/cobra"
)

// The CLI uses the same wing tool seam as MCP; no database inventory shortcut.
var callRunInventoryTool = localmcp.CallLocalWingTool

func agentInventoryCmd(recoverIDs bool) *cobra.Command {
	var client, key, cursor string
	var limit, window, maxPages int
	var jsonOutput bool
	use, short := "runs", "List owned active and recent terminal runs"
	if recoverIDs {
		use, short = "recover", "Discover run IDs and recover bounded results without coordinator memory"
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, Example: "  wt agent " + use + " --json --client codex\n  wt agent " + use + " --json --idempotency-key original-key", RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		clientName := strings.TrimSpace(client)
		if clientName == "" {
			clientName = strings.TrimSpace(os.Getenv("WT_MCP_CLIENT"))
		}
		call := func(name string, args json.RawMessage) (map[string]any, error) {
			return callRunInventoryTool(cmd.Context(), cfg.Dir, clientName, name, args)
		}
		if recoverIDs {
			recovered, err := runinventory.Recover(cmd.Context(), func(_ctx context.Context, name string, args json.RawMessage) (map[string]any, error) {
				return call(name, args)
			}, runinventory.Options{Limit: limit, MaxPages: maxPages, RequestKey: key, TerminalWindowSeconds: window})
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(recovered)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			if _, err = fmt.Fprintln(w, "WING\tRUN\tSTATE\tCLEANUP"); err != nil {
				return err
			}
			for _, r := range recovered.Runs {
				if _, err = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.WingID, r.ID, r.State, r.Cleanup); err != nil {
					return err
				}
			}
			return w.Flush()
		}
		args, _ := json.Marshal(map[string]any{"limit": limit, "cursor": cursor, "idempotency_key": key, "terminal_window_seconds": window})
		data, err := call("agent_list", args)
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(data)
		}
		// Human output is a rendering of the same structured response.
		wire, _ := json.Marshal(data)
		var typed struct {
			Runs []struct {
				WingID string `json:"wing_id"`
				RunID  string `json:"run_id"`
				Status string `json:"status"`
				CWD    string `json:"cwd"`
			}
			NextCursor string `json:"next_cursor"`
		}
		if err = json.Unmarshal(wire, &typed); err != nil {
			return err
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		if _, err = fmt.Fprintln(w, "WING\tRUN\tSTATUS\tCWD"); err != nil {
			return err
		}
		for _, r := range typed.Runs {
			if _, err = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.WingID, r.RunID, r.Status, r.CWD); err != nil {
				return err
			}
		}
		if err = w.Flush(); err != nil {
			return err
		}
		if typed.NextCursor != "" {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Next page: --cursor %s\n", typed.NextCursor)
		}
		return err
	}}
	cmd.Flags().StringVar(&client, "client", "", "local MCP client name (or WT_MCP_CLIENT)")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "exact original admission key, including older terminal runs")
	cmd.Flags().IntVar(&limit, "limit", 100, "page size (1-200)")
	cmd.Flags().IntVar(&window, "terminal-window-seconds", 604800, "recent terminal history window (3600-2592000 seconds)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "print the structured JSON response")
	if recoverIDs {
		cmd.Flags().IntVar(&maxPages, "max-pages", 20, "discovery page bound (1-100); an unfinished scan is an error")
	} else {
		cmd.Flags().StringVar(&cursor, "cursor", "", "opaque continuation cursor from agent_list")
	}
	return cmd
}
