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
	"github.com/ehrlich-b/wingthing/internal/embedding"
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
	var spacesFlag string
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

			store, err := relay.OpenRelay(cfg.SocialDBPath())
			if err != nil {
				return fmt.Errorf("open social db: %w", err)
			}
			defer store.Close()

			// Collect all available embedders
			var embedders []embedding.Embedder
			if emb, err := embedding.NewFromProvider("ollama", "", ""); err == nil {
				embedders = append(embedders, emb)
			}
			if key := os.Getenv("OPENAI_API_KEY"); key != "" {
				if emb, err := embedding.NewFromProvider("openai", "", ""); err == nil {
					embedders = append(embedders, emb)
				}
			}

			// Primary embedder for server API (nil = read-only mode, no new posts)
			var primaryEmb embedding.Embedder
			if len(embedders) > 0 {
				primaryEmb = embedders[0]
			}

			spacesPath := spacesFlag
			if spacesPath == "" {
				spacesPath = "spaces.yaml"
			}

			idx, err := embedding.LoadSpaceIndex(spacesPath, "spaces/cache", embedders...)
			if err != nil {
				return fmt.Errorf("load space index: %w", err)
			}

			// Seed anchors for each embedder
			for _, emb := range embedders {
				n, err := relay.SeedSpacesFromIndex(store, idx, emb.Name())
				if err != nil {
					return fmt.Errorf("seed spaces (%s): %w", emb.Name(), err)
				}
				fmt.Printf("seeded %d spaces (%s)\n", n, emb.Name())
			}

			if err := relay.SeedDefaultSkills(store); err != nil {
				return fmt.Errorf("seed skills: %w", err)
			}

			srvCfg := relay.ServerConfig{
				BaseURL:            envOr("WT_BASE_URL", "http://localhost:8080"),
				AppHost:            os.Getenv("WT_APP_HOST"),
				WSHost:             os.Getenv("WT_WS_HOST"),
				JWTSecret:          os.Getenv("WT_JWT_SECRET"),
				GitHubClientID:     os.Getenv("GITHUB_CLIENT_ID"),
				GitHubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
					SMTPHost:           os.Getenv("SMTP_HOST"),
				SMTPPort:           envOr("SMTP_PORT", "587"),
				SMTPUser:           os.Getenv("SMTP_USER"),
				SMTPPass:           os.Getenv("SMTP_PASS"),
				SMTPFrom:           os.Getenv("SMTP_FROM"),
			}

			srv := relay.NewServer(store, srvCfg)
			srv.Embedder = primaryEmb
			// Rate limiter: 256 KB/s sustained, 16 MB burst per user
			srv.Bandwidth = relay.NewBandwidthMeter(256*1024, 16*1024*1024, store.DB())
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
			relay.StartSidebarRefresh(store, 10*time.Minute)

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
	cmd.Flags().StringVar(&spacesFlag, "spaces", "", "path to spaces.yaml (default: spaces.yaml)")
	cmd.Flags().BoolVar(&devFlag, "dev", false, "reload templates from disk on each request")
	cmd.Flags().BoolVar(&localFlag, "local", false, "single-user mode, no login required")

	return cmd
}
