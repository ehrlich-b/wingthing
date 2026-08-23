package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/spf13/cobra"
)

func sessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Inspect and automate persistent terminal sessions",
	}
	cmd.AddCommand(sessionSyncCmd())
	cmd.AddCommand(sessionListCmd())
	cmd.AddCommand(sessionPSCmd())
	cmd.AddCommand(sessionReadCmd())
	cmd.AddCommand(sessionSendCmd())
	cmd.AddCommand(sessionWaitCmd())
	cmd.AddCommand(sessionRenameCmd())
	cmd.AddCommand(sessionKillCmd())
	return cmd
}

func sessionPSCmd() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:     "ps",
		Aliases: []string{"active"},
		Short:   "List active local terminal sessions",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return printActiveSessions(cmd.Context(), cfg, jsonFlag)
		},
	}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func sessionReadCmd() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:   "read <session>",
		Short: "Print the current terminal snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			session, snapshot, err := readSessionSnapshot(cmd.Context(), cfg, args[0])
			if err != nil {
				return err
			}
			if jsonFlag {
				return writeSessionJSON(map[string]any{
					"session": session.ID, "label": session.Name, "ansi": string(snapshot),
					"base64": base64.StdEncoding.EncodeToString(snapshot), "byte_length": len(snapshot),
				})
			}
			_, err = os.Stdout.Write(snapshot)
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func sessionSendCmd() *cobra.Command {
	var enterFlag bool
	var stdinFlag bool
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:   "send <session> [text]",
		Short: "Send input to a terminal session",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if stdinFlag && len(args) > 1 {
				return fmt.Errorf("provide text arguments or --stdin, not both")
			}
			var input []byte
			if stdinFlag {
				var err error
				input, err = io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
			} else if len(args) > 1 {
				input = []byte(strings.Join(args[1:], " "))
			}
			if len(input) == 0 && !enterFlag {
				return fmt.Errorf("provide text, --stdin, or --enter")
			}
			if enterFlag {
				input = append(input, '\r')
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			session, err := sendSessionBytes(cmd.Context(), cfg, args[0], input)
			if err != nil {
				return err
			}
			if jsonFlag {
				return writeSessionJSON(map[string]any{"session": session.ID, "bytes_sent": len(input)})
			}
			fmt.Printf("sent %d bytes to %s\n", len(input), session.ID)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&enterFlag, "enter", "e", false, "append Enter after the input")
	cmd.Flags().BoolVar(&stdinFlag, "stdin", false, "read input bytes from stdin")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func sessionWaitCmd() *cobra.Command {
	var containsFlag string
	var idleFlag time.Duration
	var timeoutFlag time.Duration
	var jsonFlag bool

	cmd := &cobra.Command{
		Use:   "wait <session>",
		Short: "Wait for terminal output or an idle session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if containsFlag != "" && cmd.Flags().Changed("idle") {
				return fmt.Errorf("--contains and --idle are mutually exclusive")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			waitCtx := cmd.Context()
			var cancel context.CancelFunc
			if timeoutFlag > 0 {
				waitCtx, cancel = context.WithTimeout(waitCtx, timeoutFlag)
				defer cancel()
			}

			if containsFlag != "" {
				session, waitErr := waitForSessionText(waitCtx, cfg, args[0], containsFlag)
				if waitErr != nil {
					return waitErr
				}
				if jsonFlag {
					return writeSessionJSON(map[string]any{"session": session.ID, "condition": "contains", "value": containsFlag})
				}
				fmt.Printf("session %s produced %q\n", session.ID, containsFlag)
				return nil
			}

			if idleFlag <= 0 {
				idleFlag = 2 * time.Second
			}
			session, ec, err := openLocalEgg(waitCtx, cfg, args[0])
			if err != nil {
				return err
			}
			defer ec.Close()
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				statusResponse, statusErr := ec.Status(waitCtx)
				if statusErr != nil {
					if waitCtx.Err() != nil {
						return waitCtx.Err()
					}
					return fmt.Errorf("wait for session %s: %w", session.ID, statusErr)
				}
				if time.Duration(statusResponse.IdleSeconds)*time.Second >= idleFlag {
					if jsonFlag {
						return writeSessionJSON(map[string]any{"session": session.ID, "condition": "idle", "idle_seconds": statusResponse.IdleSeconds})
					}
					fmt.Printf("session %s idle for %s\n", session.ID, humanDuration(time.Duration(statusResponse.IdleSeconds)*time.Second))
					return nil
				}
				select {
				case <-waitCtx.Done():
					return waitCtx.Err()
				case <-ticker.C:
				}
			}
		},
	}
	cmd.Flags().StringVar(&containsFlag, "contains", "", "wait until the terminal output contains this text")
	cmd.Flags().DurationVar(&idleFlag, "idle", 0, "wait until the session has been idle for this duration (default 2s)")
	cmd.Flags().DurationVarP(&timeoutFlag, "timeout", "t", 30*time.Second, "maximum time to wait; 0 disables the timeout")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func sessionRenameCmd() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:   "rename <session> <name>",
		Short: "Give an active session a human-readable name",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] == "" {
				return fmt.Errorf("session name cannot be empty")
			}
			if err := validateSessionName(args[1]); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			session, err := resolveActiveSession(cmd.Context(), cfg, args[0])
			if err != nil {
				return err
			}
			if err := ensureSessionNameAvailable(cfg, args[1], session.ID); err != nil {
				return err
			}
			if err := writeSessionName(filepath.Join(cfg.Dir, "eggs", session.ID), args[1]); err != nil {
				return err
			}
			if jsonFlag {
				return writeSessionJSON(map[string]any{"session": session.ID, "name": args[1]})
			}
			fmt.Printf("session %s named %s\n", session.ID, args[1])
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func sessionKillCmd() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:     "kill <session>",
		Aliases: []string{"stop"},
		Short:   "Stop a persistent terminal session",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			session, ec, err := openLocalEgg(cmd.Context(), cfg, args[0])
			if err != nil {
				return err
			}
			defer ec.Close()
			if err := ec.Kill(cmd.Context(), session.ID); err != nil {
				return fmt.Errorf("kill session %s: %w", session.ID, err)
			}
			if jsonFlag {
				return writeSessionJSON(map[string]any{"session": session.ID, "status": "stopped"})
			}
			fmt.Printf("session %s stopped\n", session.ID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable JSON")
	return cmd
}

func writeSessionJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
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
				RelayURL:       relayURL,
				DeviceToken:    tok.Token,
				PrivKey:        privKey,
				KnownWingsPath: filepath.Join(cfg.Dir, "known_wings.json"),
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
