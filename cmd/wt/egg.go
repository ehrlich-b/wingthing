package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/spf13/cobra"
)

func eggPidPath() string {
	cfg, _ := config.Load()
	if cfg != nil {
		return cfg.Dir + "/egg.pid"
	}
	home, _ := os.UserHomeDir()
	return home + "/.wingthing/egg.pid"
}

func readEggPid() (int, error) {
	data, err := os.ReadFile(eggPidPath())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, err
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		os.Remove(eggPidPath())
		return 0, fmt.Errorf("stale pid")
	}
	return pid, nil
}

func eggCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "egg",
		Short:  "Session-holding process (internal, started by wing)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			srv, err := egg.NewServer(cfg.Dir, version)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
			go func() {
				<-sigCh
				cancel()
			}()

			return srv.Run(ctx)
		},
	}

	cmd.AddCommand(eggStopCmd())
	return cmd
}

func eggStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the egg process",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := readEggPid()
			if err != nil {
				return fmt.Errorf("no egg process running")
			}
			proc, _ := os.FindProcess(pid)
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("kill pid %d: %w", pid, err)
			}
			fmt.Printf("egg stopped (pid %d)\n", pid)
			return nil
		},
	}
}
