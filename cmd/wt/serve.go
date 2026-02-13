package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/relay"
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

			store, err := relay.OpenRelay(cfg.RelayDBPath())
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
			}

			srv := relay.NewServer(store, srvCfg)
			// Bandwidth: 64 KB/s sustained, 1 MB burst per user (PTY-only traffic)
			srv.Bandwidth = relay.NewBandwidthMeter(64*1024, 1*1024*1024, store.DB())
			srv.Bandwidth.SetTierLookup(func(userID string) string {
				if store.IsUserPro(userID) {
					return "pro"
				}
				return "free"
			})
			// Rate limit: 5 req/s sustained, 20 burst per IP (friends-and-family)
			srv.RateLimit = relay.NewRateLimiter(5, 20)
			if devFlag {
				srv.DevTemplateDir = "internal/relay/templates"
				srv.DevMode = true
				fmt.Println("dev mode: templates reload, auto-claim login")
			}
			if localFlag {
				user, token, err := store.CreateLocalUser()
				if err != nil {
					return fmt.Errorf("setup local user: %w", err)
				}
				srv.LocalMode = true
				srv.SetLocalUser(user)

				// Write device token so `wt wing` can connect without `wt login`
				ts := auth.NewTokenStore(cfg.Dir)
				ts.Save(&auth.DeviceToken{
					Token:    token,
					DeviceID: "local",
				})
				fmt.Println("local mode: auth bypassed, device token written")
			}

			httpSrv := &http.Server{
				Addr:    addrFlag,
				Handler: srv,
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			// Sync bandwidth usage to DB every 10 minutes
			srv.Bandwidth.StartSync(ctx, 10*time.Minute)

			errCh := make(chan error, 1)
			go func() {
				fmt.Printf("wt serve listening on %s\n", addrFlag)
				errCh <- httpSrv.ListenAndServe()
			}()

			select {
			case <-ctx.Done():
				fmt.Println("shutting down...")
				return httpSrv.Close()
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
