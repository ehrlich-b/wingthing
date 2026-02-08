package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

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

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the wingthing web server",
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

			// Load embedder and space index
			emb, err := embedding.NewFromProvider(cfg.DefaultEmbedder, "", "")
			if err != nil {
				return fmt.Errorf("init embedder: %w", err)
			}

			spacesPath := spacesFlag
			if spacesPath == "" {
				spacesPath = "spaces.yaml"
			}

			idx, err := embedding.LoadSpaceIndex(spacesPath, "spaces/cache", emb)
			if err != nil {
				return fmt.Errorf("load space index: %w", err)
			}

			n, err := relay.SeedSpacesFromIndex(store, idx, emb.Name())
			if err != nil {
				return fmt.Errorf("seed spaces: %w", err)
			}
			fmt.Printf("seeded %d spaces (%s)\n", n, emb.Name())

			if err := relay.SeedDefaultSkills(store); err != nil {
				return fmt.Errorf("seed skills: %w", err)
			}

			srvCfg := relay.ServerConfig{
				BaseURL:            envOr("WT_BASE_URL", "http://localhost:8080"),
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
			srv.Embedder = emb
			relay.StartSidebarRefresh(store, 10*time.Minute)

			httpSrv := &http.Server{
				Addr:    addrFlag,
				Handler: srv,
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

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

	return cmd
}
