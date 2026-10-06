package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/localrelay"
	"github.com/ehrlich-b/wingthing/internal/localtls"
	"github.com/spf13/cobra"
)

func localCertCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "local-cert",
		Short: "Manage the on-demand localhost HTTPS certificate",
		Long:  "Create and trust Wingthing's localhost-only CA. The private key is created on demand in WINGTHING_DIR, remains mode 0600, and never leaves this machine. Only the public CA certificate is installed in the current user's trust store.",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Create and trust the localhost certificate",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			m, err := localtls.Ensure(cfg.Dir, time.Now())
			if err != nil {
				return err
			}
			localrelay.PrintLocalCertDisclosure(m)
			installed, err := localrelay.InstallLocalTrust(cmd.Context(), m)
			if err != nil {
				return err
			}
			if installed {
				fmt.Println("installed only the public CA certificate in the current user's trust store")
			} else {
				fmt.Println("public CA certificate is already trusted for the current user")
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "remove",
		Short: "Remove the public CA from this user's trust store",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			m, err := localtls.Load(cfg.Dir)
			if errors.Is(err, localtls.ErrNotFound) {
				fmt.Println("no Wingthing localhost certificate has been created for this profile")
				return nil
			}
			if err != nil {
				return err
			}
			removed, err := localtls.SystemTrustStore().Remove(cmd.Context(), m)
			if err != nil {
				return err
			}
			if removed {
				fmt.Println("removed the public Wingthing localhost CA from this user's trust store")
			} else {
				fmt.Println("the public Wingthing localhost CA is not marked as trusted")
			}
			fmt.Printf("private keys were left on this machine in %s\n", m.Dir)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show local certificate and trust status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			m, err := localtls.Load(cfg.Dir)
			if errors.Is(err, localtls.ErrNotFound) {
				fmt.Println("no Wingthing localhost certificate has been created for this profile")
				return nil
			}
			if err != nil {
				return err
			}
			trusted, err := localtls.SystemTrustStore().Trusted(cmd.Context(), m)
			if err != nil {
				return err
			}
			localrelay.PrintLocalCertDisclosure(m)
			fmt.Printf("  installed: %t\n", trusted)
			return nil
		},
	})
	return cmd
}
