package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/spf13/cobra"
)

const (
	roostReadyFDEnv          = "WT_ROOST_READY_FD"
	roostReadyToken          = "ready\n"
	roostDaemonReadyTimeout  = 10 * time.Second
	roostWingReadyTimeout    = 8 * time.Second
	maxRoostReadyMessageSize = 32
)

func roostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roost",
		Short: "Run relay + wing in a single process (self-hosted mode)",
		Long:  "Starts the relay server and a local wing together. One command, one process, one log stream.\nUse 'wt roost start' to daemonize, or 'wt roost start --foreground' for systemd/debugging.",
	}

	cmd.AddCommand(roostStartCmd())
	cmd.AddCommand(roostStopCmd())
	cmd.AddCommand(roostStatusCmd())

	return cmd
}

func roostAllowedEmailsFromEnv() ([]string, error) {
	raw := strings.TrimSpace(os.Getenv("WT_ROOST_ALLOWED_EMAILS"))
	if raw == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var emails []string
	for _, value := range strings.Split(raw, ",") {
		email := strings.ToLower(strings.TrimSpace(value))
		at := strings.IndexByte(email, '@')
		if email == "" || strings.Count(email, "@") != 1 || at <= 0 || at == len(email)-1 || strings.ContainsAny(email, " \t\r\n") {
			return nil, fmt.Errorf("WT_ROOST_ALLOWED_EMAILS contains invalid email %q", value)
		}
		if !seen[email] {
			seen[email] = true
			emails = append(emails, email)
		}
	}
	return emails, nil
}

func roostStartCmd() *cobra.Command {
	// Relay flags
	var addrFlag string
	var devFlag bool
	var httpsFlag bool
	var httpsAddrFlag string
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
			if err := validateAuthProviderEnvironment(); err != nil {
				return err
			}
			var lifecycleLock *os.File
			if !foregroundFlag {
				var err error
				lifecycleLock, err = acquireDaemonLifecycleLock()
				if err != nil {
					return err
				}
				defer closeWithLog("daemon lifecycle lock", lifecycleLock)
			}
			localMode := !authProvidersConfigured()
			if err := validateLocalHTTPSMode(httpsFlag, localMode, false); err != nil {
				return err
			}
			if localMode && !httpsFlag {
				var err error
				addrFlag, err = prepareLocalHTTPAddress(addrFlag, cmd.Flags().Changed("addr"))
				if err != nil {
					return err
				}
			}
			if !foregroundFlag {
				// Check before the trust ceremony so a failed duplicate start has
				// no certificate or trust-store side effects.
				if pid, kind, err := readDaemon(); err == nil {
					if kind == roostDaemon {
						return fmt.Errorf("roost daemon already running (pid %d)", pid)
					}
					return fmt.Errorf("wing daemon already running (pid %d) — stop it first with: wt stop", pid)
				} else if !errors.Is(err, errNoDaemonRunning) {
					return fmt.Errorf("inspect daemon state: %w", err)
				}
			}
			var localHTTPS *localHTTPSConfig
			if httpsFlag {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				localHTTPS, err = prepareLocalHTTPS(cmd.Context(), cfg.Dir, addrFlag, httpsAddrFlag, cmd.Flags().Changed("addr"))
				if err != nil {
					return err
				}
				addrFlag = localHTTPS.HTTPAddr
			}
			if foregroundFlag {
				return runRoostForeground(addrFlag, devFlag, labelsFlag, pathsFlag, eggConfigFlag, orgFlag, auditFlag, debugFlag, localHTTPS)
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
			if httpsFlag {
				childArgs = append(childArgs, "--https", "--https-addr", httpsAddrFlag)
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

			if err := rotateLog(roostLogPath()); err != nil {
				return err
			}
			logFile, err := os.OpenFile(roostLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				return fmt.Errorf("open log: %w", err)
			}

			home, err := os.UserHomeDir()
			if err != nil {
				closeWithLog("roost log", logFile)
				return fmt.Errorf("resolve user home: %w", err)
			}

			child := exec.Command(exe, childArgs...)
			child.Dir = home
			child.Stdout = logFile
			child.Stderr = logFile
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			readyReader, readyWriter, err := os.Pipe()
			if err != nil {
				closeWithLog("roost log", logFile)
				return fmt.Errorf("create daemon readiness pipe: %w", err)
			}
			child.ExtraFiles = []*os.File{readyWriter}
			child.Env = replaceEnvironmentValue(os.Environ(), roostReadyFDEnv, "3")

			if err := child.Start(); err != nil {
				closeWithLog("roost readiness reader", readyReader)
				closeWithLog("roost readiness writer", readyWriter)
				closeWithLog("roost log", logFile)
				return fmt.Errorf("start daemon: %w", err)
			}
			if err := readyWriter.Close(); err != nil {
				abandonStartedDaemon(child)
				closeWithLog("roost readiness reader", readyReader)
				closeWithLog("roost log", logFile)
				return fmt.Errorf("close parent readiness writer: %w", err)
			}
			if err := logFile.Close(); err != nil {
				abandonStartedDaemon(child)
				closeWithLog("roost readiness reader", readyReader)
				return fmt.Errorf("close roost log: %w", err)
			}
			if err := awaitRoostReady(readyReader, roostDaemonReadyTimeout); err != nil {
				abandonStartedDaemon(child)
				return fmt.Errorf("roost daemon did not become ready: %w (see %s)", err, roostLogPath())
			}
			if err := writeDaemonMetadata(roostPidPath(), roostArgsPath(), child.Process.Pid, childArgs); err != nil {
				abandonStartedDaemon(child)
				return fmt.Errorf("start roost daemon: %w", err)
			}
			if err := child.Process.Release(); err != nil {
				log.Printf("warning: failed to release daemon process handle: %v", err)
			}
			fmt.Printf("roost daemon started (pid %d)\n", child.Process.Pid)
			fmt.Printf("  log: %s\n", roostLogPath())
			fmt.Println()
			if localHTTPS != nil {
				fmt.Printf("open %s to start a terminal\n", localHTTPS.URL)
			} else {
				fmt.Printf("open %s to start a terminal\n", localHTTPURL(addrFlag))
			}
			return nil
		},
	}

	// Relay flags
	cmd.Flags().StringVar(&addrFlag, "addr", ":8080", "listen address")
	cmd.Flags().BoolVar(&devFlag, "dev", false, "reload templates from disk on each request")
	cmd.Flags().BoolVar(&httpsFlag, "https", false, "serve the local browser UI over HTTPS using an on-demand, device-local CA")
	cmd.Flags().StringVar(&httpsAddrFlag, "https-addr", defaultLocalHTTPSAddr, "loopback HTTPS address for the local browser UI")
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

func runRoostForeground(addrFlag string, devFlag bool, labelsFlag, pathsFlag, eggConfigFlag, orgFlag string, auditFlag, debugFlag bool, localHTTPS *localHTTPSConfig) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// --- Relay setup (local mode forced) ---

	relayDBPath, err := cfg.RelayDBPath()
	if err != nil {
		return err
	}
	store, err := relay.OpenRelay(relayDBPath)
	if err != nil {
		return fmt.Errorf("open relay db: %w", err)
	}
	defer closeWithLog("relay store", store)

	if err := store.BackfillProUsers(); err != nil {
		return fmt.Errorf("backfill pro users: %w", err)
	}

	// JWT key: an explicit key wins; existing WT_JWT_SECRET deployments derive a stable
	// P-256 key; otherwise local mode loads/generates the key in wing.yaml.
	jwtKey, keyErr := jwtKeyFromEnvironment()
	if keyErr != nil {
		return fmt.Errorf("jwt key: %w", keyErr)
	}
	if jwtKey == "" {
		jwtKey, keyErr = ensureJWTKeyInWingYaml(cfg.Dir)
		if keyErr != nil {
			return fmt.Errorf("jwt key: %w", keyErr)
		}
	} else if os.Getenv("WT_JWT_KEY") == "" {
		log.Printf("using stable P-256 JWT signing key derived from WT_JWT_SECRET")
	}

	roostAllowedEmails, err := roostAllowedEmailsFromEnv()
	if err != nil {
		return err
	}
	srvCfg := relay.ServerConfig{
		BaseURL:            defaultBaseURL(localHTTPS),
		AppHost:            os.Getenv("WT_APP_HOST"),
		WSHost:             os.Getenv("WT_WS_HOST"),
		JWTKey:             jwtKey,
		InternalSecret:     os.Getenv("WT_INTERNAL_SECRET"),
		GitHubClientID:     strings.TrimSpace(os.Getenv("GITHUB_CLIENT_ID")),
		GitHubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		GoogleClientID:     strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID")),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		SMTPHost:           strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:           envOr("SMTP_PORT", "587"),
		SMTPUser:           os.Getenv("SMTP_USER"),
		SMTPPass:           os.Getenv("SMTP_PASS"),
		SMTPFrom:           os.Getenv("SMTP_FROM"),
		HeroVideo:          os.Getenv("WT_HERO_VIDEO"),
		RoostAllowedEmails: roostAllowedEmails,
	}

	srv := relay.NewServer(store, srvCfg)
	if err := srv.InitJWTKey(); err != nil {
		return fmt.Errorf("init jwt key: %w", err)
	}
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
		if _, err := os.Stat("internal/relay/templates"); err == nil {
			srv.DevTemplateDir = "internal/relay/templates"
			fmt.Println("dev mode: templates reload from source tree")
		}
		srv.DevMode = true
		fmt.Println("dev mode: auto-claim login")
	}

	// Auth mode detection: same pattern as serve.go
	hasAuth := authProvidersConfigured()

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
		if err := ensureSelfHostedPro(store, user.ID, "local"); err != nil {
			return err
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
		if err := ensureSelfHostedPro(store, user.ID, "roost"); err != nil {
			return err
		}
		fmt.Println("auth providers configured — roost mode (OAuth enabled)")
	}

	// Authenticated roost users always receive the typed owner-scoped control
	// surface. wing.yaml can add role-scoped executable tools beside it.
	tools, policy, err := loadRoostMCPConfig(cfg.Dir)
	if err != nil {
		return err
	}
	nativeTools := roostMCPControlTools(srv, cfg, hasAuth)
	if hasAuth || policy != nil {
		srv.EnableMCP(egg.NewToolRunner(tools), policy, nativeTools...)
		roleCount := 0
		if policy != nil {
			roleCount = len(policy.Roles)
		}
		log.Printf("mcp: enabled — %d control operation(s), %d executable tool(s), %d role(s) at POST /mcp", len(nativeTools), len(tools), roleCount)
	}

	// Keep the appliance service credential process-local. Persisting it in the
	// ordinary token store would replace an operator's unrelated hosted login.
	embeddedWingToken := &auth.DeviceToken{
		Token:    wingToken,
		DeviceID: "local",
	}

	listeners := newRelayListeners(srv, addrFlag, localHTTPS)

	// --- Signal handling: single owner ---

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	defer signal.Stop(sighupCh)
	if srv.MCPEnabled() {
		mcpSIGHUPCh := make(chan os.Signal, 1)
		signal.Notify(mcpSIGHUPCh, syscall.SIGHUP)
		defer signal.Stop(mcpSIGHUPCh)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-mcpSIGHUPCh:
					newTools, newPolicy, reloadErr := loadRoostMCPConfig(cfg.Dir)
					if reloadErr != nil {
						log.Printf("mcp: reload failed; keeping previous configuration: %v", reloadErr)
						continue
					}
					srv.ReloadMCP(egg.NewToolRunner(newTools), newPolicy)
					roleCount := 0
					if newPolicy != nil {
						roleCount = len(newPolicy.Roles)
					}
					log.Printf("mcp: reloaded %d executable tool(s), %d role(s)", len(newTools), roleCount)
				}
			}
		}()
	}

	// Start bandwidth sync
	srv.Bandwidth.SeedFromDB()
	srv.Bandwidth.StartSync(ctx, 10*time.Minute)

	// --- Start relay ---

	if err := listeners.Start(localHTTPS); err != nil {
		return err
	}
	if localHTTPS != nil {
		fmt.Printf("wt roost wing endpoint (loopback HTTP): %s\n", localHTTPURL(addrFlag))
		fmt.Println()
		fmt.Printf("open %s to start a terminal\n", localHTTPS.URL)
	} else {
		fmt.Printf("wt roost listening on %s\n", addrFlag)
		fmt.Println()
		fmt.Printf("open %s to start a terminal\n", localHTTPURL(addrFlag))
	}

	// --- Start wing (local=true, roost URL = localhost) ---

	// A status file from an earlier standalone wing or roost must not satisfy
	// this process's readiness check. The new embedded wing will recreate it as
	// it moves through connecting to connected.
	_ = os.Remove(wingStatusPath())
	wingErrCh := make(chan error, 1)
	go func() {
		wingErrCh <- runWingWithContext(ctx, sighupCh, localHTTPURL(addrFlag), labelsFlag, "auto", eggConfigFlag, orgFlag, nil, pathsFlag, debugFlag, auditFlag, true, false, hasAuth, embeddedWingToken)
	}()
	if err := awaitEmbeddedWingReady(ctx, wingErrCh, listeners.errCh, readWingStatus, roostWingReadyTimeout); err != nil {
		_ = listeners.Shutdown(srv, 8*time.Second)
		return fmt.Errorf("embedded wing did not become ready: %w", err)
	}
	if err := signalRoostReady(); err != nil {
		_ = listeners.Shutdown(srv, 8*time.Second)
		return err
	}

	// --- Wait for shutdown ---

	select {
	case <-ctx.Done():
		log.Println("roost shutting down...")
		return listeners.Shutdown(srv, 8*time.Second)
	case result := <-listeners.errCh:
		err := listenerResult(result)
		if err != nil {
			_ = listeners.Shutdown(srv, 8*time.Second)
		}
		return err
	case err := <-wingErrCh:
		shutdownErr := listeners.Shutdown(srv, 8*time.Second)
		return roostWingExitResult(ctx, err, shutdownErr)
	}
}

func roostWingExitResult(ctx context.Context, wingErr, shutdownErr error) error {
	if wingErr != nil {
		return errors.Join(fmt.Errorf("wing: %w", wingErr), shutdownErr)
	}
	if ctx.Err() != nil {
		return shutdownErr
	}
	return errors.Join(errors.New("wing exited unexpectedly"), shutdownErr)
}

func replaceEnvironmentValue(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func awaitRoostReady(reader io.ReadCloser, timeout time.Duration) (resultErr error) {
	result := make(chan error, 1)
	go func() {
		payload, err := io.ReadAll(io.LimitReader(reader, maxRoostReadyMessageSize))
		if err != nil {
			result <- err
			return
		}
		if string(payload) != roostReadyToken {
			result <- fmt.Errorf("readiness pipe closed with %q", payload)
			return
		}
		result <- nil
	}()
	defer func() {
		if err := reader.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close readiness pipe: %w", err))
		}
	}()
	select {
	case err := <-result:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("timed out after %s", timeout)
	}
}

func signalRoostReady() (resultErr error) {
	rawFD := strings.TrimSpace(os.Getenv(roostReadyFDEnv))
	if rawFD == "" {
		return nil
	}
	fd, err := strconv.Atoi(rawFD)
	if err != nil || fd < 3 {
		return fmt.Errorf("invalid %s value %q", roostReadyFDEnv, rawFD)
	}
	readyWriter := os.NewFile(uintptr(fd), "roost-ready")
	if readyWriter == nil {
		return fmt.Errorf("open roost readiness descriptor %d", fd)
	}
	defer func() {
		if err := readyWriter.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close roost readiness descriptor: %w", err))
		}
	}()
	if _, err := io.WriteString(readyWriter, roostReadyToken); err != nil {
		return fmt.Errorf("signal roost readiness: %w", err)
	}
	return nil
}

func awaitEmbeddedWingReady(ctx context.Context, wingErrors <-chan error, relayErrors <-chan namedServerError, readStatus func() (*wingStatus, error), timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-wingErrors:
			if err == nil {
				return errors.New("wing exited before connecting")
			}
			return err
		case result := <-relayErrors:
			if err := listenerResult(result); err != nil {
				return err
			}
			return errors.New("relay listener closed before the wing connected")
		case <-timer.C:
			return fmt.Errorf("timed out after %s", timeout)
		case <-ticker.C:
			status, err := readStatus()
			if err != nil {
				continue
			}
			switch status.State {
			case "connected":
				return nil
			case "auth_failed":
				if status.Error != "" {
					return fmt.Errorf("authentication failed: %s", status.Error)
				}
				return errors.New("authentication failed")
			}
		}
	}
}

func roostMCPControlTools(srv *relay.Server, cfg *config.Config, sharedHost bool) []mcppkg.NativeTool {
	tools := roostNativeMCPTools(cfg, sharedHost)
	return append(tools, srv.PortalNativeMCPTools(cfg.WingID)...)
}

func loadRoostMCPConfig(configDir string) ([]*config.ToolConfig, *config.MCPConfig, error) {
	wingCfg, err := config.LoadWingConfig(configDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load wing config for mcp: %w", err)
	}
	if wingCfg.MCP == nil || !wingCfg.MCP.Enabled {
		return nil, nil, nil
	}
	toolsDir := config.ResolveToolsDir(configDir, wingCfg.ToolsDir)
	tools, err := config.LoadToolsDir(toolsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load mcp tools from %s: %w", toolsDir, err)
	}
	toolNames := make(map[string]bool, len(tools))
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}
	for roleName, role := range wingCfg.MCP.Roles {
		for _, toolName := range append(append([]string{}, role.Allow...), role.Deny...) {
			if !toolNames[toolName] {
				return nil, nil, fmt.Errorf("mcp role %q references unknown tool %q", roleName, toolName)
			}
		}
	}
	return tools, wingCfg.MCP, nil
}

func roostStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the roost daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			lifecycleLock, lockErr := acquireDaemonLifecycleLock()
			if lockErr != nil {
				return lockErr
			}
			defer closeWithLog("daemon lifecycle lock", lifecycleLock)
			pid, err := readPidFrom(roostPidPath(), roostDaemon)
			if err != nil {
				return fmt.Errorf("no roost daemon running")
			}
			if err := stopDaemonAndWait(pid, roostDaemon, 5*time.Second); err != nil {
				return err
			}
			if err := removeFiles(roostPidPath(), roostArgsPath()); err != nil {
				return fmt.Errorf("remove roost daemon metadata: %w", err)
			}
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
			pid, err := readPidFrom(roostPidPath(), roostDaemon)
			if err != nil {
				if daemonAbsentError(err) {
					fmt.Println("roost daemon is not running")
					return nil
				}
				return fmt.Errorf("inspect roost daemon state: %w", err)
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
