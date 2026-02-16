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
		Use:   "serve",
		Short: "Start the wt.ai web server",
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

				if err := relay.SeedDefaultSkills(store); err != nil {
					return fmt.Errorf("seed skills: %w", err)
				}

				if err := store.BackfillProUsers(); err != nil {
					return fmt.Errorf("backfill pro users: %w", err)
				}
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
				NodeRole:           nodeRole,
				LoginNodeAddr:      loginAddr,
				FlyMachineID:       flyMachineID,
				FlyRegion:          flyRegion,
				FlyAppName:         flyApp,
				HeroVideo:          os.Getenv("WT_HERO_VIDEO"),
			}

			srv := relay.NewServer(store, srvCfg)

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
				srv.DevTemplateDir = "internal/relay/templates"
				srv.DevMode = true
				fmt.Println("dev mode: templates reload, auto-claim login")
			}
			// Auto-enable local mode when no auth providers are configured
			if !localFlag && !isEdge && srvCfg.GitHubClientID == "" &&
				srvCfg.GoogleClientID == "" && srvCfg.SMTPHost == "" {
				localFlag = true
				fmt.Println("no auth providers configured — enabling local mode")
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
