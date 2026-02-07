package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"github.com/ehrlich-b/wingthing/internal/relay"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "wtd",
		Short: "wingthing relay server",
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, _ := cmd.Flags().GetString("addr")
			dbPath, _ := cmd.Flags().GetString("db")

			store, err := relay.OpenRelay(dbPath)
			if err != nil {
				return fmt.Errorf("open relay store: %w", err)
			}
			defer store.Close()

			srv := relay.NewServer(store)

			httpSrv := &http.Server{
				Addr:    addr,
				Handler: srv,
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			errCh := make(chan error, 1)
			go func() {
				fmt.Printf("wtd listening on %s\n", addr)
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

	root.Flags().String("addr", ":8080", "listen address")
	root.Flags().String("db", "wtd.db", "database path")

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
