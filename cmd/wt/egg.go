package main

import (
	"context"

	"errors"
	"fmt"

	"log"
	"os"

	"os/signal"
	"path/filepath"
	"runtime"

	"syscall"

	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"

	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func eggCmd() *cobra.Command {
	var configFlag string
	var traceFlag bool
	var resumeFlag string
	var nameFlag string
	var unsandboxedFlag bool
	var cwdFlag string
	var detachFlag bool
	var jsonFlag bool
	var remoteExactArgvFlag bool

	cmd := &cobra.Command{
		Use:     "sandbox [agent]",
		Aliases: []string{"egg"},
		Short:   "Run an agent in a sandboxed session",
		Long: "Spawns an agent (claude, ollama, codex) inside a per-session sandbox with PTY persistence.\n" +
			"Set dangerously_skip_permissions in egg.yaml to bypass agent permission prompts.\n\n" +
			"Arguments after -- are passed through to the agent verbatim.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return eggSpawn(cmd.Context(), args[0], configFlag, traceFlag, resumeFlag, nameFlag, cwdFlag, detachFlag || jsonFlag, jsonFlag, unsandboxedFlag, remoteExactArgvFlag, args[1:])
		},
		Example: "  wt egg claude\n" +
			"  wt egg claude --name research -- --model sonnet\n" +
			"  wt egg codex -- -m gpt-5.6-terra",
	}

	cmd.Flags().StringVar(&configFlag, "config", "", "path to egg.yaml (default: discover from cwd, then ~/.wingthing/egg.yaml, then built-in)")
	cmd.Flags().BoolVar(&traceFlag, "trace", false, "wrap sandbox with strace for syscall tracing (Linux only)")
	cmd.Flags().StringVar(&resumeFlag, "resume", "", "resume a previous session by session ID")
	cmd.Flags().StringVarP(&nameFlag, "name", "n", "", "human-readable session name")
	cmd.Flags().StringVarP(&cwdFlag, "cwd", "C", "", "working directory (default: current directory)")
	cmd.Flags().BoolVarP(&detachFlag, "detach", "d", false, "start without attaching")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "start detached and print machine-readable JSON")
	cmd.Flags().BoolVar(&unsandboxedFlag, "unsandboxed", false, "trust the host boundary; disable Wingthing filesystem, network, syscall, and resource isolation")
	cmd.Flags().BoolVar(&remoteExactArgvFlag, "remote-exact-argv", false, "preserve empty remote provider arguments (internal)")
	if err := cmd.Flags().MarkHidden("remote-exact-argv"); err != nil {
		panic(err)
	}
	cmd.MarkFlagsMutuallyExclusive("config", "unsandboxed")

	cmd.AddCommand(eggRunCmd())
	cmd.AddCommand(eggSuperviseRunCmd())
	cmd.AddCommand(eggStopCmd())
	cmd.AddCommand(eggListCmd())
	cmd.AddCommand(eggExplainCmd())
	return cmd
}

func eggSuperviseRunCmd() *cobra.Command {
	var runID, stateDir string
	cmd := &cobra.Command{
		Use: "supervise-run", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := eggclient.ValidateSessionID(runID); err != nil {
				return err
			}
			if !filepath.IsAbs(stateDir) {
				return errors.New("supervisor state directory must be absolute")
			}
			if err := os.Setenv("WINGTHING_DIR", stateDir); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			ready := os.NewFile(3, "agent-supervisor-ready")
			defer ready.Close()
			return localmcp.RunAgentSupervisor(cmd.Context(), cfg, runID, os.Stdin, ready)
		},
	}
	cmd.Flags().StringVar(&runID, "run-id", "", "authorized run ID")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "owning Wingthing state directory")
	return cmd
}

// eggRunCmd starts a single per-session egg process (hidden, called by wing or eggSpawn).
func eggRunCmd() *cobra.Command {
	var (
		sessionID                  string
		agentName                  string
		cwd                        string
		shell                      string
		rows                       uint32
		cols                       uint32
		fsFlag                     []string
		roostPolicyPathFlag        []string
		networkFlag                []string
		localPortFlag              []int
		networkModeFlag            string
		agentDomainsFlag           string
		envFlag                    []string
		envFileRequired            bool
		cpuFlag                    string
		memFlag                    string
		maxFDsFlag                 uint32
		maxPidsFlag                uint32
		debugFlag                  bool
		auditFlag                  bool
		traceFlag                  bool
		vteFlag                    bool
		renderedConfigFlag         string
		userHomeFlag               string
		skipHostAgentEnvFlag       bool
		idleTimeoutFlag            string
		dangerouslySkipPermissions bool
		resumeSessionFlag          string
		providerSessionFlag        string
		toolNamesFlag              []string
		toolSocketFlag             string
		kindFlag                   string
		commandFlag                []string
		agentArgFlag               []string
		outerBoundaryFlag          bool
		protectedWriteTargetFlag   []string
		omitBrowserBridgeFlag      bool
	)

	cmd := &cobra.Command{
		Use:    "run",
		Short:  "Run a single-session egg process (internal)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := eggclient.ValidateSessionID(sessionID); err != nil {
				return err
			}
			cfg, err := eggclient.LoadConfigForEgg(sessionID)
			if err != nil {
				return err
			}
			envPath := filepath.Join(cfg.Dir, "eggs", sessionID, ".egg.env")
			envMap, err := eggclient.ReadEggEnvironment(envPath, envFlag, envFileRequired)
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

			var cpuLimit time.Duration
			if cpuFlag != "" {
				cpuLimit, _ = time.ParseDuration(cpuFlag)
			}
			var memLimit uint64
			if memFlag != "" {
				memLimit = eggclient.ParseMemFlag(memFlag)
			}

			var idleTimeout time.Duration
			if idleTimeoutFlag != "" {
				idleTimeout, _ = time.ParseDuration(idleTimeoutFlag)
			}

			rc := egg.RunConfig{
				Agent:                      agentName,
				Kind:                       kindFlag,
				Command:                    commandFlag,
				AgentArgs:                  agentArgFlag,
				CWD:                        cwd,
				Shell:                      shell,
				FS:                         fsFlag,
				RoostPolicyPaths:           roostPolicyPathFlag,
				Network:                    networkFlag,
				LocalPorts:                 localPortFlag,
				NetworkMode:                networkModeFlag,
				AgentDomains:               agentDomainsFlag,
				Env:                        envMap,
				Rows:                       rows,
				Cols:                       cols,
				DangerouslySkipPermissions: dangerouslySkipPermissions,
				CPULimit:                   cpuLimit,
				MemLimit:                   memLimit,
				MaxFDs:                     maxFDsFlag,
				PidLimit:                   maxPidsFlag,
				Debug:                      debugFlag,
				Audit:                      auditFlag,
				Trace:                      traceFlag,
				VTE:                        vteFlag,
				RenderedConfig:             renderedConfigFlag,
				UserHome:                   userHomeFlag,
				SkipHostAgentEnv:           skipHostAgentEnvFlag,
				IdleTimeout:                idleTimeout,
				ResumeSessionID:            resumeSessionFlag,
				ProviderSessionID:          providerSessionFlag,
				ToolNames:                  toolNamesFlag,
				ToolSocketPath:             toolSocketFlag,
				OuterBoundary:              outerBoundaryFlag,
				ProtectedWriteTargets:      protectedWriteTargetFlag,
				OmitBrowserBridge:          omitBrowserBridgeFlag,
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
			if err != nil {
				if _, diagnosticErr := eggclient.PreserveEggFailure(dir, err); diagnosticErr != nil {
					log.Printf("egg: preserve startup failure: %v", diagnosticErr)
				}
			}

			// Clean up session directory on exit
			eggclient.CleanEggDir(dir)

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
	cmd.Flags().StringArrayVar(&roostPolicyPathFlag, "roost-policy-path", nil, "configured roost policy file (internal)")
	if err := cmd.Flags().MarkHidden("roost-policy-path"); err != nil {
		panic(err)
	}
	cmd.Flags().StringArrayVar(&networkFlag, "network", nil, "network domains (api.anthropic.com, *, none)")
	cmd.Flags().IntSliceVar(&localPortFlag, "local-port", nil, "host loopback port forwarded into the network namespace")
	cmd.Flags().StringVar(&networkModeFlag, "network-mode", "", "network policy mode: enforce or observe (internal)")
	cmd.Flags().StringVar(&agentDomainsFlag, "agent-domains", "", "agent domain policy: merge or none (internal)")
	if err := cmd.Flags().MarkHidden("network-mode"); err != nil {
		panic(err)
	}
	cmd.Flags().StringArrayVar(&envFlag, "env", nil, "environment variables (KEY=VAL)")
	cmd.Flags().BoolVar(&envFileRequired, "env-file-required", false, "require the internal environment payload")
	if err := cmd.Flags().MarkHidden("env-file-required"); err != nil {
		panic(err)
	}
	cmd.Flags().BoolVar(&dangerouslySkipPermissions, "dangerously-skip-permissions", false, "skip agent permission prompts")
	cmd.Flags().StringVar(&cpuFlag, "cpu", "", "CPU time limit (e.g. 300s)")
	cmd.Flags().StringVar(&memFlag, "memory", "", "memory limit (e.g. 2GB)")
	cmd.Flags().Uint32Var(&maxFDsFlag, "max-fds", 0, "max open file descriptors")
	cmd.Flags().Uint32Var(&maxPidsFlag, "max-pids", 0, "max processes in cgroup (Linux only)")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output to /tmp")
	cmd.Flags().BoolVar(&auditFlag, "audit", false, "enable input audit log and PTY stream recording")
	cmd.Flags().BoolVar(&traceFlag, "trace", false, "wrap sandbox with strace for syscall tracing (Linux only)")
	cmd.Flags().BoolVar(&vteFlag, "vte", false, "use VTerm snapshot for reconnect (internal)")
	cmd.Flags().StringVar(&renderedConfigFlag, "rendered-config", "", "rendered egg config YAML (internal)")
	cmd.Flags().StringVar(&userHomeFlag, "user-home", "", "per-user home directory (internal)")
	cmd.Flags().BoolVar(&skipHostAgentEnvFlag, "skip-host-agent-env", false, "do not inherit host provider credentials (internal)")
	cmd.Flags().StringVar(&idleTimeoutFlag, "idle-timeout", "", "idle timeout duration (e.g. 4h)")
	cmd.Flags().StringVar(&resumeSessionFlag, "resume-session", "", "agent session ID to resume (internal)")
	cmd.Flags().StringVar(&providerSessionFlag, "provider-session-id", "", "exact provider session ID (internal)")
	if err := cmd.Flags().MarkHidden("provider-session-id"); err != nil {
		panic(err)
	}
	cmd.Flags().StringArrayVar(&toolNamesFlag, "tool-name", nil, "privileged tool names (internal)")
	cmd.Flags().StringVar(&toolSocketFlag, "tool-socket", "", "tool socket path (internal)")
	cmd.Flags().StringVar(&kindFlag, "kind", "agent", "session kind (internal)")
	cmd.Flags().StringArrayVar(&commandFlag, "command-arg", nil, "command argument (internal)")
	cmd.Flags().StringArrayVar(&agentArgFlag, "agent-arg", nil, "extra agent argument (internal)")
	cmd.Flags().BoolVar(&outerBoundaryFlag, "outer-boundary", false, "trust the parent host boundary (internal)")
	if err := cmd.Flags().MarkHidden("outer-boundary"); err != nil {
		panic(err)
	}
	cmd.Flags().StringArrayVar(&protectedWriteTargetFlag, eggclient.ProtectedWriteTargetArg, nil, "host-protected path the sandbox must keep unwritable (internal)")
	if err := cmd.Flags().MarkHidden(eggclient.ProtectedWriteTargetArg); err != nil {
		panic(err)
	}
	cmd.Flags().BoolVar(&omitBrowserBridgeFlag, eggclient.OmitBrowserBridgeArg, false, "launch without the browser-open bridge (internal)")
	if err := cmd.Flags().MarkHidden(eggclient.OmitBrowserBridgeArg); err != nil {
		panic(err)
	}
	if err := cmd.MarkFlagRequired("session-id"); err != nil {
		panic(err)
	}

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
			session, ec, err := eggclient.OpenLocalEgg(cmd.Context(), cfg, args[0])
			if err != nil {
				return err
			}
			defer cmdutil.CloseWithLog("egg client", ec)
			if err := ec.Kill(cmd.Context(), session.ID); err != nil {
				return fmt.Errorf("stop session %s: %w", session.ID, err)
			}
			fmt.Printf("session %s stopped (pid %d)\n", session.ID, session.PID)
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
			return listEggSessions(cmd.Context(), cfg)
		},
	}
}

func listEggSessions(ctx context.Context, cfg *config.Config) error {
	return printActiveSessions(ctx, cfg, false)
}

func eggExplainCmd() *cobra.Command {
	var configFlag string
	var jsonFlag bool

	cmd := &cobra.Command{
		Use:   "explain [agent]",
		Short: "Show the effective sandbox policy for a session",
		Long: "Resolves egg.yaml against the agent's profile and prints the policy that would apply, " +
			"including every hole drilled automatically for the agent and why.\n\n" +
			"Omit the agent to see the policy for a plain shell session.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var agentName string
			if len(args) == 1 {
				agentName = args[0]
			}

			cwd, _ := os.Getwd()
			eggCfg, source, err := eggclient.LoadEggConfigForExplain(configFlag, cwd)
			if err != nil {
				return err
			}
			home, _ := os.UserHomeDir()

			policy, err := eggclient.ExplainPolicyWithProvider(eggCfg, agentName, home, source, os.Getenv("WT_PROVIDER_BASE_URL"))
			if err != nil {
				return err
			}
			if jsonFlag {
				return eggclient.WritePolicyJSON(cmd.OutOrStdout(), policy)
			}
			return eggclient.RenderPolicy(cmd.OutOrStdout(), policy)
		},
	}

	cmd.Flags().StringVar(&configFlag, "config", "", "path to egg.yaml (default: discover from cwd, then built-in)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print the policy as JSON")
	return cmd
}

// eggSpawn starts an agent session in a per-session egg and optionally attaches the terminal.
func eggSpawn(ctx context.Context, agentName, configPath string, trace bool, resumeID, name, cwd string, detach, jsonOutput, unsandboxed, preserveEmptyAgentArgs bool, agentArgs []string) error {
	if trace && runtime.GOOS != "linux" {
		return fmt.Errorf("--trace requires Linux (strace is not available on %s)", runtime.GOOS)
	}
	if trace && unsandboxed {
		return errors.New("--trace and --unsandboxed cannot be combined")
	}

	sessionID := cmdutil.NewRuntimeID()
	cfg, err := eggclient.LoadConfigForEgg(sessionID)
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
	eggCfg, err := eggclient.LoadSpawnEggConfig(configPath, cwd, unsandboxed)
	if err != nil {
		return err
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

	// Handle --resume: restore chat history and get agent session ID
	var agentResumeID string
	if resumeID != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve user home: %w", err)
		}
		eggDir := filepath.Join(cfg.Dir, "eggs", resumeID)
		var restoreErr error
		agentResumeID, restoreErr = egg.RestoreSessionHistory(agentName, cwd, eggDir, home)
		if restoreErr != nil {
			return fmt.Errorf("restore session: %w", restoreErr)
		}
	}

	// Spawn egg as child process
	ec, err := eggclient.SpawnEgg(cfg, sessionID, agentName, eggCfg, uint32(rows), uint32(cols), cwd, false, false, trace, eggclient.EggIdentity{}, 0, eggclient.SpawnEggOpts{
		ResumeSessionID: agentResumeID, ResumeSourceSessionID: resumeID,
		Label: name, Kind: "agent", AgentArgs: agentArgs, PreserveEmptyAgentArgs: preserveEmptyAgentArgs,
	})
	if err != nil {
		return fmt.Errorf("spawn egg: %w", err)
	}
	if detach {
		if err := ec.Close(); err != nil {
			return fmt.Errorf("close egg client: %w", err)
		}
		if jsonOutput {
			return writeSessionJSON(map[string]any{
				"session": sessionID, "name": name, "kind": "agent", "agent": agentName,
				"agent_args": agentArgs, "cwd": cwd, "status": "started", "isolation": eggclient.SessionIsolationLabel(eggCfg),
			})
		}
		display := sessionID
		if name != "" {
			display = name + " (" + sessionID + ")"
		}
		fmt.Printf("started %s\n", display)
		return nil
	}
	defer cmdutil.CloseWithLog("egg client", ec)

	stream, err := ec.AttachSessionWithOptions(ctx, sessionID, egg.AttachOptions{Claim: true, Owner: "cli"})
	if err != nil {
		return fmt.Errorf("attach session: %w", err)
	}

	// Put terminal in raw mode
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			defer func() {
				if err := term.Restore(fd, oldState); err != nil {
					log.Printf("restore terminal: %v", err)
				}
			}()
		}
	}

	// Handle SIGWINCH for terminal resize
	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)

	go func() {
		for range winchCh {
			if w, h, err := term.GetSize(fd); err == nil {
				if err := ec.Resize(ctx, sessionID, uint32(h), uint32(w)); err != nil {
					log.Printf("resize session %s: %v", sessionID, err)
				}
			}
		}
	}()

	// Read output from egg → stdout
	exitCode := 0
	var outputErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := stream.Recv()
			if err != nil {
				outputErr = fmt.Errorf("terminal connection closed before session exit was confirmed: %w; reattach with session %s", err, sessionID)
				return
			}
			switch p := msg.Payload.(type) {
			case *pb.SessionMsg_Output:
				if _, err := os.Stdout.Write(p.Output); err != nil {
					outputErr = fmt.Errorf("write terminal output: %w", err)
					return
				}
			case *pb.SessionMsg_ExitCode:
				exitCode = int(p.ExitCode)
				return
			}
		}
	}()

	// Read stdin → egg input. Ctrl+B Q detaches the local client while the
	// setsid egg process and its agent keep running.
	detachCh := make(chan struct{}, 1)
	go func() {
		filter := &eggclient.AttachInputFilter{}
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				data, detach := filter.Filter(buf[:n])
				if len(data) > 0 {
					if sendErr := stream.Send(&pb.SessionMsg{
						SessionId: sessionID,
						Payload:   &pb.SessionMsg_Input{Input: data},
					}); sendErr != nil {
						return
					}
				}
				if detach {
					_ = stream.Send(&pb.SessionMsg{
						SessionId: sessionID,
						Payload:   &pb.SessionMsg_Detach{Detach: true},
					})
					detachCh <- struct{}{}
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	select {
	case <-detachCh:
		fmt.Fprintf(os.Stderr, "\r\n[detached from %s]\r\n", sessionID)
		return nil
	case <-done:
	}
	if outputErr != nil {
		return outputErr
	}

	if exitCode != 0 {
		// Dump egg.log so the user can see why the agent crashed
		logPath := filepath.Join(cfg.Dir, "eggs", sessionID, "egg.log")
		if logData, err := os.ReadFile(logPath); err == nil && len(logData) > 0 {
			if _, err := os.Stderr.Write(logData); err != nil {
				return errors.Join(fmt.Errorf("agent exited with code %d", exitCode), fmt.Errorf("write egg log: %w", err))
			}
		}
		return fmt.Errorf("agent exited with code %d", exitCode)
	}
	return nil
}
