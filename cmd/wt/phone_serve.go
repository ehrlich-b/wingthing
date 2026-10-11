package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/phoneweb"
	"github.com/spf13/cobra"
)

func phoneServeCmd() *cobra.Command {
	var origin, upstream, keyFile, addr string
	cmd := &cobra.Command{
		Use: "serve", Short: "Run a private loopback phone gateway to an existing local roost", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := phoneweb.ValidateListenAddr(addr); err != nil {
				return err
			}
			gateway, err := phoneweb.New(origin, upstream, keyFile)
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			server := &http.Server{Addr: addr, Handler: gateway, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
				BaseContext: func(net.Listener) context.Context { return ctx },
			}
			go func() { <-ctx.Done(); _ = server.Close() }()
			fmt.Fprintf(cmd.OutOrStdout(), "Private phone gateway on %s for %s/app/\nForward only this gateway through an exact-email authenticated tunn endpoint.\n", addr, origin)
			err = server.ListenAndServe()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&origin, "origin", "", "exact external HTTPS origin, e.g. https://your-id.tunn.to")
	cmd.Flags().StringVar(&upstream, "upstream", "http://127.0.0.1:8080", "existing local roost HTTP origin")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "private file containing a random 256-bit key (64 hex characters)")
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8081", "literal loopback gateway address")
	return cmd
}
