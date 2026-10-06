package main

import (
	"errors"
	"fmt"
	"io"

	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"

	"github.com/spf13/cobra"
)

func sessionPromptCmd() *cobra.Command {
	var requestID string
	var jsonFlag, stdinFlag bool
	var timeout time.Duration
	cmd := &cobra.Command{Use: "prompt <session> [text]", Short: "Submit a retry-safe prompt and observe its exact native transcript receipt", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if stdinFlag && len(args) > 1 {
			return errors.New("provide text arguments or --stdin, not both")
		}
		input := strings.Join(args[1:], " ")
		if stdinFlag {
			data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), egg.MaxSessionPromptBytes+1))
			if err != nil {
				return err
			}
			input = string(data)
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		session, err := eggclient.ResolveLifecycleSession(cfg, args[0])
		if err != nil {
			return err
		}
		result, err := eggclient.PromptSession(cmd.Context(), cfg, session, requestID, input, timeout, "cli:session-prompt")
		if err != nil {
			return err
		}
		if jsonFlag {
			return writeSessionJSON(map[string]any{"session": session.ID, "receipt": result})
		}
		fmt.Printf("%s: %s, request=%s, transport_enqueued=%t, native_receipt_observed=%t\n", session.ID, result.Status, result.RequestID, result.TransportEnqueued, result.NativeReceiptObserved)
		if result.Reason != "" {
			fmt.Println(result.Reason)
		}
		return nil
	}}
	cmd.Flags().StringVar(&requestID, "request-id", "", "caller-chosen retry ID; reuse identical arguments after reconnect")
	cmd.Flags().BoolVar(&stdinFlag, "stdin", false, "read prompt from stdin (up to 65536 bytes)")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Second, "maximum receipt wait (100ms-60s)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}
