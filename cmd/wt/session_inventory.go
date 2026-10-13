package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/spf13/cobra"
)

func sessionInventoryCmd() *cobra.Command {
	var query string
	var remotes, jsonFlag bool
	cmd := &cobra.Command{Use: "inventory", Short: "Search qualified live and past sessions with route and coverage data", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, e := config.Load()
		if e != nil {
			return e
		}
		raw, _ := json.Marshal(map[string]any{"query": query, "include_remotes": remotes})
		result, e := localmcp.CallLocalWingTool(cmd.Context(), cfg.Dir, "", "session_inventory", raw)
		if e != nil {
			return e
		}
		if jsonFlag {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		data, _ := json.Marshal(result)
		var inv fleet.Inventory
		if e = json.Unmarshal(data, &inv); e != nil {
			return e
		}
		return renderSessionInventory(cmd.OutOrStdout(), inv)
	}}
	cmd.Flags().StringVar(&query, "query", "", "search name, repo, machine and transcript text")
	cmd.Flags().BoolVar(&remotes, "remotes", false, "include verified saved personal remotes")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}
func renderSessionInventory(out io.Writer, inv fleet.Inventory) error {
	fmt.Fprintf(out, "Sessions everywhere · complete=%t\n", inv.Complete)
	for _, source := range inv.Sources {
		fmt.Fprintf(out, "Source %s · healthy=%t · %s\n", source.MachineLabel, source.Healthy, source.ObservedAt)
		if source.Error != "" {
			fmt.Fprintf(out, "  %s\n", source.Error)
		}
	}
	for _, s := range inv.Sessions {
		pinned := ""
		if s.Pinned && s.Role == "coordinator" {
			pinned = "PINNED wing · "
		}
		fmt.Fprintf(out, "\n%s%s · %s · %s · %s\n", pinned, s.Name, s.Agent, s.MachineLabel, s.Status)
		fmt.Fprintf(out, "  %s / %s · live=%t · %s\n", s.OriginWingID, s.SessionID, s.Live, s.CWD)
		for _, r := range s.Routes {
			label := r.Kind
			if r.Kind == "via" {
				label += " " + r.MachineLabel
			}
			fmt.Fprintf(out, "  route: %s · healthy=%t\n", label, r.Healthy)
		}
		if route, err := fleet.SelectRoute(s, time.Now()); err == nil {
			label := route.Kind
			if route.Kind == "via" {
				label += " " + route.MachineLabel
			}
			fmt.Fprintf(out, "  selected: %s\n", label)
		} else {
			fmt.Fprintf(out, "  controls unavailable: %s\n", err)
		}
		if s.PossibleDuplicate {
			fmt.Fprintln(out, "  possible duplicate · origin identity unproven")
		}
		if s.Presence != nil {
			p := s.Presence
			fmt.Fprintf(out, "  %s · last wake %s · next wake %s · stale=%t\n", p.State, p.LastWake, p.NextWake, p.Stale)
			fmt.Fprintf(out, "  last note %s · source %s · observed %s\n", p.LastNote, p.Source, p.ObservedAt)
		}
		if s.NeedsAttention {
			fmt.Fprintln(out, "  needs your attention")
		}
		if len(s.MatchFields) > 0 {
			fmt.Fprintf(out, "  matched: %v\n", s.MatchFields)
		}
	}
	return nil
}
func sessionMergeCmd() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{Use: "merge <inventory.json>...", Short: "Merge inventories from separately authenticated wings", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var inventories []fleet.Inventory
		for _, path := range args {
			f, e := os.Open(path)
			if e != nil {
				return e
			}
			var inv fleet.Inventory
			e = json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&inv)
			_ = f.Close()
			if e != nil {
				return e
			}
			if inv.Version != fleet.Version {
				return fmt.Errorf("unsupported inventory contract")
			}
			inventories = append(inventories, inv)
		}
		inv := fleet.Merge(inventories)
		if jsonFlag {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(inv)
		}
		return renderSessionInventory(cmd.OutOrStdout(), inv)
	}}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}
func sessionActionCmd() *cobra.Command {
	var origin, id, operation, args string
	var jsonFlag bool
	cmd := &cobra.Command{Use: "action", Short: "Run one existing operation on an exact wing/session identity", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, e := config.Load()
		if e != nil {
			return e
		}
		raw, e := json.Marshal(map[string]any{"origin_wing_id": origin, "session_id": id, "operation": operation, "arguments": json.RawMessage(args)})
		if e != nil {
			return e
		}
		result, e := localmcp.CallLocalWingTool(cmd.Context(), cfg.Dir, "", "session_action", raw)
		if e != nil {
			return e
		}
		if jsonFlag {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		data, _ := json.Marshal(result["route"])
		var route fleet.Route
		_ = json.Unmarshal(data, &route)
		label := route.Kind
		if route.Kind == "via" {
			label += " " + route.MachineLabel
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "%s / %s\nOperation %s · route %s\n", origin, id, operation, label)
		underlying, _ := result["result"].(map[string]any)
		if raw, ok := underlying["lifecycle"]; ok {
			data, _ := json.Marshal(raw)
			var view egg.SessionView
			_ = json.Unmarshal(data, &view)
			fmt.Fprintf(out, "%s · %s · process_alive=%t\n", view.Agent, view.State, view.ProcessAlive)
			for _, event := range view.Events {
				fmt.Fprintf(out, "%d %s: %s\n", event.Sequence, event.Role, event.Text)
			}
			return nil
		}
		return json.NewEncoder(out).Encode(underlying)
	}}
	cmd.Flags().StringVar(&origin, "wing", "", "execution origin wing ID")
	cmd.Flags().StringVar(&id, "session", "", "full exact session ID")
	cmd.Flags().StringVar(&operation, "operation", "session_read", "existing session operation")
	cmd.Flags().StringVar(&args, "arguments", "{}", "operation arguments as JSON, without session selector")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}
func sessionLaunchCmd() *cobra.Command {
	var origin, operation, arguments string
	var jsonFlag bool
	cmd := &cobra.Command{Use: "launch", Short: "Start one session on an explicit personal wing", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		raw, err := json.Marshal(map[string]any{"origin_wing_id": origin, "operation": operation, "arguments": json.RawMessage(arguments)})
		if err != nil {
			return err
		}
		result, err := localmcp.CallLocalWingTool(cmd.Context(), cfg.Dir, "", "session_launch", raw)
		if err != nil {
			return err
		}
		if !jsonFlag {
			data, _ := json.Marshal(result["route"])
			var route fleet.Route
			_ = json.Unmarshal(data, &route)
			fmt.Fprintf(cmd.OutOrStdout(), "%s / %s\nLaunched %s · route %s", origin, result["session_id"], operation, route.Kind)
			if route.Kind == "via" {
				fmt.Fprintf(cmd.OutOrStdout(), " %s", route.MachineLabel)
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	cmd.Flags().StringVar(&origin, "wing", "", "execution origin wing ID")
	cmd.Flags().StringVar(&operation, "operation", "terminal_start", "terminal_start or agent_start")
	cmd.Flags().StringVar(&arguments, "arguments", "{}", "existing launch arguments as JSON; cwd is on the execution wing")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func sessionHeartbeatCmd() *cobra.Command {
	var state, last, next, note string
	var jsonFlag bool
	cmd := &cobra.Command{Use: "heartbeat <session>", Short: "Record coordinator presence without arming a wake timer", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, e := config.Load()
		if e != nil {
			return e
		}
		raw, _ := json.Marshal(map[string]any{"session": args[0], "state": state, "last_wake": last, "next_wake": next, "last_note": note})
		result, e := localmcp.CallLocalWingTool(cmd.Context(), cfg.Dir, "", "coordinator_heartbeat", raw)
		if e != nil {
			return e
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}
	cmd.Flags().StringVar(&state, "state", "awake", "awake or asleep")
	cmd.Flags().StringVar(&last, "last-wake", "", "RFC3339 last wake time")
	cmd.Flags().StringVar(&next, "next-wake", "", "RFC3339 planned next wake time")
	cmd.Flags().StringVar(&note, "last-note", "", "RFC3339 last note time")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}
