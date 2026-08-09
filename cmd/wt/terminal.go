package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func terminalCmd() *cobra.Command {
	var (
		nameFlag   string
		configFlag string
		cwdFlag    string
		detachFlag bool
		traceFlag  bool
	)

	cmd := &cobra.Command{
		Use:     "terminal [command [args...]]",
		Aliases: []string{"new"},
		Short:   "Start a persistent shell or command",
		Long: "Start a persistent, sandboxed terminal and attach it to the current terminal. " +
			"With no command, Wingthing starts $SHELL. Use -- before command-specific flags.\n\n" +
			"Detach without stopping the process with Ctrl+B, then Q.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if traceFlag && runtime.GOOS != "linux" {
				return fmt.Errorf("--trace requires Linux (strace is not available on %s)", runtime.GOOS)
			}
			return terminalSpawn(cmd, args, nameFlag, configFlag, cwdFlag, detachFlag, traceFlag)
		},
		Example: "  wt terminal --name work\n" +
			"  wt terminal --name dev-server -- npm run dev\n" +
			"  wt attach work",
	}
	cmd.Flags().StringVarP(&nameFlag, "name", "n", "", "human-readable session name")
	cmd.Flags().StringVar(&configFlag, "config", "", "path to egg.yaml sandbox configuration")
	cmd.Flags().StringVarP(&cwdFlag, "cwd", "C", "", "working directory (default: current directory)")
	cmd.Flags().BoolVarP(&detachFlag, "detach", "d", false, "start without attaching")
	cmd.Flags().BoolVar(&traceFlag, "trace", false, "wrap the command with strace (Linux only)")
	return cmd
}

func terminalSpawn(cmd *cobra.Command, command []string, name, configPath, cwd string, detach, trace bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if cwd == "" {
		cwd, err = os.Getwd()
	} else {
		cwd, err = filepath.Abs(cwd)
	}
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	if info, statErr := os.Stat(cwd); statErr != nil || !info.IsDir() {
		return fmt.Errorf("working directory %q does not exist or is not a directory", cwd)
	}

	var eggCfg *egg.EggConfig
	if configPath != "" {
		eggCfg, err = egg.ResolveEggConfig(configPath)
		if err != nil {
			return fmt.Errorf("load egg config: %w", err)
		}
	} else {
		eggCfg = egg.DiscoverEggConfig(cwd, nil)
	}

	kind := "command"
	if len(command) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		command = []string{shell}
		kind = "shell"
	}

	cols, rows := 80, 24
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		if width, height, sizeErr := term.GetSize(fd); sizeErr == nil {
			cols, rows = width, height
		}
	}

	sessionID := uuid.New().String()[:8]
	ec, err := spawnEgg(
		cfg,
		sessionID,
		"",
		eggCfg,
		uint32(rows),
		uint32(cols),
		cwd,
		false,
		false,
		trace,
		EggIdentity{},
		0,
		spawnEggOpts{Label: name, Kind: kind, Command: command},
	)
	if err != nil {
		return fmt.Errorf("start terminal: %w", err)
	}
	_ = ec.Close()

	display := sessionID
	if name != "" {
		display = name + " (" + sessionID + ")"
	}
	if detach {
		fmt.Printf("started %s\n", display)
		return nil
	}

	detached, err := attachLocal(cmd.Context(), cfg, sessionID)
	if detached {
		fmt.Fprintf(os.Stderr, "\r\n[detached from %s]\r\n", display)
	}
	return err
}
