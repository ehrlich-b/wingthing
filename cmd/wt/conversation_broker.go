package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/spf13/cobra"
)

func conversationBrokerCmds() []*cobra.Command {
	cmd := &cobra.Command{Use: "broker <session>", Short: "Serve a registered parent's host mailbox (started by the host launcher)", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		err = localmcp.RunConversationBroker(version, ctx, cfg, args[0], os.Stderr)
		if errors.Is(err, localmcp.ErrConversationBrokerRunning) {
			return nil
		}
		return err
	}}
	status := &cobra.Command{Use: "transport <session>", Short: "Inspect a parent execution's host mailbox registration and readiness", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		result, err := localmcp.ConversationBrokerStatus(cfg, args[0])
		if err != nil {
			return err
		}
		return writeSessionJSON(result)
	}}
	status.Flags().Bool("json", true, "print structured transport state")
	return []*cobra.Command{cmd, status}
}
