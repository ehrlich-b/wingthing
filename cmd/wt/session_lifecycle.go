package main

import (
	"context"
	"errors"
	"fmt"

	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/spf13/cobra"
)

func sessionLifecycleCmd() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{Use: "status <session>", Short: "Read native agent lifecycle (independent of terminal activity)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		view, err := eggclient.ReadSessionLifecycleView(cmd.Context(), cfg, args[0], 0, 1)
		if err != nil {
			return err
		}
		view.Events = []egg.SessionEvent{}
		view.Cursor = view.HeadCursor
		view.HasMore = false
		if jsonFlag {
			return writeSessionJSON(localmcp.LifecycleResult(view))
		}
		fmt.Printf("%s: %s (%s), ready=%t, process_alive=%t\n", view.SessionID, view.State, view.StateSource, view.Ready, view.ProcessAlive)
		return nil
	}}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func sessionTranscriptCmd() *cobra.Command {
	var jsonFlag bool
	var after int64
	var limit int
	cmd := &cobra.Command{Use: "transcript <session>", Short: "Read exact-session native conversation and lifecycle events", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		view, err := eggclient.ReadSessionLifecycleView(cmd.Context(), cfg, args[0], after, limit)
		if err != nil {
			return err
		}
		if jsonFlag {
			return writeSessionJSON(localmcp.LifecycleResult(view))
		}
		for _, e := range view.Events {
			fmt.Printf("%d %s %s %s\n", e.Sequence, e.Type, e.Role, e.Text)
		}
		return nil
	}}
	cmd.Flags().Int64Var(&after, "after-cursor", 0, "read events after this durable cursor")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum events (1-200)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func sessionAwaitCmd() *cobra.Command {
	var jsonFlag bool
	var after int64
	var state string
	var timeout time.Duration
	cmd := &cobra.Command{Use: "await <session>", Short: "Wait for a native lifecycle state or event", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if timeout < 100*time.Millisecond || timeout > time.Hour {
			return errors.New("timeout must be between 100ms and 1h")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		session, err := eggclient.ResolveLifecycleSession(cfg, args[0])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		view, matched, err := eggclient.WaitSessionLifecycle(ctx, cfg, session, after, state)
		if err != nil {
			return err
		}
		result := localmcp.LifecycleResult(view)
		result["matched"] = matched
		result["timed_out"] = !matched
		if jsonFlag {
			return writeSessionJSON(result)
		}
		fmt.Printf("%s: %s, matched=%t\n", view.SessionID, view.State, matched)
		return nil
	}}
	cmd.Flags().Int64Var(&after, "after-cursor", 0, "wait for evidence after this durable cursor")
	cmd.Flags().StringVar(&state, "state", "", "native state to wait for, or ready; omit to wait for any new event")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "maximum wait (100ms-1h)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}
