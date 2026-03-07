package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
)

func sessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage egg sessions",
	}
	cmd.AddCommand(sessionSyncCmd())
	cmd.AddCommand(sessionListCmd())
	return cmd
}

func sessionSyncCmd() *cobra.Command {
	var fromFlag string

	cmd := &cobra.Command{
		Use:   "sync <session-id>",
		Short: "Sync chat history from a remote wing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := args[0]
			if fromFlag == "" {
				return fmt.Errorf("--from is required (wing ID)")
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			ts := auth.NewTokenStore(cfg.Dir)
			tok, err := ts.Load()
			if err != nil || !ts.IsValid(tok) {
				return fmt.Errorf("not logged in — run: wt login")
			}

			privKey, err := auth.LoadPrivateKey(cfg.Dir)
			if err != nil {
				return fmt.Errorf("load key: %w", err)
			}

			relayURL := resolveRelayHTTPURL(cfg)
			tc := &ws.TunnelClient{
				RelayURL:    relayURL,
				DeviceToken: tok.Token,
				PrivKey:     privKey,
			}

			// Discover target wing
			wing, err := tc.DiscoverWing(cmd.Context(), fromFlag)
			if err != nil {
				return fmt.Errorf("discover wing: %w", err)
			}

			// Create local egg dir for the session
			eggDir := filepath.Join(cfg.Dir, "eggs", sessionID)
			if err := os.MkdirAll(eggDir, 0700); err != nil {
				return fmt.Errorf("create dir: %w", err)
			}

			// Stream chat history from remote wing
			fmt.Printf("syncing session %s from wing %s...\n", sessionID, fromFlag)
			var chatData []byte
			err = tc.Stream(cmd.Context(), fromFlag, wing.PublicKey,
				map[string]string{
					"type":       "audit.request",
					"session_id": sessionID,
					"kind":       "chat",
				},
				func(chunk []byte) error {
					var c struct {
						Data string `json:"data"`
						Done bool   `json:"done"`
					}
					if err := json.Unmarshal(chunk, &c); err != nil {
						return nil
					}
					if c.Done {
						return nil
					}
					if c.Data != "" {
						decoded, err := base64.StdEncoding.DecodeString(c.Data)
						if err != nil {
							return fmt.Errorf("decode chunk: %w", err)
						}
						chatData = append(chatData, decoded...)
					}
					return nil
				},
			)
			if err != nil {
				return fmt.Errorf("stream: %w", err)
			}

			if len(chatData) == 0 {
				return fmt.Errorf("no chat history for session %s on wing %s", sessionID, fromFlag)
			}

			// Write chat.jsonl.gz
			gzPath := filepath.Join(eggDir, "chat.jsonl.gz")
			if err := os.WriteFile(gzPath, chatData, 0644); err != nil {
				return fmt.Errorf("write chat: %w", err)
			}

			// Also sync egg.meta for agent info
			metaSrc := filepath.Join(eggDir, "chat.meta")
			if _, err := os.Stat(metaSrc); os.IsNotExist(err) {
				// Try to get meta from the remote egg.meta
				// For now, create a minimal meta from what we know
				// The user will need to specify the agent when resuming
				fmt.Printf("synced %s (%d bytes)\n", sessionID, len(chatData))
				fmt.Println("note: no chat.meta — you may need to create one manually")
				fmt.Printf("  resume with: wt egg <agent> --resume %s\n", sessionID)
				return nil
			}

			// Read meta to get agent name
			metaData, _ := os.ReadFile(metaSrc)
			meta := egg.ParseChatMeta(string(metaData))
			agent := meta["agent"]
			if agent == "" {
				agent = "<agent>"
			}

			fmt.Printf("synced session %s (%s)\n", sessionID, agent)
			fmt.Printf("  resume with: wt egg %s --resume %s\n", agent, sessionID)
			return nil
		},
	}

	cmd.Flags().StringVar(&fromFlag, "from", "", "source wing ID")
	cmd.MarkFlagRequired("from")
	return cmd
}

func sessionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List local sessions with chat history",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			eggsDir := filepath.Join(cfg.Dir, "eggs")
			entries, err := os.ReadDir(eggsDir)
			if err != nil {
				fmt.Println("no sessions")
				return nil
			}

			found := false
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				dir := filepath.Join(eggsDir, e.Name())
				if _, err := os.Stat(filepath.Join(dir, "chat.jsonl.gz")); err != nil {
					continue
				}

				agent, cwd := readEggMeta(dir)
				metaPath := filepath.Join(dir, "chat.meta")
				if metaData, err := os.ReadFile(metaPath); err == nil {
					meta := egg.ParseChatMeta(string(metaData))
					if a, ok := meta["agent"]; ok && a != "" {
						agent = a
					}
					if c, ok := meta["cwd"]; ok && c != "" {
						cwd = c
					}
				}
				if agent == "" {
					agent = "unknown"
				}

				info, _ := os.Stat(filepath.Join(dir, "chat.jsonl.gz"))
				size := ""
				if info != nil {
					size = humanBytes(info.Size())
				}

				fmt.Printf("  %s  agent=%s  chat=%s", e.Name(), agent, size)
				if cwd != "" {
					fmt.Printf("  cwd=%s", shortenPath(cwd))
				}
				fmt.Println()
				found = true
			}

			if !found {
				fmt.Println("no sessions with chat history")
			}
			return nil
		},
	}
}

// shortenPath shortens a path for display by replacing home dir with ~.
func shortenPath(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}
