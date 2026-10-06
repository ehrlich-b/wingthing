package main

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/spf13/cobra"
)

func conversationWakeCmd(client *string) *cobra.Command {
	cmd := &cobra.Command{Use: "wake <conversation> [enable|disable|status|retry]", Short: "Opt a personal parent into durable child lifecycle wake messages", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		input := map[string]any{"conversation_id": args[0]}
		if len(args) == 2 {
			switch args[1] {
			case "enable":
				input["enabled"] = true
			case "disable":
				input["enabled"] = false
			case "status":
			case "retry":
				input["retry_not_sent"] = true
			default:
				return errors.New("choose enable, disable, status, or retry")
			}
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		s := &localmcp.Server{Version: version, Cfg: cfg, Principal: *client, Logs: os.Stderr}
		data, _ := json.Marshal(input)
		result, err := s.ToolConversationWake(data)
		if err != nil {
			return err
		}
		return writeSessionJSON(result)
	}}
	return cmd
}
