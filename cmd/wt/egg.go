package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func eggCmd() *cobra.Command {
	var configFlag string

	cmd := &cobra.Command{
		Use:   "egg [agent]",
		Short: "Run an agent in a sandboxed session",
		Long:  "Spawns an agent (claude, ollama, codex) inside a per-session egg process with PTY persistence and optional sandboxing.\nStandalone eggs use dangerously_skip_permissions by default — the sandbox IS the permission boundary.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return eggSpawn(cmd.Context(), args[0], configFlag)
		},
	}

	cmd.Flags().StringVar(&configFlag, "config", "", "path to egg.yaml (default: discover from cwd, then ~/.wingthing/egg.yaml, then built-in)")

	cmd.AddCommand(eggRunCmd())
	cmd.AddCommand(eggStopCmd())
	cmd.AddCommand(eggListCmd())
	return cmd
}

// eggRunCmd starts a single per-session egg process (hidden, called by wing or eggSpawn).
func eggRunCmd() *cobra.Command {
	var (
		sessionID  string
		agentName  string
		cwd        string
		isolation  string
		shell      string
		rows       uint32
		cols       uint32
		mountsFlag []string
		denyFlag   []string
		envFlag    []string
		cpuFlag    string
		memFlag    string
		maxFDsFlag uint32
		debugFlag  bool
		dangerouslySkipPermissions bool
	)

	cmd := &cobra.Command{
		Use:    "run",
		Short:  "Run a single-session egg process (internal)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			dir := filepath.Join(cfg.Dir, "eggs", sessionID)
			if err := os.MkdirAll(dir, 0700); err != nil {
				return fmt.Errorf("create egg dir: %w", err)
			}

			srv, err := egg.NewServer(dir)
			if err != nil {
				return err
			}

			// Parse env flags into map
			envMap := make(map[string]string)
			for _, e := range envFlag {
				k, v, ok := strings.Cut(e, "=")
				if ok {
					envMap[k] = v
				}
			}

			var cpuLimit time.Duration
			if cpuFlag != "" {
				cpuLimit, _ = time.ParseDuration(cpuFlag)
			}
			var memLimit uint64
			if memFlag != "" {
				memLimit = parseMemFlag(memFlag)
			}

			rc := egg.RunConfig{
				Agent:     agentName,
				CWD:       cwd,
				Isolation: isolation,
				Shell:     shell,
				Mounts:    mountsFlag,
				Deny:      denyFlag,
				Env:       envMap,
				Rows:      rows,
				Cols:      cols,
				DangerouslySkipPermissions: dangerouslySkipPermissions,
				CPULimit:  cpuLimit,
				MemLimit:  memLimit,
				MaxFDs:    maxFDsFlag,
				Debug:     debugFlag,
			}

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
			go func() {
				<-sigCh
				cancel()
			}()

			err = srv.RunSession(ctx, rc)

			// Clean up session directory on exit
			os.Remove(filepath.Join(dir, "egg.sock"))
			os.Remove(filepath.Join(dir, "egg.token"))
			os.Remove(filepath.Join(dir, "egg.pid"))
			os.Remove(filepath.Join(dir, "egg.meta"))
			os.Remove(filepath.Join(dir, "egg.log"))
			os.Remove(dir)

			return err
		},
	}

	cmd.Flags().StringVar(&sessionID, "session-id", "", "session ID")
	cmd.Flags().StringVar(&agentName, "agent", "claude", "agent name")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory")
	cmd.Flags().StringVar(&isolation, "isolation", "network", "sandbox isolation level")
	cmd.Flags().StringVar(&shell, "shell", "", "override shell")
	cmd.Flags().Uint32Var(&rows, "rows", 24, "terminal rows")
	cmd.Flags().Uint32Var(&cols, "cols", 80, "terminal cols")
	cmd.Flags().StringArrayVar(&mountsFlag, "mounts", nil, "sandbox mounts (~/repos:rw)")
	cmd.Flags().StringArrayVar(&denyFlag, "deny", nil, "paths to deny")
	cmd.Flags().StringArrayVar(&envFlag, "env", nil, "environment variables (KEY=VAL)")
	cmd.Flags().BoolVar(&dangerouslySkipPermissions, "dangerously-skip-permissions", false, "skip agent permission prompts")
	cmd.Flags().StringVar(&cpuFlag, "cpu", "", "CPU time limit (e.g. 300s)")
	cmd.Flags().StringVar(&memFlag, "memory", "", "memory limit (e.g. 2GB)")
	cmd.Flags().Uint32Var(&maxFDsFlag, "max-fds", 0, "max open file descriptors")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output to /tmp")
	cmd.MarkFlagRequired("session-id")

	return cmd
}

func eggStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <session-id>",
		Short: "Stop an egg session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			sessionID := args[0]
			pidPath := filepath.Join(cfg.Dir, "eggs", sessionID, "egg.pid")
			data, err := os.ReadFile(pidPath)
			if err != nil {
				return fmt.Errorf("session %s not found", sessionID)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				return fmt.Errorf("bad pid file")
			}
			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("find process: %w", err)
			}
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("kill pid %d: %w", pid, err)
			}
			fmt.Printf("egg %s stopped (pid %d)\n", sessionID, pid)
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
			eggsDir := filepath.Join(cfg.Dir, "eggs")
			entries, err := os.ReadDir(eggsDir)
			if err != nil {
				fmt.Println("no active sessions")
				return nil
			}

			found := false
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				sessionID := e.Name()
				pidPath := filepath.Join(eggsDir, sessionID, "egg.pid")
				data, err := os.ReadFile(pidPath)
				if err != nil {
					continue
				}
				pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil {
					continue
				}
				proc, err := os.FindProcess(pid)
				if err != nil {
					continue
				}
				if err := proc.Signal(syscall.Signal(0)); err != nil {
					// Dead — clean up
					os.Remove(filepath.Join(eggsDir, sessionID, "egg.sock"))
					os.Remove(filepath.Join(eggsDir, sessionID, "egg.token"))
					os.Remove(filepath.Join(eggsDir, sessionID, "egg.pid"))
					os.Remove(filepath.Join(eggsDir, sessionID, "egg.meta"))
					os.Remove(filepath.Join(eggsDir, sessionID, "egg.log"))
					os.Remove(filepath.Join(eggsDir, sessionID))
					continue
				}
				fmt.Printf("  %s  pid=%d\n", sessionID, pid)
				found = true
			}
			if !found {
				fmt.Println("no active sessions")
			}
			return nil
		},
	}
}

// eggSpawn starts an agent session in a per-session egg and attaches the terminal.
func eggSpawn(ctx context.Context, agentName, configPath string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Load egg config
	var eggCfg *egg.EggConfig
	if configPath != "" {
		eggCfg, err = egg.LoadEggConfig(configPath)
		if err != nil {
			return fmt.Errorf("load egg config: %w", err)
		}
	} else {
		cwd, _ := os.Getwd()
		eggCfg = egg.DiscoverEggConfig(cwd, nil)
	}
	// Standalone eggs default to dangerously_skip_permissions ON
	if !eggCfg.DangerouslySkipPermissions {
		eggCfg.DangerouslySkipPermissions = true
	}

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

	// Spawn egg as child process
	ec, err := spawnEgg(cfg, sessionID, agentName, eggCfg, uint32(rows), uint32(cols), cwd, false)
	if err != nil {
		return fmt.Errorf("spawn egg: %w", err)
	}
	defer ec.Close()

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

// spawnEgg starts a per-session egg child process and returns a connected client.
func spawnEgg(cfg *config.Config, sessionID, agentName string, eggCfg *egg.EggConfig, rows, cols uint32, cwd string, debug bool) (*egg.Client, error) {
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create egg dir: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find executable: %w", err)
	}

	args := []string{"egg", "run",
		"--session-id", sessionID,
		"--agent", agentName,
		"--cwd", cwd,
		"--isolation", eggCfg.Isolation,
		"--rows", strconv.Itoa(int(rows)),
		"--cols", strconv.Itoa(int(cols)),
	}
	if eggCfg.Shell != "" {
		args = append(args, "--shell", eggCfg.Shell)
	}
	if eggCfg.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	for _, m := range eggCfg.Mounts {
		args = append(args, "--mounts", m)
	}
	for _, d := range eggCfg.Deny {
		args = append(args, "--deny", d)
	}
	for k, v := range eggCfg.BuildEnvMap() {
		args = append(args, "--env", k+"="+v)
	}
	if eggCfg.Resources.CPU != "" {
		args = append(args, "--cpu", eggCfg.Resources.CPU)
	}
	if eggCfg.Resources.Memory != "" {
		args = append(args, "--memory", eggCfg.Resources.Memory)
	}
	if eggCfg.Resources.MaxFDs > 0 {
		args = append(args, "--max-fds", strconv.Itoa(int(eggCfg.Resources.MaxFDs)))
	}
	if debug {
		args = append(args, "--debug")
	}

	logPath := filepath.Join(dir, "egg.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open egg log: %w", err)
	}

	child := exec.Command(exe, args...)
	child.Stdout = logFile
	child.Stderr = logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start egg: %w", err)
	}
	logFile.Close()

	// Poll for socket
	sockPath := filepath.Join(dir, "egg.sock")
	tokenPath := filepath.Join(dir, "egg.token")
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		ec, err := egg.Dial(sockPath, tokenPath)
		if err == nil {
			return ec, nil
		}
	}

	return nil, fmt.Errorf("egg did not start within 5s (check %s)", logPath)
}

// parseMemFlag parses a memory string like "2GB" or "512MB" into bytes.
func parseMemFlag(s string) uint64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	multiplier := uint64(1)
	if strings.HasSuffix(s, "GB") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "GB")
	} else if strings.HasSuffix(s, "MB") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "MB")
	} else if strings.HasSuffix(s, "KB") {
		multiplier = 1024
		s = strings.TrimSuffix(s, "KB")
	}
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n * multiplier
}
