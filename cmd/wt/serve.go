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
	var devFlag bool

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
			srv.Embedder = primaryEmb
			if devFlag {
				srv.DevTemplateDir = "internal/relay/templates"
				srv.DevMode = true
				fmt.Println("dev mode: templates reload, auto-claim login")
			}
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
	cmd.Flags().BoolVar(&devFlag, "dev", false, "reload templates from disk on each request")

	return cmd
}
