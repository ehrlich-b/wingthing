package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
)

func connectMCPCmd() *cobra.Command {
	var clientName string
	var roost string
	var connectTimeout time.Duration
	command := &cobra.Command{
		Use:   "connect",
		Short: "Manage agents on remote wings over direct encrypted connections",
		Long: "Run one local MCP server for every accessible wing. Wingthing uses the roost for " +
			"identity, inventory, and WebRTC signaling; control payloads go directly to the selected wing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			actor := strings.TrimSpace(clientName)
			if actor == "" {
				actor = strings.TrimSpace(os.Getenv("WT_MCP_CLIENT"))
			}
			if actor == "" {
				actor = "default"
			}
			if err := eggclient.ValidateSessionName(actor); err != nil {
				return fmt.Errorf("invalid MCP client name: %w", err)
			}
			tokenStore := auth.NewTokenStore(cfg.Dir)
			token, err := tokenStore.Load()
			if err != nil || !tokenStore.IsValid(token) {
				return fmt.Errorf("not logged in — run: wt login --roost <url>")
			}
			privateKey, err := auth.LoadPrivateKey(cfg.Dir)
			if err != nil {
				return fmt.Errorf("load native client key: %w", err)
			}
			server := &localmcp.ConnectMCPServer{Version: version,
				In: os.Stdin, Out: os.Stdout, Actor: actor, Timeout: connectTimeout,
				Tunnel: &ws.TunnelClient{
					RelayURL: finderRelayURL(cfg, roost), DeviceToken: token.Token,
					PrivKey: privateKey, KnownWingsPath: filepath.Join(cfg.Dir, "known_wings.json"),
				},
				Controls: make(map[string]*webrtcpkg.ControlClient),
			}
			defer server.Close()
			return server.Serve(cmd.Context())
		},
	}
	legacy := remoteCmd()
	for _, sub := range legacy.Commands() {
		if sub.Name() == "ls" || sub.Name() == "rm" {
			command.AddCommand(sub)
		}
	}
	command.AddCommand(addConnectMCPCmd(&clientName, &connectTimeout))
	command.PersistentFlags().StringVar(&clientName, "client", "", "MCP actor name used for attribution (or WT_MCP_CLIENT)")
	command.Flags().StringVar(&roost, "roost", "", "coordination roost URL (default: config or wingthing.ai)")
	command.PersistentFlags().DurationVar(&connectTimeout, "connect-timeout", 15*time.Second, "deadline for establishing each direct wing connection")
	return command
}
