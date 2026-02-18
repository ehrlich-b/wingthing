package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func roostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roost",
		Short: "Run relay + wing in a single process (self-hosted mode)",
		Long:  "Starts the relay server and a local wing together. One command, one process, one log stream.\nUse 'wt roost' to daemonize, 'wt roost --foreground' for systemd/debugging.",
	}

	cmd.AddCommand(roostStartCmd())
	cmd.AddCommand(roostStopCmd())
	cmd.AddCommand(roostStatusCmd())

	return cmd
}

func roostStartCmd() *cobra.Command {
	// Relay flags
	var addrFlag string
	var devFlag bool
	// Wing flags
	var labelsFlag string
	var pathsFlag string
	var eggConfigFlag string
	var auditFlag bool
	var debugFlag bool
	var orgFlag string
	// Shared
	var foregroundFlag bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start roost (relay + wing)",
		Long:  "Start a roost — relay server and local wing in one process. Daemonizes by default. Use --foreground for debugging or systemd.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if foregroundFlag {
				return runRoostForeground(addrFlag, devFlag, labelsFlag, pathsFlag, eggConfigFlag, orgFlag, auditFlag, debugFlag)
			}

			// Daemon mode: check for existing daemon
			if pid, err := readPidFrom(roostPidPath()); err == nil {
				return fmt.Errorf("roost daemon already running (pid %d)", pid)
			}
			if pid, err := readPidFrom(wingPidPath()); err == nil {
				return fmt.Errorf("wing daemon already running (pid %d) — stop it first with: wt stop", pid)
			}

			exe, err := os.Executable()
			if err != nil {
				return err
			}

			// Build child args
			var childArgs []string
			childArgs = append(childArgs, "roost", "start", "--foreground")
			if addrFlag != ":8080" {
				childArgs = append(childArgs, "--addr", addrFlag)
			}
			if devFlag {
				childArgs = append(childArgs, "--dev")
			}
			if labelsFlag != "" {
				childArgs = append(childArgs, "--labels", labelsFlag)
			}
			if pathsFlag != "" {
				childArgs = append(childArgs, "--paths", pathsFlag)
			}
			if eggConfigFlag != "" {
				childArgs = append(childArgs, "--egg-config", eggConfigFlag)
			}
			if orgFlag != "" {
				childArgs = append(childArgs, "--org", orgFlag)
			}
			if auditFlag {
				childArgs = append(childArgs, "--audit")
			}
			if debugFlag {
				childArgs = append(childArgs, "--debug")
			}

			rotateLog(roostLogPath())
			logFile, err := os.OpenFile(roostLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				return fmt.Errorf("open log: %w", err)
			}

			home, _ := os.UserHomeDir()

			child := exec.Command(exe, childArgs...)
			child.Dir = home
			child.Stdout = logFile
			child.Stderr = logFile
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

			if err := child.Start(); err != nil {
				logFile.Close()
				return fmt.Errorf("start daemon: %w", err)
			}
			logFile.Close()

			if err := os.WriteFile(roostPidPath(), []byte(strconv.Itoa(child.Process.Pid)), 0644); err != nil {
				log.Printf("warning: failed to write PID file: %v", err)
			}
			if err := os.WriteFile(roostArgsPath(), []byte(strings.Join(childArgs, "\n")), 0644); err != nil {
				log.Printf("warning: failed to write args file: %v", err)
			}
			fmt.Printf("roost daemon started (pid %d)\n", child.Process.Pid)
			fmt.Printf("  log: %s\n", roostLogPath())
			fmt.Println()
			fmt.Println("open http://localhost:8080 to start a terminal")
			return nil
		},
	}

	// Relay flags
	cmd.Flags().StringVar(&addrFlag, "addr", ":8080", "listen address")
	cmd.Flags().BoolVar(&devFlag, "dev", false, "reload templates from disk on each request")
	// Wing flags
	cmd.Flags().StringVar(&labelsFlag, "labels", "", "comma-separated wing labels")
	cmd.Flags().StringVar(&pathsFlag, "paths", "", "comma-separated directories the wing can browse")
	cmd.Flags().StringVar(&eggConfigFlag, "egg-config", "", "path to egg.yaml for sandbox defaults")
	cmd.Flags().StringVar(&orgFlag, "org", "", "org name or ID")
	cmd.Flags().BoolVar(&auditFlag, "audit", false, "enable audit logging for all egg sessions")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output for each egg")
	// Shared
	cmd.Flags().BoolVar(&foregroundFlag, "foreground", false, "run in foreground instead of daemonizing")

	return cmd
}

func runRoostForeground(addrFlag string, devFlag bool, labelsFlag, pathsFlag, eggConfigFlag, orgFlag string, auditFlag, debugFlag bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// --- Relay setup (local mode forced) ---

	store, err := relay.OpenRelay(cfg.RelayDBPath())
	if err != nil {
		return fmt.Errorf("open relay db: %w", err)
	}
	defer store.Close()

	if err := store.BackfillProUsers(); err != nil {
		return fmt.Errorf("backfill pro users: %w", err)
	}

	srvCfg := relay.ServerConfig{
		BaseURL:            envOr("WT_BASE_URL", "http://localhost:8080"),
		AppHost:            os.Getenv("WT_APP_HOST"),
		WSHost:             os.Getenv("WT_WS_HOST"),
		JWTSecret:          os.Getenv("WT_JWT_SECRET"),
		GitHubClientID:     os.Getenv("GITHUB_CLIENT_ID"),
		GitHubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		SMTPHost:           os.Getenv("SMTP_HOST"),
		SMTPPort:           envOr("SMTP_PORT", "587"),
		SMTPUser:           os.Getenv("SMTP_USER"),
		SMTPPass:           os.Getenv("SMTP_PASS"),
		SMTPFrom:           os.Getenv("SMTP_FROM"),
		HeroVideo:          os.Getenv("WT_HERO_VIDEO"),
	}

	srv := relay.NewServer(store, srvCfg)
	srv.RateLimit = relay.NewRateLimiter(5, 20)

	// Local mode: direct DB access for bandwidth
	srv.Bandwidth = relay.NewBandwidthMeter(relay.SustainedRate, 1*1024*1024, store.DB())
	srv.Bandwidth.SetTierLookup(func(userID string) string {
		if store.IsUserPro(userID) {
			return "pro"
		}
		return "free"
	})

	if devFlag {
		srv.DevTemplateDir = "internal/relay/templates"
		srv.DevMode = true
		fmt.Println("dev mode: templates reload, auto-claim login")
	}

	// Auth mode detection: same pattern as serve.go
	hasAuth := srvCfg.GoogleClientID != "" || srvCfg.GitHubClientID != "" || srvCfg.SMTPHost != ""

	var wingToken string
	if !hasAuth {
		// No auth providers — single user, no login (existing behavior)
		user, token, err := store.CreateLocalUser()
		if err != nil {
			return fmt.Errorf("setup local user: %w", err)
		}
		srv.LocalMode = true
		srv.SetLocalUser(user)
		wingToken = token

		// Grant pro tier — self-hosted has no bandwidth cap
		if !store.IsUserPro(user.ID) {
			subID := uuid.New().String()
			store.CreateSubscription(&relay.Subscription{ID: subID, UserID: &user.ID, Plan: "local", Status: "active", Seats: 1})
			store.CreateEntitlement(&relay.Entitlement{ID: uuid.New().String(), UserID: user.ID, SubscriptionID: subID})
			store.UpdateUserTier(user.ID, "pro")
		}
		fmt.Println("no auth providers configured — local mode")
	} else {
		// OAuth configured — real auth, roost wing visible to all logged-in users
		srv.RoostMode = true
		user, token, err := store.CreateServiceUser()
		if err != nil {
			return fmt.Errorf("setup service user: %w", err)
		}
		wingToken = token

		// Grant pro to service user
		if !store.IsUserPro(user.ID) {
			subID := uuid.New().String()
			store.CreateSubscription(&relay.Subscription{ID: subID, UserID: &user.ID, Plan: "roost", Status: "active", Seats: 1})
			store.CreateEntitlement(&relay.Entitlement{ID: uuid.New().String(), UserID: user.ID, SubscriptionID: subID})
			store.UpdateUserTier(user.ID, "pro")
		}
		fmt.Println("auth providers configured — roost mode (OAuth enabled)")
	}

	// Write device token so the wing goroutine can connect
	ts := auth.NewTokenStore(cfg.Dir)
	ts.Save(&auth.DeviceToken{
		Token:    wingToken,
		DeviceID: "local",
	})

	httpSrv := &http.Server{
		Addr:    addrFlag,
		Handler: srv,
	}

	// --- Signal handling: single owner ---

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)

	// Start bandwidth sync
	srv.Bandwidth.SeedFromDB()
	srv.Bandwidth.StartSync(ctx, 10*time.Minute)

	// --- Start relay ---

	relayErrCh := make(chan error, 1)
	go func() {
		fmt.Printf("wt roost listening on %s\n", addrFlag)
		fmt.Println()
		fmt.Println("open http://localhost:8080 to start a terminal")
		relayErrCh <- httpSrv.ListenAndServe()
	}()

	// --- Start wing (local=true, roost URL = localhost) ---

	wingErrCh := make(chan error, 1)
	go func() {
		wingErrCh <- runWingWithContext(ctx, sighupCh, "", labelsFlag, "auto", eggConfigFlag, orgFlag, nil, pathsFlag, debugFlag, auditFlag, true, false)
	}()

	// --- Wait for shutdown ---

	select {
	case <-ctx.Done():
		log.Println("roost shutting down...")
		return srv.GracefulShutdown(httpSrv, 8*time.Second)
	case err := <-relayErrCh:
		return fmt.Errorf("relay: %w", err)
	case err := <-wingErrCh:
		return fmt.Errorf("wing: %w", err)
	}
}

func roostStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the roost daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := readPidFrom(roostPidPath())
			if err != nil {
				return fmt.Errorf("no roost daemon running")
			}
			proc, _ := os.FindProcess(pid)
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("kill pid %d: %w", pid, err)
			}
			os.Remove(roostPidPath())
			os.Remove(roostArgsPath())
			fmt.Printf("roost daemon stopped (pid %d)\n", pid)
			return nil
		},
	}
}

func roostStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check roost daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := readPidFrom(roostPidPath())
			if err != nil {
				fmt.Println("roost daemon is not running")
				return nil
			}
			fmt.Printf("roost daemon is running (pid %d)\n", pid)
			fmt.Printf("  log: %s\n", roostLogPath())

			cfg, _ := config.Load()
			if cfg != nil {
				sessions := listAliveEggSessions(cfg)
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
