package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
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

func relayPolicyFromEnv() (string, time.Time, error) {
	policy := strings.TrimSpace(envOr("WT_RELAY_POLICY", relay.RelayPolicyLegacy))
	if policy != relay.RelayPolicyLegacy && policy != relay.RelayPolicyDirectFree {
		return "", time.Time{}, fmt.Errorf("WT_RELAY_POLICY must be %q or %q", relay.RelayPolicyLegacy, relay.RelayPolicyDirectFree)
	}

	// The migration boundary is deployment state, not a compile-time product
	// default. Prefer the accurately named variable while retaining the old one
	// as a compatibility alias for existing operators.
	raw := strings.TrimSpace(os.Getenv("WT_RELAY_MIGRATION_BEFORE"))
	legacyRaw := strings.TrimSpace(os.Getenv("WT_RELAY_GRANDFATHER_BEFORE"))
	if raw != "" && legacyRaw != "" && raw != legacyRaw {
		return "", time.Time{}, fmt.Errorf("WT_RELAY_MIGRATION_BEFORE and deprecated WT_RELAY_GRANDFATHER_BEFORE disagree")
	}
	if raw == "" {
		raw = legacyRaw
	}
	if raw == "" {
		return policy, time.Time{}, nil
	}
	cutoff, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("WT_RELAY_MIGRATION_BEFORE must be RFC3339: %w", err)
	}
	return policy, cutoff, nil
}

func saveLocalServeToken(configDir, token string) error {
	return auth.NewLocalTokenStore(configDir).Save(&auth.DeviceToken{
		Token:    token,
		DeviceID: "local",
	})
}

func serveCmd() *cobra.Command {
	var addrFlag string
	var devFlag bool
	var localFlag bool
	var httpsFlag bool
	var httpsAddrFlag string

	cmd := &cobra.Command{
		Use:     "relay",
		Aliases: []string{"serve"},
		Short:   "Start the relay server (web UI + WebSocket relay)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !localFlag {
				if err := validateAuthProviderEnvironment(); err != nil {
					return err
				}
			}
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
				relayDBPath, pathErr := cfg.RelayDBPath()
				if pathErr != nil {
					return pathErr
				}
				store, err = relay.OpenRelay(relayDBPath)
				if err != nil {
					return fmt.Errorf("open relay db: %w", err)
				}
				defer closeWithLog("relay store", store)

				if err := store.BackfillProUsers(); err != nil {
					return fmt.Errorf("backfill pro users: %w", err)
				}
			}

			// Auto-enable local mode when no auth providers are configured.
			// Must happen before JWT key check — local mode uses wing.yaml, not env.
			githubID := strings.TrimSpace(os.Getenv("GITHUB_CLIENT_ID"))
			googleID := strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID"))
			smtpHost := strings.TrimSpace(os.Getenv("SMTP_HOST"))
			if !localFlag && !isEdge && !authProvidersConfigured() {
				localFlag = true
				fmt.Println("no auth providers configured — enabling local mode")
			}
			if localFlag && !httpsFlag {
				addrFlag, err = prepareLocalHTTPAddress(addrFlag, cmd.Flags().Changed("addr"))
				if err != nil {
					return err
				}
			}
			if err := validateLocalHTTPSMode(httpsFlag, localFlag, isEdge); err != nil {
				return err
			}
			var localHTTPS *localHTTPSConfig
			if httpsFlag {
				localHTTPS, err = prepareLocalHTTPS(cmd.Context(), cfg.Dir, addrFlag, httpsAddrFlag, cmd.Flags().Changed("addr"))
				if err != nil {
					return err
				}
				addrFlag = localHTTPS.HTTPAddr
			}
			jwtKey, err := jwtKeyFromEnvironment()
			if err != nil {
				return fmt.Errorf("jwt key: %w", err)
			}
			relayPolicy, relayMigrationBefore, err := relayPolicyFromEnv()
			if err != nil {
				return err
			}
			allowedEmails, err := roostAllowedEmailsFromEnv()
			if err != nil {
				return err
			}

			srvCfg := relay.ServerConfig{
				BaseURL:              defaultBaseURL(localHTTPS),
				AppHost:              os.Getenv("WT_APP_HOST"),
				WSHost:               os.Getenv("WT_WS_HOST"),
				JWTKey:               jwtKey,
				InternalSecret:       os.Getenv("WT_INTERNAL_SECRET"),
				GitHubClientID:       githubID,
				GitHubClientSecret:   os.Getenv("GITHUB_CLIENT_SECRET"),
				GoogleClientID:       googleID,
				GoogleClientSecret:   os.Getenv("GOOGLE_CLIENT_SECRET"),
				SMTPHost:             smtpHost,
				SMTPPort:             envOr("SMTP_PORT", "587"),
				SMTPUser:             os.Getenv("SMTP_USER"),
				SMTPPass:             os.Getenv("SMTP_PASS"),
				SMTPFrom:             os.Getenv("SMTP_FROM"),
				NodeRole:             nodeRole,
				LoginNodeAddr:        loginAddr,
				FlyMachineID:         flyMachineID,
				FlyRegion:            flyRegion,
				FlyAppName:           flyApp,
				HeroVideo:            os.Getenv("WT_HERO_VIDEO"),
				RelayPolicy:          relayPolicy,
				RelayMigrationBefore: relayMigrationBefore,
				RoostAllowedEmails:   allowedEmails,
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
				srv.SetSessionCache(relay.NewSessionCache(srvCfg.InternalSecret))
				// Bandwidth metering still works on edge, just with cached tiers
				srv.Bandwidth = relay.NewBandwidthMeter(relay.SustainedRate, 1*1024*1024, nil)
				entCache := relay.NewEntitlementCache(loginAddr, srvCfg.InternalSecret)
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
				if err := ensureSelfHostedPro(store, user.ID, "local"); err != nil {
					return err
				}

				// Keep the localhost credential separate from the ordinary portal
				// login so starting a self-hosted UI cannot log this profile out of
				// wingthing.ai or an operator's private roost.
				if err := saveLocalServeToken(cfg.Dir, token); err != nil {
					return fmt.Errorf("save local device token: %w", err)
				}
				fmt.Println("local mode: single-user, no login required")
			}

			listeners := newRelayListeners(srv, addrFlag, localHTTPS)

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

			if err := listeners.Start(localHTTPS); err != nil {
				return err
			}
			if localHTTPS != nil {
				fmt.Printf("wt serve wing endpoint (loopback HTTP): %s\n", localHTTPURL(addrFlag))
				fmt.Printf("wt serve browser UI (local HTTPS): %s\n", localHTTPS.URL)
			} else {
				fmt.Printf("wt serve listening on %s\n", addrFlag)
			}
			if localFlag {
				fmt.Println()
				fmt.Println("next: wt start --local")
				if localHTTPS != nil {
					fmt.Printf("then: open %s\n", localHTTPS.URL)
				} else {
					fmt.Println("then: open http://localhost:8080")
				}
			}

			select {
			case <-ctx.Done():
				fmt.Println("graceful shutdown (sending relay.restart to all connections)...")
				return listeners.Shutdown(srv, 8*time.Second)
			case result := <-listeners.errCh:
				err := listenerResult(result)
				if err != nil {
					_ = listeners.Shutdown(srv, 8*time.Second)
				}
				return err
			}
		},
	}

	cmd.Flags().StringVar(&addrFlag, "addr", ":8080", "listen address")
	cmd.Flags().BoolVar(&devFlag, "dev", false, "reload templates from disk on each request")
	cmd.Flags().BoolVar(&localFlag, "local", false, "single-user mode, no login required")
	cmd.Flags().BoolVar(&httpsFlag, "https", false, "serve the local browser UI over HTTPS using an on-demand, device-local CA")
	cmd.Flags().StringVar(&httpsAddrFlag, "https-addr", defaultLocalHTTPSAddr, "loopback HTTPS address for the local browser UI")

	return cmd
}

// jwtKeyFromEnvironment prefers an explicit encoded P-256 key. Existing deployments that
// provide only WT_JWT_SECRET get a stable, domain-separated P-256 key without another secret.
func jwtKeyFromEnvironment() (string, error) {
	if key := os.Getenv("WT_JWT_KEY"); key != "" {
		return key, nil
	}
	if secret := os.Getenv("WT_JWT_SECRET"); secret != "" {
		return relay.DeriveECKeyStringFromSecret(secret)
	}
	return "", nil
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
