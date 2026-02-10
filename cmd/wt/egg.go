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
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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
		Use:   "egg [agent]",
		Short: "Run an agent in a sandboxed session",
		Long:  "Spawns an agent (claude, ollama, codex) inside the egg process with PTY persistence and optional sandboxing.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return eggSpawn(cmd.Context(), args[0])
		},
	}

	cmd.AddCommand(eggServeCmd())
	cmd.AddCommand(eggStopCmd())
	cmd.AddCommand(eggListCmd())
	return cmd
}

// eggServeCmd starts the gRPC server (internal, called by wing).
func eggServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "serve",
		Short:  "Start egg gRPC server (internal)",
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

func eggListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active egg sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			ec, err := ensureEgg(cfg)
			if err != nil {
				return fmt.Errorf("connect to egg: %w", err)
			}
			defer ec.Close()

			resp, err := ec.List(cmd.Context())
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			if len(resp.Sessions) == 0 {
				fmt.Println("no active sessions")
				return nil
			}
			for _, s := range resp.Sessions {
				fmt.Printf("  %s  %s  %s  %s\n", s.SessionId, s.Agent, s.Cwd, s.StartedAt)
			}
			return nil
		},
	}
}

// eggSpawn starts an agent session in the egg and attaches the terminal.
func eggSpawn(ctx context.Context, agentName string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ec, err := ensureEgg(cfg)
	if err != nil {
		return fmt.Errorf("connect to egg: %w", err)
	}
	defer ec.Close()

	// Get terminal size
	fd := int(os.Stdin.Fd())
	cols, rows := 80, 24
	if term.IsTerminal(fd) {
		w, h, err := term.GetSize(fd)
		if err == nil {
			cols, rows = w, h
		}
	}

	cwd, _ := os.Getwd()
	sessionID := uuid.New().String()[:8]

	// Pass through current environment
	env := make(map[string]string)
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}

	_, err = ec.Spawn(ctx, &pb.SpawnRequest{
		SessionId: sessionID,
		Agent:     agentName,
		Cwd:       cwd,
		Rows:      uint32(rows),
		Cols:      uint32(cols),
		Env:       env,
	})
	if err != nil {
		return fmt.Errorf("spawn %s: %w", agentName, err)
	}

	stream, err := ec.AttachSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("attach session: %w", err)
	}

	// Put terminal in raw mode
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			defer term.Restore(fd, oldState)
		}
	}

	// Handle SIGWINCH for terminal resize
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)

	go func() {
		for range winchCh {
			if w, h, err := term.GetSize(fd); err == nil {
				ec.Resize(ctx, sessionID, uint32(h), uint32(w))
			}
		}
	}()

	// Read output from egg → stdout
	exitCode := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			switch p := msg.Payload.(type) {
			case *pb.SessionMsg_Output:
				os.Stdout.Write(p.Output)
			case *pb.SessionMsg_ExitCode:
				exitCode = int(p.ExitCode)
				return
			}
		}
	}()

	// Read stdin → egg input
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				stream.Send(&pb.SessionMsg{
					SessionId: sessionID,
					Payload:   &pb.SessionMsg_Input{Input: data},
				})
			}
			if err != nil {
				return
			}
		}
	}()

	<-done

	if exitCode != 0 {
		return fmt.Errorf("agent exited with code %d", exitCode)
	}
	return nil
}
