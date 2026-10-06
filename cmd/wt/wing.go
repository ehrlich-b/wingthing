package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/wing"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/spf13/cobra"
)

func wingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "daemon",
		Aliases: []string{"wing"},
		Short:   "Connect this machine to a relay, accessible from anywhere",
		Long:    "Makes this machine reachable from anywhere via the relay.\nUse 'wt daemon start' to go online, 'wt daemon status' to check.",
	}

	cmd.AddCommand(wingStartCmd())
	cmd.AddCommand(wingStopCmd())
	cmd.AddCommand(wingStatusCmd())
	cmd.AddCommand(wingAllowCmd())
	cmd.AddCommand(wingRevokeCmd())
	cmd.AddCommand(wingLockCmd())
	cmd.AddCommand(wingUnlockCmd())
	cmd.AddCommand(wingConfigCmd())

	return cmd
}

func wingStartCmd() *cobra.Command {
	var roostFlag string
	var labelsFlag string
	var convFlag string
	var foregroundFlag bool
	var debugFlag bool
	var eggConfigFlag string
	var orgFlag string
	var allowFlags []string
	var pathsFlag string
	var auditFlag bool
	var localFlag bool
	var rawReplayFlag bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start wing daemon and go online",
		Long:  "Start a wing — your machine becomes reachable from anywhere via the roost. Runs as a background daemon by default. Use --foreground for debugging.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Foreground mode: run directly
			if foregroundFlag {
				return runWingForeground(cmd, roostFlag, labelsFlag, convFlag, eggConfigFlag, orgFlag, allowFlags, pathsFlag, debugFlag, auditFlag, localFlag, !rawReplayFlag)
			}
			lifecycleLock, err := daemonctl.AcquireDaemonLifecycleLock()
			if err != nil {
				return err
			}
			defer cmdutil.CloseWithLog("daemon lifecycle lock", lifecycleLock)

			// Daemon mode (default): re-exec detached, write PID file, return
			if pid, _, err := daemonctl.ReadDaemon(); err == nil {
				return fmt.Errorf("wing daemon already running (pid %d)", pid)
			} else if !errors.Is(err, daemonctl.ErrNoDaemonRunning) {
				return fmt.Errorf("inspect daemon state: %w", err)
			}

			// Pre-flight auth probe: catch expired tokens before spawning daemon
			if !localFlag || roostFlag != "" {
				cfg, cfgErr := config.Load()
				if cfgErr == nil {
					ts := auth.NewTokenStore(cfg.Dir)
					tok, tokErr := ts.Load()
					if tokErr != nil || !ts.IsValid(tok) {
						return fmt.Errorf("not logged in — run: wt login")
					}
					// Use the same precedence and normalization as the child daemon.
					relayURL := wingpolicy.ResolveWingRelayHTTPURL(cfg, roostFlag, localFlag)
					if err := auth.ValidateTokenRemote(relayURL, tok.Token); err != nil {
						if errors.Is(err, auth.ErrAuthFailed) {
							return fmt.Errorf("login expired — run: wt login")
						}
						// Network error: warn but proceed (daemon will retry)
						fmt.Printf("warning: relay unreachable (%v) — starting daemon anyway\n", err)
					}
				}
			}

			exe, err := os.Executable()
			if err != nil {
				return err
			}

			// Build args for foreground child
			var childArgs []string
			childArgs = append(childArgs, "wing", "start", "--foreground")
			if roostFlag != "" {
				childArgs = append(childArgs, "--roost", roostFlag)
			}
			if labelsFlag != "" {
				childArgs = append(childArgs, "--labels", labelsFlag)
			}
			if convFlag != "auto" {
				childArgs = append(childArgs, "--conv", convFlag)
			}
			if eggConfigFlag != "" {
				childArgs = append(childArgs, "--egg-config", eggConfigFlag)
			}
			if orgFlag != "" {
				childArgs = append(childArgs, "--org", orgFlag)
			}
			for _, ak := range allowFlags {
				childArgs = append(childArgs, "--allow", ak)
			}
			if pathsFlag != "" {
				childArgs = append(childArgs, "--paths", pathsFlag)
			}
			if debugFlag {
				childArgs = append(childArgs, "--debug")
			}
			if auditFlag {
				childArgs = append(childArgs, "--audit")
			}
			if localFlag {
				childArgs = append(childArgs, "--local")
			}
			if rawReplayFlag {
				childArgs = append(childArgs, "--raw-replay")
			}

			// Remove stale status from previous run
			if err := cmdutil.RemoveIfExists(daemonctl.WingStatusPath()); err != nil {
				return fmt.Errorf("remove stale wing status: %w", err)
			}

			if err := daemonctl.RotateLog(daemonctl.WingLogPath()); err != nil {
				return err
			}
			logFile, err := os.OpenFile(daemonctl.WingLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				return fmt.Errorf("open log: %w", err)
			}

			home, err := os.UserHomeDir()
			if err != nil {
				cmdutil.CloseWithLog("wing log", logFile)
				return fmt.Errorf("resolve user home: %w", err)
			}

			child := exec.Command(exe, childArgs...)
			child.Dir = home
			child.Stdout = logFile
			child.Stderr = logFile
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

			if err := child.Start(); err != nil {
				cmdutil.CloseWithLog("wing log", logFile)
				return fmt.Errorf("start daemon: %w", err)
			}
			if err := logFile.Close(); err != nil {
				daemonctl.AbandonStartedDaemon(child)
				return fmt.Errorf("close wing log: %w", err)
			}

			if err := daemonctl.WriteDaemonMetadata(daemonctl.WingPidPath(), daemonctl.WingArgsPath(), child.Process.Pid, childArgs); err != nil {
				daemonctl.AbandonStartedDaemon(child)
				return fmt.Errorf("start daemon: %w", err)
			}

			// Wait for daemon to report initial connection state
			startupResult := daemonctl.WaitForWingStatus(child.Process.Pid, 5*time.Second)
			switch startupResult {
			case "auth_failed":
				// Kill daemon, clean up
				daemonctl.AbandonStartedDaemon(child)
				if err := cmdutil.RemoveFiles(daemonctl.WingPidPath(), daemonctl.WingArgsPath(), daemonctl.WingStatusPath()); err != nil {
					return errors.Join(fmt.Errorf("login expired — run: wt login"), fmt.Errorf("remove failed daemon metadata: %w", err))
				}
				return fmt.Errorf("login expired — run: wt login")
			case "connected":
				fmt.Printf("wing daemon started (pid %d)\n", child.Process.Pid)
				fmt.Printf("  relay: connected\n")
			default:
				// Timeout or still connecting — daemon is running, relay might be slow
				fmt.Printf("wing daemon started (pid %d)\n", child.Process.Pid)
				fmt.Printf("  relay: connecting...\n")
			}
			if err := child.Process.Release(); err != nil {
				log.Printf("warning: failed to release daemon process handle: %v", err)
			}
			// Show account identity
			if cfgLoaded, cfgErr := config.Load(); cfgErr == nil {
				relayURL := wingpolicy.ResolveWingRelayHTTPURL(cfgLoaded, roostFlag, localFlag)
				if tok, tokErr := auth.NewTokenStore(cfgLoaded.Dir).Load(); tokErr == nil && tok != nil {
					if info, infoErr := auth.FetchUserInfo(relayURL, tok.Token); infoErr == nil {
						fmt.Printf("  account: %s\n", formatUserIdentity(info))
					}
				}
			}
			fmt.Printf("  log: %s\n", daemonctl.WingLogPath())
			fmt.Println()
			if cfgLoaded, cfgErr := config.Load(); cfgErr == nil {
				browserURL := wingpolicy.RoostBrowserURL(wingpolicy.ResolveWingRelayHTTPURL(cfgLoaded, roostFlag, localFlag))
				fmt.Printf("open %s for wing status and direct-agent setup\n", browserURL)
			} else if localFlag {
				fmt.Println("open http://localhost:8080/app/ for wing status and direct-agent setup")
			} else {
				fmt.Println("open https://app.wingthing.ai/ for wing status and direct-agent setup")
			}
			if !localFlag {
				fmt.Println("hosted browser terminals require relay access")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&roostFlag, "roost", "", "roost server URL (default: ws.wingthing.ai)")
	cmd.Flags().StringVar(&labelsFlag, "labels", "", "comma-separated wing labels (e.g. gpu,cuda,research)")
	cmd.Flags().StringVar(&convFlag, "conv", "auto", "conversation mode: auto (daily rolling), new (fresh), or a named thread")
	cmd.Flags().BoolVar(&foregroundFlag, "foreground", false, "run in foreground instead of daemonizing")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output to /tmp/wt-pty-<session>.bin for each egg")
	cmd.Flags().StringVar(&eggConfigFlag, "egg-config", "", "path to egg.yaml for wing-level sandbox defaults")
	cmd.Flags().StringVar(&orgFlag, "org", "", "org name or ID — share this wing with org members")
	cmd.Flags().StringSliceVar(&allowFlags, "allow", nil, "ephemeral passkey public key(s) for this session")
	cmd.Flags().StringVar(&pathsFlag, "paths", "", "comma-separated directories the wing can browse (default: ~/)")
	cmd.Flags().BoolVar(&auditFlag, "audit", false, "enable audit logging for all egg sessions")
	cmd.Flags().BoolVar(&localFlag, "local", false, "connect to localhost:8080 (for self-hosted wt serve)")
	cmd.Flags().BoolVar(&rawReplayFlag, "raw-replay", false, "use raw replay buffer for reconnect instead of VTerm snapshot")

	return cmd
}

func runWingForeground(cmd *cobra.Command, roostFlag, labelsFlag, convFlag, eggConfigFlag, orgFlag string, allowFlags []string, pathsFlag string, debug, audit, local, vte bool) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer cmdutil.RemoveWithLog(daemonctl.WingStatusPath())

	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	defer signal.Stop(sighupCh)

	return wing.RunWingWithContext(wing.EntryOptions{Version: version}, ctx, sighupCh, roostFlag, labelsFlag, convFlag, eggConfigFlag, orgFlag, allowFlags, pathsFlag, debug, audit, local, vte, false, nil)
}

func wingStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the wing daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			lifecycleLock, lockErr := daemonctl.AcquireDaemonLifecycleLock()
			if lockErr != nil {
				return lockErr
			}
			defer cmdutil.CloseWithLog("daemon lifecycle lock", lifecycleLock)
			pid, kind, err := daemonctl.ReadDaemon()
			if err != nil {
				return fmt.Errorf("no wing daemon running")
			}
			if err := daemonctl.StopDaemonAndWait(pid, kind, 5*time.Second); err != nil {
				return err
			}
			if err := cmdutil.RemoveFiles(daemonctl.WingPidPath(), daemonctl.WingArgsPath(), daemonctl.WingStatusPath()); err != nil {
				return fmt.Errorf("remove wing daemon metadata: %w", err)
			}
			fmt.Printf("wing daemon stopped (pid %d)\n", pid)
			return nil
		},
	}
}

func wingStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check wing daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := daemonctl.ReadPid()
			if err != nil {
				if errors.Is(err, daemonctl.ErrNoDaemonRunning) {
					fmt.Println("wing daemon is not running")
					return nil
				}
				return fmt.Errorf("inspect daemon state: %w", err)
			}
			fmt.Printf("wing daemon is running (pid %d)\n", pid)

			cfg, _ := config.Load()
			status, _ := daemonctl.ReadWingStatus()

			// Show account identity and relay verification
			var relayVerified bool
			if cfg != nil {
				relayURL := daemonctl.ActiveWingRelayHTTPURL(cfg, status)
				if tok, tokErr := auth.NewTokenStore(cfg.Dir).Load(); tokErr == nil && tok != nil {
					if info, infoErr := auth.FetchUserInfo(relayURL, tok.Token); infoErr == nil {
						fmt.Printf("  account: %s\n", formatUserIdentity(info))
						relayVerified = true
					} else if errors.Is(infoErr, auth.ErrAuthFailed) {
						fmt.Println("  account: token expired — run: wt logout && wt login")
					} else {
						fmt.Printf("  account: relay unreachable (%v)\n", infoErr)
					}
				}
			}

			// Show relay connection state
			if status != nil {
				switch status.State {
				case "connected":
					if relayVerified {
						fmt.Println("  relay: connected (verified)")
					} else {
						fmt.Println("  relay: connected")
					}
				case "auth_failed":
					fmt.Println("  relay: auth_failed — run: wt login")
				case "connecting":
					fmt.Println("  relay: connecting...")
				case "disconnected":
					if status.Error != "" {
						fmt.Printf("  relay: disconnected (%s)\n", status.Error)
					} else {
						fmt.Println("  relay: disconnected")
					}
				default:
					fmt.Printf("  relay: %s\n", status.State)
				}
			}

			if cfg != nil {
				fmt.Printf("  wing_id: %s\n", cfg.WingID)
			}
			fmt.Printf("  log: %s\n", daemonctl.WingLogPath())

			// Show egg sessions from filesystem
			if cfg != nil {
				sessions := wing.ListAliveEggSessions(cfg)
				if len(sessions) > 0 {
					fmt.Println("  egg sessions:")
					for _, s := range sessions {
						fmt.Printf("    %s  %s  %s\n", s.SessionID, s.Agent, s.CWD)
					}
				} else {
					fmt.Println("  egg sessions: none")
				}
			}
			return nil
		},
	}
}

// resolveEmail calls the relay API to look up a user by email. Returns (userID, displayName, error).
func resolveEmail(cfg *config.Config, email string) (string, string, error) {
	roostURL := cfg.RoostURL
	if roostURL == "" {
		roostURL = config.DefaultRelayURL()
	}
	ts := auth.NewTokenStore(cfg.Dir)
	tok, err := ts.Load()
	if err != nil || !ts.IsValid(tok) {
		return "", "", fmt.Errorf("not logged in — run: wt login")
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(roostURL, "/")+"/api/app/resolve-email?email="+url.QueryEscape(email), nil)
	if err != nil {
		return "", "", fmt.Errorf("build email lookup request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := cmdutil.CLIHTTPClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("resolve email: %w", err)
	}
	defer cmdutil.CloseWithLog("email lookup response", resp.Body)
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("no user found with email: %s", email)
	}
	var result struct {
		UserID      string `json:"user_id"`
		DisplayName string `json:"display_name"`
	}
	if err := cmdutil.DecodeCLIAPIResponse(resp.Body, &result); err != nil {
		return "", "", fmt.Errorf("parse email lookup response: %w", err)
	}
	return result.UserID, result.DisplayName, nil
}

// fetchCurrentPasskey resolves the logged-in device-token user and fetches one
// of that user's registered WebAuthn public keys. This is used only by an
// explicit local CLI action; runtime relay envelopes are never enrollment
// authority for a locked wing.
func fetchCurrentPasskey(cfg *config.Config) (config.AllowKey, error) {
	ts := auth.NewTokenStore(cfg.Dir)
	tok, err := ts.Load()
	if err != nil || !ts.IsValid(tok) {
		return config.AllowKey{}, fmt.Errorf("not logged in — run: wt login")
	}
	relayURL := wingpolicy.ResolveRelayHTTPURL(cfg)
	info, err := auth.FetchUserInfo(relayURL, tok.Token)
	if err != nil {
		return config.AllowKey{}, fmt.Errorf("resolve current user: %w", err)
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(relayURL, "/")+"/api/app/passkey", nil)
	if err != nil {
		return config.AllowKey{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := cmdutil.CLIHTTPClient.Do(req)
	if err != nil {
		return config.AllowKey{}, fmt.Errorf("fetch passkeys: %w", err)
	}
	defer cmdutil.CloseWithLog("passkey response", resp.Body)
	if resp.StatusCode != http.StatusOK {
		return config.AllowKey{}, fmt.Errorf("fetch passkeys: HTTP %d", resp.StatusCode)
	}
	var credentials []struct {
		PublicKey string `json:"public_key"`
	}
	if err := cmdutil.DecodeCLIAPIResponse(resp.Body, &credentials); err != nil {
		return config.AllowKey{}, fmt.Errorf("parse passkeys: %w", err)
	}
	for _, credential := range credentials {
		raw, err := base64.StdEncoding.DecodeString(credential.PublicKey)
		if err == nil && auth.IsValidP256Point(raw) {
			return config.AllowKey{Key: credential.PublicKey, UserID: info.UserID, Email: info.Email}, nil
		}
	}
	return config.AllowKey{}, fmt.Errorf("no registered passkey — add one in the account page before locking this wing")
}

func wingAllowCmd() *cobra.Command {
	var userIDFlag string
	var emailFlag string
	var allFlag bool
	cmd := &cobra.Command{
		Use:   "allow [base64-public-key]",
		Short: "Allow a user or list allowlist (no args)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			// No args and no flags: list allowlist
			if len(args) == 0 && userIDFlag == "" && emailFlag == "" && !allFlag {
				if len(wingCfg.AllowKeys) == 0 {
					fmt.Println("no allowed users")
					return nil
				}
				for _, ak := range wingCfg.AllowKeys {
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					if display == "" {
						display = "(key-only)"
					}
					keyInfo := ""
					if ak.Key != "" {
						prefix := ak.Key
						if len(prefix) > 16 {
							prefix = prefix[:16] + "..."
						}
						keyInfo = "  key:" + prefix
					}
					fmt.Printf("  %s%s\n", display, keyInfo)
				}
				return nil
			}

			// --all: fetch org members from relay and add all
			if allFlag {
				orgSlug := wingCfg.Org
				if orgSlug == "" {
					return fmt.Errorf("no org configured — set org in wing.yaml or use --org on wt wing")
				}
				roostURL := cfg.RoostURL
				if roostURL == "" {
					roostURL = config.DefaultRelayURL()
				}
				ts := auth.NewTokenStore(cfg.Dir)
				tok, err := ts.Load()
				if err != nil || !ts.IsValid(tok) {
					return fmt.Errorf("not logged in — run: wt login")
				}
				base := strings.TrimRight(roostURL, "/")

				// Resolve org slug to ID via GET /api/orgs
				orgsReq, err := http.NewRequest(http.MethodGet, base+"/api/orgs", nil)
				if err != nil {
					return fmt.Errorf("build org lookup request: %w", err)
				}
				orgsReq.Header.Set("Authorization", "Bearer "+tok.Token)
				orgsResp, err := cmdutil.CLIHTTPClient.Do(orgsReq)
				if err != nil {
					return fmt.Errorf("fetch orgs: %w", err)
				}
				defer cmdutil.CloseWithLog("org lookup response", orgsResp.Body)
				if orgsResp.StatusCode != 200 {
					return fmt.Errorf("fetch orgs: HTTP %d", orgsResp.StatusCode)
				}
				var orgs []struct {
					ID   string `json:"id"`
					Slug string `json:"slug"`
				}
				if err := cmdutil.DecodeCLIAPIResponse(orgsResp.Body, &orgs); err != nil {
					return fmt.Errorf("parse orgs: %w", err)
				}
				var orgID string
				for _, o := range orgs {
					if o.Slug == orgSlug || o.ID == orgSlug {
						orgID = o.ID
						break
					}
				}
				if orgID == "" {
					return fmt.Errorf("org %q not found — check wing.yaml org setting", orgSlug)
				}

				// Fetch members via GET /api/orgs/{id}/members
				req, err := http.NewRequest(http.MethodGet, base+"/api/orgs/"+url.PathEscape(orgID)+"/members", nil)
				if err != nil {
					return fmt.Errorf("build org member request: %w", err)
				}
				req.Header.Set("Authorization", "Bearer "+tok.Token)
				resp, err := cmdutil.CLIHTTPClient.Do(req)
				if err != nil {
					return fmt.Errorf("fetch org members: %w", err)
				}
				defer cmdutil.CloseWithLog("org member response", resp.Body)
				if resp.StatusCode != 200 {
					return fmt.Errorf("fetch org members: HTTP %d", resp.StatusCode)
				}
				var membersResp struct {
					Members []struct {
						UserID        string `json:"user_id"`
						Email         string `json:"email"`
						DisplayName   string `json:"display_name"`
						PasskeyPubKey string `json:"passkey_public_key"`
					} `json:"members"`
				}
				if err := cmdutil.DecodeCLIAPIResponse(resp.Body, &membersResp); err != nil {
					return fmt.Errorf("parse org members: %w", err)
				}
				members := membersResp.Members
				added := 0
				updated := 0
				skipped := 0
				for _, m := range members {
					// Skip members without a registered passkey
					if m.PasskeyPubKey == "" {
						fmt.Printf("skipped %s (no passkey)\n", m.Email)
						skipped++
						continue
					}
					// Deduplicate by user_id
					dupIdx := -1
					for i, ak := range wingCfg.AllowKeys {
						if ak.UserID == m.UserID {
							dupIdx = i
							break
						}
					}
					if dupIdx >= 0 {
						// Update passkey public key if we have one now and didn't before
						if wingCfg.AllowKeys[dupIdx].Key != m.PasskeyPubKey {
							wingCfg.AllowKeys[dupIdx].Key = m.PasskeyPubKey
							fmt.Printf("updated key: %s\n", m.Email)
							updated++
						} else {
							fmt.Printf("already allowed: %s\n", m.Email)
						}
						continue
					}
					wingCfg.AllowKeys = append(wingCfg.AllowKeys, config.AllowKey{Key: m.PasskeyPubKey, UserID: m.UserID, Email: m.Email})
					fmt.Printf("allowed %s\n", m.Email)
					added++
				}
				if added > 0 || updated > 0 {
					if !wingCfg.Locked {
						wingCfg.Locked = true
					}
					if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
						return err
					}
					if err := daemonctl.SignalDaemon(syscall.SIGHUP); err != nil {
						return err
					}
				}
				if skipped > 0 {
					fmt.Printf("skipped %d members without passkeys\n", skipped)
				}
				fmt.Printf("added %d members, updated %d keys\n", added, updated)
				return nil
			}

			var keyB64 string
			if len(args) > 0 {
				keyB64 = args[0]
				raw, err := base64.StdEncoding.DecodeString(keyB64)
				if err != nil {
					return fmt.Errorf("invalid base64: %w", err)
				}
				if len(raw) != 64 {
					return fmt.Errorf("invalid key: expected 64 bytes (P-256 X||Y), got %d", len(raw))
				}
				if !auth.IsValidP256Point(raw) {
					return fmt.Errorf("invalid key: not a valid P-256 curve point")
				}
			}

			// Resolve email to user ID
			var resolvedEmail string
			if emailFlag != "" {
				uid, _, resolveErr := resolveEmail(cfg, emailFlag)
				if resolveErr != nil {
					return resolveErr
				}
				userIDFlag = uid
				resolvedEmail = emailFlag
			}

			if keyB64 == "" && userIDFlag == "" {
				return fmt.Errorf("must provide --email, --user-id, or a public key")
			}

			// Deduplicate by key or user_id
			for _, ak := range wingCfg.AllowKeys {
				if keyB64 != "" && ak.Key == keyB64 {
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					fmt.Printf("already allowed: %s\n", display)
					return nil
				}
				if userIDFlag != "" && ak.UserID == userIDFlag {
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					fmt.Printf("already allowed: %s\n", display)
					return nil
				}
			}

			wingCfg.AllowKeys = append(wingCfg.AllowKeys, config.AllowKey{Key: keyB64, UserID: userIDFlag, Email: resolvedEmail})
			if !wingCfg.Locked {
				wingCfg.Locked = true
			}
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			display := resolvedEmail
			if display == "" {
				display = userIDFlag
			}
			if display == "" {
				display = keyB64[:12] + "..."
			}
			fmt.Printf("allowed %s\n", display)
			return daemonctl.SignalDaemon(syscall.SIGHUP)
		},
	}
	cmd.Flags().StringVar(&userIDFlag, "user-id", "", "relay user ID to allow")
	cmd.Flags().StringVar(&emailFlag, "email", "", "user email to allow (resolves via relay)")
	cmd.Flags().BoolVar(&allFlag, "all", false, "allow all org members from relay")
	return cmd
}

func wingRevokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke [user-id-or-email]",
		Short: "Remove from allowlist",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			revokeAll, _ := cmd.Flags().GetBool("all")

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			if revokeAll {
				count := len(wingCfg.AllowKeys)
				if count == 0 {
					fmt.Println("allowlist is already empty")
					return nil
				}
				wingCfg.AllowKeys = nil
				if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
					return err
				}
				fmt.Printf("revoked all %d entries\n", count)
				return daemonctl.SignalDaemon(syscall.SIGHUP)
			}

			if len(args) == 0 {
				return fmt.Errorf("specify a user-id or email, or use --all")
			}
			query := args[0]

			// Find matches by user_id, email, or key prefix
			var matches []int
			for i, ak := range wingCfg.AllowKeys {
				if ak.UserID == query || ak.Email == query || strings.HasPrefix(ak.Key, query) {
					matches = append(matches, i)
				}
			}

			if len(matches) == 0 {
				return fmt.Errorf("no matching entry found for %q", query)
			}
			if len(matches) > 1 {
				fmt.Println("ambiguous match:")
				for _, i := range matches {
					ak := wingCfg.AllowKeys[i]
					display := ak.Email
					if display == "" {
						display = ak.UserID
					}
					if display == "" {
						display = "(key-only)"
					}
					fmt.Printf("  %s\n", display)
				}
				return fmt.Errorf("specify a more precise user_id or key prefix")
			}

			removed := wingCfg.AllowKeys[matches[0]]
			wingCfg.AllowKeys = append(wingCfg.AllowKeys[:matches[0]], wingCfg.AllowKeys[matches[0]+1:]...)
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			display := removed.Email
			if display == "" {
				display = removed.UserID
			}
			if display == "" {
				display = removed.Key[:12] + "..."
			}
			fmt.Printf("revoked: %s\n", display)
			return daemonctl.SignalDaemon(syscall.SIGHUP)
		},
	}
	cmd.Flags().Bool("all", false, "Revoke all entries from the allowlist")
	return cmd
}

func wingLockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lock",
		Short: "Enable access control",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}
			if wingCfg.Locked {
				fmt.Println("wing is already locked")
				return nil
			}
			hasPinnedKey := false
			for _, allowed := range wingCfg.AllowKeys {
				raw, err := base64.StdEncoding.DecodeString(allowed.Key)
				if err == nil && auth.IsValidP256Point(raw) {
					hasPinnedKey = true
					break
				}
			}
			if !hasPinnedKey {
				allowed, err := fetchCurrentPasskey(cfg)
				if err != nil {
					return fmt.Errorf("cannot lock wing without a locally pinned passkey: %w", err)
				}
				updated := false
				for i := range wingCfg.AllowKeys {
					if wingCfg.AllowKeys[i].UserID == allowed.UserID {
						wingCfg.AllowKeys[i] = allowed
						updated = true
						break
					}
				}
				if !updated {
					wingCfg.AllowKeys = append(wingCfg.AllowKeys, allowed)
				}
				fmt.Printf("pinned passkey for %s\n", allowed.Email)
			}
			wingCfg.Locked = true
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			if err := daemonctl.SignalDaemon(syscall.SIGHUP); err != nil {
				return err
			}
			fmt.Println("wing locked")
			return nil
		},
	}
}

func wingUnlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "Disable access control",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}
			if !wingCfg.Locked {
				fmt.Println("wing is already unlocked")
				return nil
			}
			wingCfg.Locked = false
			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}
			if err := daemonctl.SignalDaemon(syscall.SIGHUP); err != nil {
				return err
			}
			fmt.Println("wing unlocked")
			return nil
		},
	}
}

func wingConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "View or set wing configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			daemonStatus := "(daemon stopped)"
			if _, err := daemonctl.ReadPid(); err == nil {
				daemonStatus = "(daemon running)"
			}

			fmt.Printf("wing_id:    %s\n", wingCfg.WingID)
			roost := wingCfg.Roost
			if roost == "" {
				roost = config.DefaultRelayURL()
			}
			fmt.Printf("roost:      %s\n", roost)
			fmt.Printf("org:        %s\n", wingCfg.Org)
			fmt.Printf("paths:      %s\n", strings.Join(wingCfg.Paths.Strings(), ", "))
			fmt.Printf("labels:     %s\n", strings.Join(wingCfg.Labels, ", "))
			fmt.Printf("egg_config: %s\n", wingCfg.EggConfig)
			fmt.Printf("conv:       %s\n", wingCfg.Conv)
			fmt.Printf("audit:      %v\n", wingCfg.Audit)
			fmt.Printf("debug:      %v\n", wingCfg.Debug)
			fmt.Printf("locked:     %v\n", wingCfg.Locked)
			fmt.Printf("spectate:   %v\n", wingCfg.Spectate)
			fmt.Printf("hosted_relay: %s\n", wingCfg.EffectiveHostedRelay())
			if wingCfg.Conversations != "" {
				fmt.Printf("conversations: %s\n", wingCfg.Conversations)
			}
			authTTL := wingCfg.AuthTTL
			if authTTL == "" {
				authTTL = "0"
			}
			fmt.Printf("auth_ttl:   %s\n", authTTL)
			fmt.Printf("allow_keys: %d configured\n", len(wingCfg.AllowKeys))
			fmt.Println()
			fmt.Println(daemonStatus)
			return nil
		},
	}
	cmd.AddCommand(wingConfigSetCmd())
	return cmd
}

func wingConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set key=value [key=value ...]",
		Short: "Set wing configuration values",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			wingCfg, err := config.LoadWingConfig(cfg.Dir)
			if err != nil {
				return err
			}

			restartFields := map[string]bool{"org": true, "hosted_relay": true}
			immutableFields := map[string]bool{"wing_id": true, "roost": true, "allow_keys": true}

			var changedRestart []string

			for _, arg := range args {
				key, value, ok := strings.Cut(arg, "=")
				if !ok {
					return fmt.Errorf("invalid argument %q — use key=value format", arg)
				}
				key = strings.TrimSpace(key)
				value = strings.TrimSpace(value)

				if immutableFields[key] {
					return fmt.Errorf("%s cannot be changed via config set", key)
				}

				switch key {
				case "audit":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("audit: expected true or false")
					}
					wingCfg.Audit = b
				case "debug":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("debug: expected true or false")
					}
					wingCfg.Debug = b
				case "locked":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("locked: expected true or false")
					}
					wingCfg.Locked = b
				case "spectate":
					b, err := strconv.ParseBool(value)
					if err != nil {
						return fmt.Errorf("spectate: expected true or false")
					}
					wingCfg.Spectate = b
				case "hosted_relay":
					if value != config.HostedRelayAllow && value != config.HostedRelayDeny {
						return fmt.Errorf("hosted_relay: expected %q or %q", config.HostedRelayAllow, config.HostedRelayDeny)
					}
					wingCfg.HostedRelay = value
				case "conversations":
					if value != config.ConversationsEnabled && value != config.ConversationsDisabled {
						return fmt.Errorf("conversations: expected %q or %q", config.ConversationsEnabled, config.ConversationsDisabled)
					}
					wingCfg.Conversations = value
				case "labels":
					var labels []string
					for _, l := range strings.Split(value, ",") {
						l = strings.TrimSpace(l)
						if l != "" {
							labels = append(labels, l)
						}
					}
					wingCfg.Labels = labels
				case "conv":
					wingCfg.Conv = value
				case "egg_config":
					if value != "" {
						if _, err := os.Stat(value); err != nil {
							return fmt.Errorf("egg_config: %s does not exist", value)
						}
					}
					wingCfg.EggConfig = value
				case "auth_ttl":
					if _, err := time.ParseDuration(value); err != nil {
						return fmt.Errorf("auth_ttl: invalid duration %q", value)
					}
					wingCfg.AuthTTL = value
				case "paths":
					var paths config.PathList
					for _, p := range strings.Split(value, ",") {
						p = strings.TrimSpace(p)
						if p == "" {
							continue
						}
						info, err := os.Stat(p)
						if err != nil {
							return fmt.Errorf("paths: %s does not exist", p)
						}
						if !info.IsDir() {
							return fmt.Errorf("paths: %s is not a directory", p)
						}
						paths = append(paths, config.PathEntry{Path: p})
					}
					wingCfg.Paths = paths
					wingCfg.Root = "" // clear legacy
				case "root":
					// compat alias: sets paths to single entry
					if value != "" {
						info, err := os.Stat(value)
						if err != nil {
							return fmt.Errorf("root: %s does not exist", value)
						}
						if !info.IsDir() {
							return fmt.Errorf("root: %s is not a directory", value)
						}
						wingCfg.Paths = config.PathList{{Path: value}}
					} else {
						wingCfg.Paths = nil
					}
					wingCfg.Root = "" // clear legacy
				case "org":
					wingCfg.Org = value
				default:
					return fmt.Errorf("unknown config key: %s", key)
				}

				if restartFields[key] {
					changedRestart = append(changedRestart, key)
				}
			}

			if err := config.SaveWingConfig(cfg.Dir, wingCfg); err != nil {
				return err
			}

			if err := daemonctl.SignalDaemon(syscall.SIGHUP); err != nil {
				return err
			}

			for _, key := range changedRestart {
				fmt.Printf("%s: will take effect next restart\n", key)
			}
			return nil
		},
	}
}
