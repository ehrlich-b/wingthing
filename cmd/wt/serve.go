package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func serveCmd() *cobra.Command {
	var addrFlag string
	var devFlag bool
	var localFlag bool

	cmd := &cobra.Command{
		Use:     "relay",
		Aliases: []string{"serve"},
		Short:   "Start the relay server (web UI + WebSocket relay)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			nodeRole := os.Getenv("WT_NODE_ROLE")
			loginAddr := os.Getenv("WT_LOGIN_ADDR")
			flyMachineID := os.Getenv("FLY_MACHINE_ID")
			flyRegion := os.Getenv("FLY_REGION")
			flyApp := os.Getenv("FLY_APP_NAME")

			// Auto-detect node role on Fly: volume mounted at /data → login, else edge.
			if flyMachineID != "" && nodeRole == "" {
				if info, err := os.Stat("/data"); err == nil && info.IsDir() {
					nodeRole = "login"
				} else {
					nodeRole = "edge"
				}
				fmt.Printf("auto-detected node role: %s\n", nodeRole)
			}

			// Auto-derive login node address from Fly internal DNS.
			if nodeRole == "edge" && loginAddr == "" && flyApp != "" {
				loginAddr = "http://login.process." + flyApp + ".internal:8080"
				fmt.Printf("auto-derived login addr: %s\n", loginAddr)
			}

			isEdge := nodeRole == "edge"

			// Edge nodes skip SQLite and DB-dependent init
			var store *relay.RelayStore
			if !isEdge {
				store, err = relay.OpenRelay(cfg.RelayDBPath())
				if err != nil {
					return fmt.Errorf("open relay db: %w", err)
				}
				defer store.Close()

				if err := store.BackfillProUsers(); err != nil {
					return fmt.Errorf("backfill pro users: %w", err)
				}
			}

			// Auto-enable local mode when no auth providers are configured.
			// Must happen before JWT key check — local mode uses wing.yaml, not env.
			githubID := os.Getenv("GITHUB_CLIENT_ID")
			googleID := os.Getenv("GOOGLE_CLIENT_ID")
			smtpHost := os.Getenv("SMTP_HOST")
			if !localFlag && !isEdge && githubID == "" && googleID == "" && smtpHost == "" {
				localFlag = true
				fmt.Println("no auth providers configured — enabling local mode")
			}

			srvCfg := relay.ServerConfig{
				BaseURL:            envOr("WT_BASE_URL", "http://localhost:8080"),
				AppHost:            os.Getenv("WT_APP_HOST"),
				WSHost:             os.Getenv("WT_WS_HOST"),
				JWTKey:             os.Getenv("WT_JWT_KEY"),
				GitHubClientID:     githubID,
				GitHubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
				GoogleClientID:     googleID,
				GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
				SMTPHost:           smtpHost,
				SMTPPort:           envOr("SMTP_PORT", "587"),
				SMTPUser:           os.Getenv("SMTP_USER"),
				SMTPPass:           os.Getenv("SMTP_PASS"),
				SMTPFrom:           os.Getenv("SMTP_FROM"),
				NodeRole:           nodeRole,
				LoginNodeAddr:      loginAddr,
				FlyMachineID:       flyMachineID,
				FlyRegion:          flyRegion,
				FlyAppName:         flyApp,
				HeroVideo:          os.Getenv("WT_HERO_VIDEO"),
			}

			// JWT key: server mode requires WT_JWT_KEY env var.
			// Local mode loads/generates key from wing.yaml.
			if srvCfg.JWTKey == "" {
				if localFlag {
					key, err := ensureJWTKeyInWingYaml(cfg.Dir)
					if err != nil {
						return fmt.Errorf("jwt key: %w", err)
					}
					srvCfg.JWTKey = key
				} else {
					return fmt.Errorf("WT_JWT_KEY is required — generate with: wt keygen")
				}
			}

			srv := relay.NewServer(store, srvCfg)
			if err := srv.InitJWTKey(); err != nil {
				return fmt.Errorf("init jwt key: %w", err)
			}

			// Rate limit: 5 req/s sustained, 20 burst per IP
			srv.RateLimit = relay.NewRateLimiter(5, 20)

			if isEdge {
				// Edge: use entitlement cache for bandwidth metering
				if loginAddr == "" {
					return fmt.Errorf("WT_LOGIN_ADDR required for edge nodes")
				}
				srv.SetLoginProxy(relay.NewLoginProxy(loginAddr))
				srv.SetSessionCache(relay.NewSessionCache())
				// Bandwidth metering still works on edge, just with cached tiers
				srv.Bandwidth = relay.NewBandwidthMeter(relay.SustainedRate, 1*1024*1024, nil)
				entCache := relay.NewEntitlementCache(loginAddr)
				srv.Bandwidth.SetTierLookup(func(userID string) string {
					return entCache.GetTier(userID)
				})
				srv.EntitlementCache = entCache
				fmt.Printf("edge node: machine=%s region=%s login=%s\n", flyMachineID, flyRegion, loginAddr)
			} else {
				// Login or single node: direct DB access
				srv.Bandwidth = relay.NewBandwidthMeter(relay.SustainedRate, 1*1024*1024, store.DB())
				srv.Bandwidth.SetTierLookup(func(userID string) string {
					if store.IsUserPro(userID) {
						return "pro"
					}
					return "free"
				})
				if nodeRole == "login" {
					srv.WingMap = relay.NewWingMap()
					fmt.Printf("login node: machine=%s region=%s\n", flyMachineID, flyRegion)
				}
			}

			if devFlag {
				if _, err := os.Stat("internal/relay/templates"); err == nil {
					srv.DevTemplateDir = "internal/relay/templates"
					fmt.Println("dev mode: templates reload from source tree")
				}
				srv.DevMode = true
				fmt.Println("dev mode: auto-claim login")
			}

			if localFlag {
				if isEdge {
					return fmt.Errorf("--local is not compatible with edge mode")
				}
				user, token, err := store.CreateLocalUser()
				if err != nil {
					return fmt.Errorf("setup local user: %w", err)
				}
				srv.LocalMode = true
				srv.SetLocalUser(user)

				// Grant pro tier — self-hosted has no bandwidth cap
				if !store.IsUserPro(user.ID) {
					subID := uuid.New().String()
					store.CreateSubscription(&relay.Subscription{ID: subID, UserID: &user.ID, Plan: "local", Status: "active", Seats: 1})
					store.CreateEntitlement(&relay.Entitlement{ID: uuid.New().String(), UserID: user.ID, SubscriptionID: subID})
					store.UpdateUserTier(user.ID, "pro")
				}

				// Write device token so `wt wing` can connect without `wt login`
				ts := auth.NewTokenStore(cfg.Dir)
				ts.Save(&auth.DeviceToken{
					Token:    token,
					DeviceID: "local",
				})
				fmt.Println("local mode: single-user, no login required")
			}

			httpSrv := &http.Server{
				Addr:    addrFlag,
				Handler: srv,
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			// Sync bandwidth usage to DB every 10 minutes (only if DB available)
			if !isEdge {
				srv.Bandwidth.SeedFromDB()
				srv.Bandwidth.StartSync(ctx, 10*time.Minute)
			}

			// Start edge reconcile loop
			if isEdge && loginAddr != "" {
				srv.StartEdgeSync(ctx, loginAddr, 5*time.Second)
				fmt.Println("edge sync started (5s interval)")
				srv.GetSessionCache().StartOrgSync(ctx, loginAddr, 5*time.Minute)
			}
			if srv.EntitlementCache != nil {
				srv.EntitlementCache.StartSync(ctx, 60*time.Second)
			}

			errCh := make(chan error, 1)
			go func() {
				fmt.Printf("wt serve listening on %s\n", addrFlag)
				if localFlag {
					fmt.Println()
					fmt.Println("next: wt start --local")
					fmt.Println("then: open http://localhost:8080")
				}
				errCh <- httpSrv.ListenAndServe()
			}()

			select {
			case <-ctx.Done():
				fmt.Println("graceful shutdown (sending relay.restart to all connections)...")
				return srv.GracefulShutdown(httpSrv, 8*time.Second)
			case err := <-errCh:
				return err
			}
		},
	}

	cmd.Flags().StringVar(&addrFlag, "addr", ":8080", "listen address")
	cmd.Flags().BoolVar(&devFlag, "dev", false, "reload templates from disk on each request")
	cmd.Flags().BoolVar(&localFlag, "local", false, "single-user mode, no login required")

	return cmd
}

// ensureJWTKeyInWingYaml loads the JWT signing key from wing.yaml, or generates
// one and saves it. Used by local/roost mode where there's no external secrets manager.
func ensureJWTKeyInWingYaml(configDir string) (string, error) {
	wingCfg, err := config.LoadWingConfig(configDir)
	if err != nil {
		return "", fmt.Errorf("load wing config: %w", err)
	}

	if wingCfg.JWTKey != "" {
		return wingCfg.JWTKey, nil
	}

	// Auto-generate and persist
	_, encoded, err := relay.GenerateECKey()
	if err != nil {
		return "", err
	}

	wingCfg.JWTKey = encoded
	if err := config.SaveWingConfig(configDir, wingCfg); err != nil {
		return "", fmt.Errorf("save wing config: %w", err)
	}
	fmt.Println("generated JWT signing key → wing.yaml")
	return encoded, nil
}
