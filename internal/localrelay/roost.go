package localrelay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/contextclient"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	mcppkg "github.com/ehrlich-b/wingthing/internal/mcp"
	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/ehrlich-b/wingthing/internal/wing"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

const (
	RoostReadyFDEnv          = "WT_ROOST_READY_FD"
	roostReadyToken          = "ready\n"
	RoostDaemonReadyTimeout  = 10 * time.Second
	roostWingReadyTimeout    = 8 * time.Second
	maxRoostReadyMessageSize = 32
)

func RoostAllowedEmailsFromEnv() ([]string, error) {
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

func RunRoostForeground(version string, addrFlag string, devFlag bool, labelsFlag, pathsFlag, eggConfigFlag, orgFlag string, auditFlag, debugFlag bool, localHTTPS *LocalHTTPSConfig) error {
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
	defer cmdutil.CloseWithLog("relay store", store)

	if err := store.BackfillProUsers(); err != nil {
		return fmt.Errorf("backfill pro users: %w", err)
	}

	// JWT key: an explicit key wins; existing WT_JWT_SECRET deployments derive a stable
	// P-256 key; otherwise local mode loads/generates the key in wing.yaml.
	jwtKey, keyErr := JwtKeyFromEnvironment()
	if keyErr != nil {
		return fmt.Errorf("jwt key: %w", keyErr)
	}
	if jwtKey == "" {
		jwtKey, keyErr = EnsureJWTKeyInWingYaml(cfg.Dir)
		if keyErr != nil {
			return fmt.Errorf("jwt key: %w", keyErr)
		}
	} else if os.Getenv("WT_JWT_KEY") == "" {
		log.Printf("using stable P-256 JWT signing key derived from WT_JWT_SECRET")
	}

	roostAllowedEmails, err := RoostAllowedEmailsFromEnv()
	if err != nil {
		return err
	}
	srvCfg := relay.ServerConfig{
		BaseURL:            DefaultBaseURL(localHTTPS),
		AppHost:            os.Getenv("WT_APP_HOST"),
		WSHost:             os.Getenv("WT_WS_HOST"),
		JWTKey:             jwtKey,
		InternalSecret:     os.Getenv("WT_INTERNAL_SECRET"),
		GitHubClientID:     strings.TrimSpace(os.Getenv("GITHUB_CLIENT_ID")),
		GitHubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		GoogleClientID:     strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID")),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		SMTPHost:           strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:           EnvOr("SMTP_PORT", "587"),
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
	hasAuth := AuthProvidersConfigured()

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
		if err := EnsureSelfHostedPro(store, user.ID, "local"); err != nil {
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
		if err := EnsureSelfHostedPro(store, user.ID, "roost"); err != nil {
			return err
		}
		fmt.Println("auth providers configured — roost mode (OAuth enabled)")
	}

	// Authenticated roost users always receive the typed owner-scoped control
	// surface. wing.yaml can add role-scoped executable tools beside it.
	wingCfg, err := config.LoadWingConfig(cfg.Dir)
	if err != nil {
		return err
	}
	contextCfg, releaseContext := config.FreezeContextConfig(cfg.Dir, wingCfg.Context)
	defer releaseContext()
	tools, policy, err := loadRoostMCPConfig(cfg.Dir, contextCfg)
	if err != nil {
		return err
	}
	var runtimePolicy atomic.Pointer[func() (*config.WingConfig, *egg.EggConfig)]
	var runtimeSessions atomic.Pointer[wingsession.Service]
	nativeTools := localmcp.RoostNativeMCPToolsWithSessions(version, cfg, hasAuth, runtimeSessions.Load, func() (*config.WingConfig, *egg.EggConfig) {
		if source := runtimePolicy.Load(); source != nil {
			return (*source)()
		}
		return nil, nil
	})
	nativeTools = append(nativeTools, srv.PortalNativeMCPTools(cfg.WingID)...)
	if hasAuth || policy != nil {
		runner, err := roostToolRunner(cfg.Dir, tools)
		if err != nil {
			return err
		}
		srv.EnableMCP(runner, policy, nativeTools...)
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

	listeners := NewRelayListeners(srv, addrFlag, localHTTPS)

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
					newTools, newPolicy, reloadErr := loadRoostMCPConfig(cfg.Dir, contextCfg)
					if reloadErr != nil {
						log.Printf("mcp: reload failed; keeping previous configuration: %v", reloadErr)
						continue
					}
					runner, err := roostToolRunner(cfg.Dir, newTools)
					if err != nil {
						log.Printf("mcp: context reload failed: %v", err)
						continue
					}
					srv.ReloadMCP(runner, newPolicy)
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
		fmt.Printf("wt roost wing endpoint (loopback HTTP): %s\n", LocalHTTPURL(addrFlag))
		fmt.Println()
		fmt.Printf("open %s to start a terminal\n", localHTTPS.URL)
	} else {
		fmt.Printf("wt roost listening on %s\n", addrFlag)
		fmt.Println()
		fmt.Printf("open %s to start a terminal\n", LocalHTTPURL(addrFlag))
	}

	// --- Start wing (local=true, roost URL = localhost) ---

	// A status file from an earlier standalone wing or roost must not satisfy
	// this process's readiness check. The new embedded wing will recreate it as
	// it moves through connecting to connected.
	_ = os.Remove(daemonctl.WingStatusPath())
	wingErrCh := make(chan error, 1)
	go func() {
		wingErrCh <- wing.RunWingWithContext(wing.EntryOptions{Version: version, SetSessionService: runtimeSessions.Store, SetPolicySource: func(source func() (*config.WingConfig, *egg.EggConfig)) { runtimePolicy.Store(&source) }}, ctx, sighupCh, LocalHTTPURL(addrFlag), labelsFlag, "auto", eggConfigFlag, orgFlag, nil, pathsFlag, debugFlag, auditFlag, true, false, hasAuth, embeddedWingToken)
	}()
	if err := awaitEmbeddedWingReady(ctx, wingErrCh, listeners.ErrCh, daemonctl.ReadWingStatus, roostWingReadyTimeout); err != nil {
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
	case result := <-listeners.ErrCh:
		err := ListenerResult(result)
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

func ReplaceEnvironmentValue(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func AwaitRoostReady(reader io.ReadCloser, timeout time.Duration) (resultErr error) {
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
	rawFD := strings.TrimSpace(os.Getenv(RoostReadyFDEnv))
	if rawFD == "" {
		return nil
	}
	fd, err := strconv.Atoi(rawFD)
	if err != nil || fd < 3 {
		return fmt.Errorf("invalid %s value %q", RoostReadyFDEnv, rawFD)
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

func awaitEmbeddedWingReady(ctx context.Context, wingErrors <-chan error, relayErrors <-chan namedServerError, readStatus func() (*daemonctl.WingStatus, error), timeout time.Duration) error {
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
			if err := ListenerResult(result); err != nil {
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

func roostMCPControlTools(version string, srv *relay.Server, cfg *config.Config, sharedHost bool, sources ...func() (*config.WingConfig, *egg.EggConfig)) []mcppkg.NativeTool {
	tools := localmcp.RoostNativeMCPTools(version, cfg, sharedHost, sources...)
	return append(tools, srv.PortalNativeMCPTools(cfg.WingID)...)
}

func loadRoostMCPConfig(configDir string, contexts ...*config.ContextConfig) ([]*config.ToolConfig, *config.MCPConfig, error) {
	wingCfg, err := config.LoadWingConfig(configDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load wing config for mcp: %w", err)
	}
	if len(contexts) > 0 {
		config.RetainContextConfig(wingCfg, contexts[0])
	}
	if wingCfg.MCP == nil || !wingCfg.MCP.Enabled {
		return nil, nil, nil
	}
	toolsDir := config.ResolveToolsDir(configDir, wingCfg.ToolsDir)
	tools, err := config.LoadWingTools(toolsDir, wingCfg.Context)
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

func roostToolRunner(dir string, tools []*config.ToolConfig) (*egg.ToolRunner, error) {
	contextCfg, err := config.LoadContextConfig(dir)
	if err != nil {
		return nil, err
	}
	client, err := contextclient.New(contextCfg)
	if err != nil {
		return nil, err
	}
	return egg.NewToolRunner(tools, client), nil
}
