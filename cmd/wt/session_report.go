package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/spf13/cobra"
)

func sessionReportCmd() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{Use: "report <session>", Short: "Record a goal, update, next action and optional wake plan from stdin JSON", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var report egg.SessionReport
		decoder := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 16*1024+1))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&report); err != nil {
			return err
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return errors.New("provide exactly one report JSON object")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		session, err := eggclient.ResolveLifecycleSession(cfg, args[0])
		if err != nil {
			return err
		}
		event, err := egg.RecordSessionReport(filepath.Join(cfg.Dir, "eggs", session.ID), report)
		if err != nil {
			return err
		}
		if jsonFlag {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"session": session.ID, "event": event})
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: report recorded at cursor %d\n", session.ID, event.Sequence)
		return err
	}}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}
