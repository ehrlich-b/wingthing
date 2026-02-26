package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
		Use:     "sandbox [agent]",
		Aliases: []string{"egg"},
		Short:   "Run an agent in a sandboxed session",
		Long:    "Spawns an agent (claude, ollama, codex) inside a per-session sandbox with PTY persistence.\nSet dangerously_skip_permissions in egg.yaml to bypass agent permission prompts.",
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
		shell      string
		rows       uint32
		cols       uint32
		fsFlag     []string
		networkFlag []string
		envFlag    []string
		cpuFlag    string
		memFlag    string
		maxFDsFlag  uint32
		maxPidsFlag uint32
		debugFlag  bool
		auditFlag  bool
		vteFlag    bool
		renderedConfigFlag string
		userHomeFlag string
		idleTimeoutFlag string
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

			var idleTimeout time.Duration
			if idleTimeoutFlag != "" {
				idleTimeout, _ = time.ParseDuration(idleTimeoutFlag)
			}

			rc := egg.RunConfig{
				Agent:   agentName,
				CWD:     cwd,
				Shell:   shell,
				FS:      fsFlag,
				Network: networkFlag,
				Env:     envMap,
				Rows:    rows,
				Cols:    cols,
				DangerouslySkipPermissions: dangerouslySkipPermissions,
				CPULimit:       cpuLimit,
				MemLimit:       memLimit,
				MaxFDs:         maxFDsFlag,
				PidLimit:       maxPidsFlag,
				Debug:          debugFlag,
				Audit:          auditFlag,
				VTE:            vteFlag,
				RenderedConfig: renderedConfigFlag,
				UserHome:       userHomeFlag,
				IdleTimeout:    idleTimeout,
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
			cleanEggDir(dir)

			return err
		},
	}

	cmd.Flags().StringVar(&sessionID, "session-id", "", "session ID")
	cmd.Flags().StringVar(&agentName, "agent", "claude", "agent name")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory")
	cmd.Flags().StringVar(&shell, "shell", "", "override shell")
	cmd.Flags().Uint32Var(&rows, "rows", 24, "terminal rows")
	cmd.Flags().Uint32Var(&cols, "cols", 80, "terminal cols")
	cmd.Flags().StringArrayVar(&fsFlag, "fs", nil, "filesystem rules (rw:./, deny:~/.ssh)")
	cmd.Flags().StringArrayVar(&networkFlag, "network", nil, "network domains (api.anthropic.com, *, none)")
	cmd.Flags().StringArrayVar(&envFlag, "env", nil, "environment variables (KEY=VAL)")
	cmd.Flags().BoolVar(&dangerouslySkipPermissions, "dangerously-skip-permissions", false, "skip agent permission prompts")
	cmd.Flags().StringVar(&cpuFlag, "cpu", "", "CPU time limit (e.g. 300s)")
	cmd.Flags().StringVar(&memFlag, "memory", "", "memory limit (e.g. 2GB)")
	cmd.Flags().Uint32Var(&maxFDsFlag, "max-fds", 0, "max open file descriptors")
	cmd.Flags().Uint32Var(&maxPidsFlag, "max-pids", 0, "max processes in cgroup (Linux only)")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output to /tmp")
	cmd.Flags().BoolVar(&auditFlag, "audit", false, "enable input audit log and PTY stream recording")
	cmd.Flags().BoolVar(&vteFlag, "vte", false, "use VTerm snapshot for reconnect (internal)")
	cmd.Flags().StringVar(&renderedConfigFlag, "rendered-config", "", "rendered egg config YAML (internal)")
	cmd.Flags().StringVar(&userHomeFlag, "user-home", "", "per-user home directory (internal)")
	cmd.Flags().StringVar(&idleTimeoutFlag, "idle-timeout", "", "idle timeout duration (e.g. 4h)")
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
					cleanEggDir(filepath.Join(eggsDir, sessionID))
					continue
				}

				line := fmt.Sprintf("  %s  pid=%d", sessionID, pid)

				// Try gRPC status for live debug info
				sockPath := filepath.Join(eggsDir, sessionID, "egg.sock")
				tokenPath := filepath.Join(eggsDir, sessionID, "egg.token")
				ec, dialErr := egg.Dial(sockPath, tokenPath)
				if dialErr == nil {
					ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
					st, stErr := ec.Status(ctx)
					cancel()
					ec.Close()
					if stErr == nil {
						line += fmt.Sprintf("  agent=%s  buf=%s  written=%s  trimmed=%s  readers=%d  uptime=%s  idle=%s",
							st.Agent,
							humanBytes(st.BufferBytes),
							humanBytes(st.TotalWritten),
							humanBytes(st.TotalTrimmed),
							st.Readers,
							humanDuration(time.Duration(st.UptimeSeconds)*time.Second),
							humanDuration(time.Duration(st.IdleSeconds)*time.Second),
						)
					}
				}

				fmt.Println(line)
				found = true
			}
			if !found {
				fmt.Println("no active sessions")
			}
			return nil
		},
	}
}

func humanBytes(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// eggSpawn starts an agent session in a per-session egg and attaches the terminal.
func eggSpawn(ctx context.Context, agentName, configPath string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Load egg config (with base chain resolution)
	var eggCfg *egg.EggConfig
	if configPath != "" {
		eggCfg, err = egg.ResolveEggConfig(configPath)
		if err != nil {
			return fmt.Errorf("load egg config: %w", err)
		}
	} else {
		cwd, _ := os.Getwd()
		eggCfg = egg.DiscoverEggConfig(cwd, nil)
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
	ec, err := spawnEgg(cfg, sessionID, agentName, eggCfg, uint32(rows), uint32(cols), cwd, false, false, EggIdentity{}, 0)
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

// EggIdentity holds the authenticated user's identity for per-session env injection.
// Zero value means no identity (local egg, no authenticated user).
type EggIdentity struct {
	UserID      string // relay user ID
	Email       string // authenticated email (e.g. from Google OAuth)
	DisplayName string // human-readable name (Google full name, GitHub login)
	IsOwner     bool   // owner/admin of this wing — use real HOME, skip per-user isolation
}

// sanitizeEnvValue strips characters that could cause shell injection.
// Allows alphanumeric, spaces, hyphens, underscores, dots, and @.
func sanitizeEnvValue(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == ' ' || c == '-' || c == '_' || c == '.' || c == '@' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// NormalizeUser converts an email local part to a safe username.
// Lowercase, alphanumeric + hyphens only. Dots and special chars become hyphens.
func NormalizeUser(email string) string {
	local, _, _ := strings.Cut(email, "@")
	local = strings.ToLower(local)
	var b strings.Builder
	for _, c := range local {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	// Collapse multiple hyphens and trim edges
	s := b.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// userHash returns the first 12 hex chars of the SHA256 of the email.
func userHash(email string) string {
	h := sha256.Sum256([]byte(email))
	return hex.EncodeToString(h[:])[:12]
}

// spawnEgg starts a per-session egg child process and returns a connected client.
func spawnEgg(cfg *config.Config, sessionID, agentName string, eggCfg *egg.EggConfig, rows, cols uint32, cwd string, debug, vte bool, identity EggIdentity, idleTimeout time.Duration) (*egg.Client, error) {
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
		"--rows", strconv.Itoa(int(rows)),
		"--cols", strconv.Itoa(int(cols)),
	}
	if eggCfg.Shell != "" {
		args = append(args, "--shell", eggCfg.Shell)
	}
	if eggCfg.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	for _, entry := range eggCfg.FS {
		// Resolve relative paths in fs entries
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			path = entry
			mode = "rw"
		}
		if path == "." || path == "./" {
			path = cwd
		} else if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			path = filepath.Join(cwd, path)
		}
		args = append(args, "--fs", mode+":"+path)
	}
	for _, d := range eggCfg.Network {
		args = append(args, "--network", d)
	}
	envMap := eggCfg.BuildEnvMap()
	// Inject agent profile env vars from host env (e.g. ANTHROPIC_API_KEY for claude).
	// BuildEnvMap uses the egg config whitelist which may not include these.
	profile := egg.Profile(agentName)
	for _, k := range profile.EnvVars {
		if _, ok := envMap[k]; !ok {
			if v := os.Getenv(k); v != "" {
				envMap[k] = v
			}
		}
	}
	// Per-user home directory for multi-user isolation on shared machines.
	// Owners and admins use real HOME — they're the machine owner and
	// shouldn't be sandboxed into a synthetic home. Only non-owner
	// authenticated users get per-user homes.
	realHome, _ := os.UserHomeDir()
	effectiveHome := realHome
	if identity.Email != "" && !identity.IsOwner {
		perUserHome := filepath.Join(cfg.Dir, "user-homes", userHash(identity.Email))
		os.MkdirAll(perUserHome, 0700)
		effectiveHome = perUserHome
		// Seed shell + agent config symlinks from real HOME
		if realHome != "" {
			for _, rc := range []string{".bashrc", ".zshrc", ".profile", ".claude.json"} {
				src := filepath.Join(realHome, rc)
				dst := filepath.Join(perUserHome, rc)
				if _, err := os.Stat(src); err == nil {
					if _, err := os.Lstat(dst); err != nil {
						os.Symlink(src, dst)
					}
				}
			}
		}
		// Create ~/.local/bin and symlink the agent binary so Claude Code
		// doesn't warn about missing native install dir or command not found.
		localBin := filepath.Join(perUserHome, ".local", "bin")
		os.MkdirAll(localBin, 0755)
		if agentBin, err := exec.LookPath(agentName); err == nil {
			dst := filepath.Join(localBin, agentName)
			if _, err := os.Lstat(dst); err != nil {
				os.Symlink(agentBin, dst)
			}
		}
		args = append(args, "--user-home", perUserHome)
	}
	// Rebuild agent settings every session for non-owner users.
	// Owners use their real HOME and real agent config — no overrides.
	// For guests, reads existing prefs, layers host settings on top
	// (host always wins for permissions), then injects agent-specific overrides.
	if identity.Email != "" && !identity.IsOwner {
		agentProfile := egg.Profile(agentName)
		if agentProfile.SettingsFile != "" {
			settingsDst := filepath.Join(effectiveHome, agentProfile.SettingsFile)
			os.MkdirAll(filepath.Dir(settingsDst), 0700)
			baseSettings := make(map[string]any)
			// Read existing session settings to preserve user preferences
			if data, err := os.ReadFile(settingsDst); err == nil {
				json.Unmarshal(data, &baseSettings)
			}
			// Layer host settings on top (permissions from host always win)
			if srcPath, ok := eggCfg.AgentSettings[agentName]; ok {
				if data, err := os.ReadFile(srcPath); err == nil {
					var hostSettings map[string]any
					if json.Unmarshal(data, &hostSettings) == nil {
						for k, v := range hostSettings {
							baseSettings[k] = v
						}
					}
				}
			} else if realHome != "" {
				hostPath := filepath.Join(realHome, agentProfile.SettingsFile)
				if data, err := os.ReadFile(hostPath); err == nil {
					var hostSettings map[string]any
					if json.Unmarshal(data, &hostSettings) == nil {
						for k, v := range hostSettings {
							baseSettings[k] = v
						}
					}
				}
			}
			// Clean up stale apiKeyHelper from previous sessions —
			// ANTHROPIC_API_KEY is now passed directly via env.
			delete(baseSettings, "apiKeyHelper")
			if len(baseSettings) > 0 {
				if data, err := json.MarshalIndent(baseSettings, "", "  "); err == nil {
					os.WriteFile(settingsDst, append(data, '\n'), 0644)
				}
			}
		}
	}
	for k, v := range envMap {
		// Skip WT_ prefix — reserved for session identity injection
		if strings.HasPrefix(k, "WT_") {
			continue
		}
		args = append(args, "--env", k+"="+v)
	}
	// Inject per-session identity vars (always override, not configurable via egg.yaml).
	// All values are sanitized to prevent shell injection.
	args = append(args, "--env", "WT_SESSION_ID="+sessionID)
	// Preview: session-specific file so multi-user previews don't collide.
	// The shim writes to $WT_PREVIEW_DIR/$WT_PREVIEW_FILE.
	args = append(args, "--env", "WT_PREVIEW_DIR="+cwd)
	args = append(args, "--env", "WT_PREVIEW_FILE=.wt-preview-"+sessionID)
	if identity.Email != "" {
		args = append(args, "--env", "WT_USER="+NormalizeUser(identity.Email))
		args = append(args, "--env", "WT_USER_EMAIL="+sanitizeEnvValue(identity.Email))
	}
	if identity.DisplayName != "" {
		args = append(args, "--env", "WT_USER_NAME="+sanitizeEnvValue(identity.DisplayName))
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
	if eggCfg.Resources.MaxPids > 0 {
		args = append(args, "--max-pids", strconv.Itoa(int(eggCfg.Resources.MaxPids)))
	}
	if debug {
		args = append(args, "--debug")
	}
	if vte {
		args = append(args, "--vte")
	}
	if eggCfg.Audit {
		args = append(args, "--audit")
	}
	if idleTimeout > 0 {
		args = append(args, "--idle-timeout", idleTimeout.String())
	}

	// Serialize rendered config as YAML for status RPC
	if rendered, yamlErr := eggCfg.YAML(); yamlErr == nil {
		args = append(args, "--rendered-config", rendered)
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
