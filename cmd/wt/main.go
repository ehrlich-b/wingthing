package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/skill"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/thread"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	// Fast path: re-exec'd as sandbox deny-path wrapper (Linux mount namespace).
	// Must run before cobra to avoid any overhead — this process execs immediately.
	if len(os.Args) > 1 && os.Args[1] == "_deny_init" {
		sandbox.DenyInit(os.Args[2:])
		return
	}

	root := newRootCommand()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		var exitErr *commandExitError
		if errors.As(err, &exitErr) {
			if exitErr.message != "" {
				_, _ = fmt.Fprintln(os.Stderr, exitErr.message)
			}
			os.Exit(exitErr.code)
		}
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "wt",
		Short:         "wingthing — an agent manager for agents",
		Long:          "An agent manager for agents: one typed control plane for durable agent runs and terminals across your machines, with human inspection and takeover when useful.",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	root.AddCommand(
		runCmd(),
		startCmd(),
		stopCmd(),
		timelineCmd(),
		threadCmd(),
		statusCmd(),
		logCmd(),
		agentCmd(),
		scheduleCmd(),
		retryCmd(),
		promptCmd(),
		initCmd(),
		loginCmd(),
		logoutCmd(),
		whoamiCmd(),
		supportCmd(),
		embedCmd(),
		doctorCmd(),
		serveCmd(),
		wingCmd(),
		roostCmd(),
		eggCmd(),
		terminalCmd(),
		attachCmd(),
		sessionCmd(),
		wingsCmd(),
		keygenCmd(),
		updateCmd(),
		toolCallCmd(),
		toolListCmd(),
		mcpCmd(),
		localCertCmd(),
	)
	return root
}

func startCmd() *cobra.Command {
	var debugFlag bool
	var auditFlag bool
	var orgFlag string
	var roostFlag string
	var allowFlags []string
	var pathsFlag string
	var localFlag bool
	var rawReplayFlag bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon (alias for wt wing start / wt daemon start)",
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, exeErr := os.Executable()
			if exeErr != nil {
				return exeErr
			}
			childArgs := []string{"wing", "start"}
			if roostFlag != "" {
				childArgs = append(childArgs, "--roost", roostFlag)
			}
			if orgFlag != "" {
				childArgs = append(childArgs, "--org", orgFlag)
			}
			for _, ak := range allowFlags {
				childArgs = append(childArgs, "--allow", ak)
			}
			if pathsFlag != "" {
				childArgs = append(childArgs, "--paths", pathsFlag)
			}
			if debugFlag {
				childArgs = append(childArgs, "--debug")
			}
			if auditFlag {
				childArgs = append(childArgs, "--audit")
			}
			if localFlag {
				childArgs = append(childArgs, "--local")
			}
			if rawReplayFlag {
				childArgs = append(childArgs, "--raw-replay")
			}
			child := exec.Command(exe, childArgs...)
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
			return child.Run()
		},
	}
	cmd.Flags().StringVar(&roostFlag, "roost", "", "roost server URL (default: config or wingthing.ai)")
	cmd.Flags().BoolVar(&debugFlag, "debug", false, "dump raw PTY output to /tmp/wt-pty-<session>.bin for each egg")
	cmd.Flags().StringVar(&orgFlag, "org", "", "org name or ID — share this wing with org members")
	cmd.Flags().StringSliceVar(&allowFlags, "allow", nil, "ephemeral passkey public key(s) for this session")
	cmd.Flags().StringVar(&pathsFlag, "paths", "", "comma-separated directories the wing can browse (default: ~/)")
	cmd.Flags().BoolVar(&auditFlag, "audit", false, "enable audit logging for all egg sessions")
	cmd.Flags().BoolVar(&localFlag, "local", false, "connect to localhost:8080 (for self-hosted wt serve)")
	cmd.Flags().BoolVar(&rawReplayFlag, "raw-replay", false, "use raw replay buffer for reconnect instead of VTerm snapshot")
	return cmd
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon (alias for wt wing stop / wt daemon stop)",
		RunE: func(cmd *cobra.Command, args []string) error {
			lifecycleLock, lockErr := acquireDaemonLifecycleLock()
			if lockErr != nil {
				return lockErr
			}
			defer closeWithLog("daemon lifecycle lock", lifecycleLock)
			pid, kind, err := readDaemon()
			if err != nil {
				return fmt.Errorf("no wing daemon running")
			}
			if err := stopDaemonAndWait(pid, kind, 5*time.Second); err != nil {
				return err
			}
			// Clean up both wing and roost pid/args files
			if err := removeFiles(wingPidPath(), wingArgsPath(), roostPidPath(), roostArgsPath()); err != nil {
				return fmt.Errorf("remove daemon metadata: %w", err)
			}
			fmt.Printf("wing daemon stopped (pid %d)\n", pid)
			return nil
		},
	}
}

func genTaskID() string {
	return fmt.Sprintf("t-%s-%s", time.Now().Format("20060102-150405"), newRuntimeID())
}

func runCmd() *cobra.Command {
	var skillFlag string
	var agentFlag string
	var afterFlag string
	var noRun bool
	var unsandboxed bool
	var configFlag string

	cmd := &cobra.Command{
		Use:   "run [prompt]",
		Short: "Run a prompt or skill",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && skillFlag == "" {
				return fmt.Errorf("provide a prompt or --skill flag")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer closeWithLog("store", s)

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			eggConfigYAML, err := resolveRunEggConfigYAML(configFlag, cwd, unsandboxed)
			if err != nil {
				return err
			}
			t := &store.Task{
				ID:            genTaskID(),
				RunAt:         time.Now().UTC(),
				CWD:           cwd,
				EggConfigYAML: eggConfigYAML,
			}
			if unsandboxed {
				t.Isolation = "privileged"
			}
			if skillFlag != "" {
				t.What = skillFlag
				t.Type = "skill"
				state, stErr := skill.LoadState(cfg.Dir)
				if stErr == nil && !state.IsEnabled(skillFlag) {
					return fmt.Errorf("skill %q is disabled — run: wt skill enable %s", skillFlag, skillFlag)
				}
				sk, skErr := skill.Load(filepath.Join(cfg.SkillsDir(), skillFlag+".md"))
				if skErr == nil && sk.Schedule != "" {
					t.Cron = &sk.Schedule
				}
			} else {
				t.What = args[0]
				t.Type = "prompt"
			}
			if agentFlag != "" {
				t.Agent = agentFlag
			}
			if afterFlag != "" {
				deps, _ := json.Marshal([]string{afterFlag})
				d := string(deps)
				t.DependsOn = &d
			}
			if err := s.CreateTask(t); err != nil {
				return fmt.Errorf("create task: %w", err)
			}
			fmt.Printf("submitted: %s (%s)\n", t.ID, t.What)

			if noRun {
				return nil
			}

			return runTask(cmd.Context(), cfg, s, t)
		},
	}
	cmd.Flags().StringVar(&skillFlag, "skill", "", "Run a named skill")
	cmd.Flags().StringVar(&agentFlag, "agent", "", "Use specific agent")
	cmd.Flags().StringVar(&afterFlag, "after", "", "Task ID this task depends on")
	cmd.Flags().StringVar(&configFlag, "config", "", "Path to egg config")
	cmd.Flags().BoolVar(&noRun, "no-run", false, "Submit task without running it")
	cmd.Flags().BoolVar(&unsandboxed, "unsandboxed", false, "trust the host boundary; run with the full authority of the local OS user")
	cmd.MarkFlagsMutuallyExclusive("config", "unsandboxed")
	return cmd
}

func resolveRunEggConfigYAML(configPath, cwd string, unsandboxed bool) (string, error) {
	if unsandboxed {
		if configPath != "" {
			return "", errors.New("--config and --unsandboxed cannot be combined")
		}
		return "", nil
	}
	eggCfg, err := loadSpawnEggConfig(configPath, cwd, false)
	if err != nil {
		return "", err
	}
	// wt run retains its established process-environment behavior until the
	// explicit agent_env boundary ships. The resolved task policy therefore
	// freezes filesystem, network, and resource controls while leaving env
	// absent; the direct runner applies its existing local/shared-host rules.
	runCfg := *eggCfg
	runCfg.Env = nil
	rendered, err := runCfg.YAML()
	if err != nil {
		return "", fmt.Errorf("render egg config: %w", err)
	}
	return rendered, nil
}

func taskEggConfig(t *store.Task, cwd string) (*egg.EggConfig, error) {
	if strings.TrimSpace(t.EggConfigYAML) == "" {
		return egg.DiscoverEggConfig(cwd, nil), nil
	}
	eggCfg, err := egg.LoadEggConfigFromYAML(t.EggConfigYAML)
	if err != nil {
		return nil, fmt.Errorf("load task egg config: %w", err)
	}
	return eggCfg, nil
}

func newAgent(name string) agent.Agent {
	switch name {
	case "ollama":
		return agent.NewOllama("", 0)
	case "gemini":
		return agent.NewGemini("", 0)
	case "hermes":
		return agent.NewHermes(0)
	case "codex":
		return agent.NewCodex(0)
	case "cursor":
		return agent.NewCursor(0)
	case "opencode":
		return agent.NewOpenCode(0)
	default:
		return agent.NewClaude(0)
	}
}

func runTask(ctx context.Context, cfg *config.Config, s *store.Store, t *store.Task) error {
	return runTaskTo(ctx, cfg, s, t, os.Stdout)
}

func runTaskTo(ctx context.Context, cfg *config.Config, s *store.Store, t *store.Task, destination io.Writer) error {
	return runTaskToWithOptions(ctx, cfg, s, t, destination, taskRunOptions{})
}

type taskRunOptions struct {
	UserHome     string
	SharedHost   bool
	AllowedPaths []string
}

func runTaskToWithOptions(ctx context.Context, cfg *config.Config, s *store.Store, t *store.Task, destination io.Writer, options taskRunOptions) (runErr error) {
	if err := s.UpdateTaskStatus(t.ID, "running"); err != nil {
		return fmt.Errorf("mark task running: %w", err)
	}
	defer func() {
		if runErr == nil {
			return
		}
		if err := s.SetTaskError(t.ID, runErr.Error()); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("record task failure: %w", err))
		}
	}()
	if err := s.AppendLog(t.ID, "started", nil); err != nil {
		return fmt.Errorf("record task start: %w", err)
	}
	if options.SharedHost && runtime.GOOS != "linux" {
		err := errors.New("shared-host credential isolation requires the Linux filesystem jail")
		return err
	}
	if options.SharedHost {
		_, canonical, err := sharedHostFilesystemRules(cfg, options.AllowedPaths)
		if err != nil {
			return err
		}
		options.AllowedPaths = canonical
	}

	// Pre-create all agents so the builder can look up any agent's context window
	agents := make(map[string]agent.Agent)
	for _, definition := range agent.Definitions() {
		agents[definition.Name] = newAgent(definition.Name)
	}
	mem := memory.New(cfg.MemoryDir())

	builder := &orchestrator.Builder{
		Store:  s,
		Memory: mem,
		Config: cfg,
		Agents: agents,
	}

	pr, err := builder.Build(ctx, t.ID)
	if err != nil {
		return fmt.Errorf("build prompt: %w", err)
	}
	if err := s.SetTaskResolved(t.ID, pr.Agent, pr.Isolation); err != nil {
		return fmt.Errorf("record resolved task: %w", err)
	}
	t.Agent = pr.Agent
	t.Isolation = pr.Isolation

	promptDetail := pr.Prompt
	if err := s.AppendLog(t.ID, "prompt_built", &promptDetail); err != nil {
		return fmt.Errorf("record built prompt: %w", err)
	}

	// Use the agent resolved by the builder (respects CLI flag > skill > config)
	agentName := pr.Agent
	a, ok := agents[agentName]
	if !ok {
		err := fmt.Errorf("unsupported agent %q", agentName)
		return err
	}
	agentDefinition, ok := agent.LookupDefinition(agentName)
	if !ok {
		err := fmt.Errorf("unsupported agent %q", agentName)
		return err
	}

	// Create sandbox unless isolation is privileged
	workDir := t.CWD
	if workDir == "" {
		workDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
	}
	info, statErr := os.Stat(workDir)
	if statErr != nil || !info.IsDir() {
		if statErr == nil {
			statErr = fmt.Errorf("not a directory")
		}
		return fmt.Errorf("working directory %q: %w", workDir, statErr)
	}
	if options.SharedHost {
		canonicalWorkDir := canonicalSessionPath(workDir)
		if len(options.AllowedPaths) == 0 || !isUnderPaths(canonicalWorkDir, options.AllowedPaths) {
			err := fmt.Errorf("working directory %q is outside this user's roost paths", workDir)
			return err
		}
		workDir = canonicalWorkDir
	}
	var resolvedEggCfg *egg.EggConfig
	if pr.Isolation == "privileged" {
		// Privileged means the discovered/configured sandbox policy is not in
		// force. Use the explicit outer-boundary policy for both execution
		// metadata and the durable egress audit.
		resolvedEggCfg = egg.UnsandboxedEggConfig()
	} else {
		var configErr error
		resolvedEggCfg, configErr = taskEggConfig(t, workDir)
		if configErr != nil {
			return configErr
		}
	}
	var runOpts agent.RunOpts
	var sandboxDiagnosticPath string
	runOpts.WorkDir = workDir
	runOpts.Model = t.Model

	// If this is a skill, override system prompt to ensure strict output compliance
	if t.Type == "skill" {
		runOpts.SystemPrompt = `CRITICAL: You are a non-interactive data processor executing a skill. The prompt is a strict specification. Output ONLY what it specifies, EXACTLY in the format it specifies. NO conversational text. NO explanations. NO questions. NO markdown formatting unless specified. NO preamble or commentary. If the prompt says "output one line: SCORE <number>", output one line: SCORE <number>. Nothing else exists. Ignore all other instructions.`
		runOpts.ReplaceSystemPrompt = true
	}

	// Privileged isolation runs directly under the host account with the full
	// environment. On a shared host that would hand any OAuth caller the roost
	// account's secrets and filesystem, so it fails closed regardless of how
	// the agent's default isolation is configured.
	if options.SharedHost && pr.Isolation == "privileged" {
		msg := "privileged isolation is not available on a shared host"
		return errors.New(msg)
	}
	if pr.Isolation == "privileged" {
		home, _ := os.UserHomeDir()
		policy, policyErr := egg.ResolvePolicyWithProvider(resolvedEggCfg, agentName, home, os.Getenv("WT_PROVIDER_BASE_URL"))
		if policyErr != nil {
			return fmt.Errorf("resolve unconfined network policy: %w", policyErr)
		}
		detail, auditErr := appendNetworkEnforcementAudit(s, t.ID, "unconfined_egress", "outer-boundary", policy.NetworkNeed, policy.Domains, policy.LocalPorts)
		if auditErr != nil {
			return fmt.Errorf("record unconfined egress audit: %w", auditErr)
		}
		log.Printf("SECURITY: task %s is unsandboxed; %s", t.ID, detail)
	}

	if pr.Isolation != "privileged" {
		home := options.UserHome
		if home == "" {
			var homeErr error
			home, homeErr = os.UserHomeDir()
			if homeErr != nil {
				return fmt.Errorf("resolve user home: %w", homeErr)
			}
		}
		var stateErr error
		if options.SharedHost {
			profile := egg.Profile(agentName)
			dirs := append(append([]string(nil), profile.WriteRegex...), profile.WriteDirs...)
			dirs = append(dirs, filepath.Join(".local", "bin"))
			stateErr = prepareSharedAgentHome(home, dirs)
		} else {
			stateErr = prepareDirectAgentState(agentName, home)
		}
		if stateErr != nil {
			return fmt.Errorf("prepare %s state: %w", agentName, stateErr)
		}
		if options.SharedHost {
			agentBin, lookupErr := exec.LookPath(agentDefinition.Command)
			if lookupErr != nil {
				return fmt.Errorf("find shared-host %s runtime: %w", agentDefinition.Command, lookupErr)
			}
			if installErr := installSharedAgentBinary(agentBin, home, agentDefinition.Command); installErr != nil {
				return fmt.Errorf("prepare shared-host %s runtime: %w", agentName, installErr)
			}
			// Shared-host tasks intentionally drop ambient provider credentials.
			// Give Claude the same file-backed helper used by interactive org
			// sessions so the secret never enters the agent environment.
			if err := setupAPIKeyHelper(agentName, map[string]string{}, home); err != nil {
				return fmt.Errorf("prepare shared-host credential helper: %w", err)
			}
		}

		mountPaths := taskSandboxMountPaths(pr.Mounts, workDir, options)
		sbCfg, policyErr := directAgentSandboxConfigForTask(resolvedEggCfg, agentName, pr.Isolation, home, workDir, mountPaths, options.SharedHost)
		if policyErr != nil {
			return fmt.Errorf("resolve sandbox network policy: %w", policyErr)
		}
		sbCfg.SessionID = t.ID
		domainProxy, proxyErr := sandbox.StartPolicyProxyWithMode(sbCfg.NetworkNeed, sbCfg.Domains, sbCfg.NetworkMode)
		if proxyErr != nil {
			detail := proxyErr.Error()
			if err := s.AppendLog(t.ID, "domain_proxy_unavailable", &detail); err != nil {
				return errors.Join(fmt.Errorf("start enforcing network proxy: %w", proxyErr), fmt.Errorf("record proxy failure: %w", err))
			}
			return fmt.Errorf("start enforcing network proxy: %w", proxyErr)
		}
		if domainProxy != nil {
			defer domainProxy.Close()
			sbCfg.ProxyPort = domainProxy.Port()
		}
		detail, auditErr := appendNetworkEnforcementAudit(s, t.ID, "sandbox_enforcement", explainEnforcement(sbCfg.NetworkNeed, runtime.GOOS, sbCfg.NetworkMode), sbCfg.NetworkNeed, sbCfg.Domains, sbCfg.LocalPorts)
		if auditErr != nil {
			return fmt.Errorf("record sandbox enforcement audit: %w", auditErr)
		}
		log.Printf("task %s sandbox: %s", t.ID, detail)

		sb, sbErr := sandbox.New(sbCfg)
		if sbErr != nil {
			return fmt.Errorf("create sandbox: %w", sbErr)
		}
		defer func() {
			if err := sb.Destroy(); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("destroy sandbox: %w", err))
			}
		}()
		sandboxDiagnosticPath = sb.DiagLog()
		agentEnv := directAgentEnvWithPolicy(agentName, home, sbCfg.ProxyPort, !options.SharedHost)
		runOpts.CmdFactory = func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			executable, resolveErr := sandboxAgentExecutable(name, home, options.SharedHost)
			if resolveErr != nil {
				return nil, resolveErr
			}
			cmd, execErr := sb.Exec(ctx, executable, args)
			if execErr != nil {
				return nil, execErr
			}
			cmd.Env = agentEnv
			return cmd, nil
		}
	}

	runCtx := ctx
	var cancel context.CancelFunc
	timeout := pr.Timeout
	if t.TimeoutSeconds > 0 {
		timeout = time.Duration(t.TimeoutSeconds) * time.Second
	}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	stream, err := a.Run(runCtx, pr.Prompt, runOpts)
	if err != nil {
		return fmt.Errorf("run agent: %w", err)
	}

	// Stream output to stdout
	for {
		chunk, ok := stream.Next()
		if !ok {
			break
		}
		if _, err := fmt.Fprint(destination, chunk.Text); err != nil {
			return fmt.Errorf("write agent output: %w", err)
		}
	}
	if _, err := fmt.Fprintln(destination); err != nil {
		return fmt.Errorf("finish agent output: %w", err)
	}

	if err := stream.Err(); err != nil {
		diagnostics := mergeAgentFailureDiagnostics(err, readSandboxDiagnostics(sandboxDiagnosticPath))
		if outputErr := s.SetTaskOutput(t.ID, mergeAgentFailureOutput(stream.Text(), diagnostics)); outputErr != nil {
			return errors.Join(fmt.Errorf("agent error: %w", err), fmt.Errorf("record failed agent output: %w", outputErr))
		}
		if diagnostics != "" {
			if _, writeErr := fmt.Fprintln(destination, diagnostics); writeErr != nil {
				return errors.Join(fmt.Errorf("agent error: %w", err), fmt.Errorf("write agent diagnostics: %w", writeErr))
			}
		}
		return fmt.Errorf("agent error: %w", err)
	}
	if err := runCtx.Err(); err != nil {
		return fmt.Errorf("agent run ended after cancellation: %w", err)
	}

	// Store result
	output := stream.Text()
	if err := s.SetTaskOutput(t.ID, output); err != nil {
		return fmt.Errorf("record task output: %w", err)
	}
	if err := s.UpdateTaskStatus(t.ID, "done"); err != nil {
		return fmt.Errorf("mark task done: %w", err)
	}
	if err := s.AppendLog(t.ID, "done", nil); err != nil {
		return fmt.Errorf("record task completion: %w", err)
	}

	// Record tokens in thread
	inputTok, outputTok := stream.Tokens()
	totalTok := inputTok + outputTok
	if totalTok > 0 {
		if err := s.AppendThread(&store.ThreadEntry{
			TaskID:     &t.ID,
			WingID:     cfg.WingID,
			Agent:      &agentName,
			UserInput:  &t.What,
			Summary:    truncate(output, 200),
			TokensUsed: &totalTok,
		}); err != nil {
			return fmt.Errorf("record task thread entry: %w", err)
		}
	}

	return nil
}

func networkEnforcementDetail(enforcement string, need sandbox.NetworkNeed, domains []string, localPorts []int) string {
	return fmt.Sprintf("network=%s enforcement=%s domains=%d local_ports=%v", need, enforcement, len(domains), localPorts)
}

func appendNetworkEnforcementAudit(s *store.Store, taskID, event, enforcement string, need sandbox.NetworkNeed, domains []string, localPorts []int) (string, error) {
	detail := networkEnforcementDetail(enforcement, need, domains, localPorts)
	return detail, s.AppendLog(taskID, event, &detail)
}

func taskSandboxMountPaths(promptMounts []string, workDir string, options taskRunOptions) []string {
	if options.SharedHost {
		return append([]string(nil), options.AllowedPaths...)
	}
	mounts := append([]string(nil), promptMounts...)
	return append(mounts, workDir)
}

const maxSandboxDiagnostics = 64 * 1024

func readSandboxDiagnostics(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > maxSandboxDiagnostics {
		data = data[len(data)-maxSandboxDiagnostics:]
	}
	return strings.TrimSpace(string(data))
}

func mergeAgentFailureDiagnostics(runErr error, sandboxDiagnostics string) string {
	var sections []string
	if runErr != nil && strings.TrimSpace(runErr.Error()) != "" {
		sections = append(sections, strings.TrimSpace(runErr.Error()))
	}
	sandboxDiagnostics = strings.TrimSpace(sandboxDiagnostics)
	if sandboxDiagnostics != "" {
		sections = append(sections, sandboxDiagnostics)
	}
	return strings.Join(sections, "\n")
}

func mergeAgentFailureOutput(stdout, diagnostics string) string {
	diagnostics = strings.TrimSpace(diagnostics)
	if diagnostics == "" {
		return stdout
	}
	if strings.TrimSpace(stdout) == "" {
		return diagnostics
	}
	if !strings.HasSuffix(stdout, "\n") {
		stdout += "\n"
	}
	return stdout + diagnostics
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func timelineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "timeline",
		Short: "Show upcoming and recent tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer closeWithLog("store", s)

			tasks, err := s.ListRecent(20)
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				fmt.Println("no tasks")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tSTATUS\tAGENT\tWHAT\tRUN AT"); err != nil {
				return err
			}
			for _, t := range tasks {
				what := t.What
				if len(what) > 50 {
					what = what[:47] + "..."
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, t.Agent, what, t.RunAt.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
}

func threadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thread",
		Short: "Print today's daily thread",
		RunE: func(cmd *cobra.Command, args []string) error {
			yesterday, _ := cmd.Flags().GetBool("yesterday")
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer closeWithLog("store", s)

			date := time.Now().UTC()
			if yesterday {
				date = date.AddDate(0, 0, -1)
			}
			rendered, err := thread.RenderDay(s, date, 0)
			if err != nil {
				return err
			}
			if rendered == "" {
				fmt.Println("(empty thread)")
				return nil
			}
			fmt.Print(rendered)
			return nil
		},
	}
	cmd.Flags().Bool("yesterday", false, "Show yesterday's thread")
	return cmd
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Task counts and token usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer closeWithLog("store", s)

			var pending, running int
			if err := s.DB().QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'pending'").Scan(&pending); err != nil {
				return fmt.Errorf("count pending tasks: %w", err)
			}
			if err := s.DB().QueryRow("SELECT COUNT(*) FROM tasks WHERE status = 'running'").Scan(&running); err != nil {
				return fmt.Errorf("count running tasks: %w", err)
			}
			agents, err := s.ListAgents()
			if err != nil {
				return fmt.Errorf("list agents: %w", err)
			}

			now := time.Now().UTC()
			todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			weekStart := todayStart.AddDate(0, 0, -6)
			tomorrow := todayStart.AddDate(0, 0, 1)

			tokensToday, err := s.SumTokensByDateRange(todayStart, tomorrow)
			if err != nil {
				return fmt.Errorf("sum today's tokens: %w", err)
			}
			tokensWeek, err := s.SumTokensByDateRange(weekStart, tomorrow)
			if err != nil {
				return fmt.Errorf("sum weekly tokens: %w", err)
			}

			fmt.Printf("pending: %d\nrunning: %d\nagents:  %d\ntokens:  %d today / %d this week\n", pending, running, len(agents), tokensToday, tokensWeek)
			return nil
		},
	}
}

func logCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log [taskId]",
		Short: "Show task log events",
		RunE: func(cmd *cobra.Command, args []string) error {
			last, _ := cmd.Flags().GetBool("last")
			showContext, _ := cmd.Flags().GetBool("context")

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer closeWithLog("store", s)

			taskID := ""
			if len(args) > 0 {
				taskID = args[0]
			} else if last {
				tasks, err := s.ListRecent(1)
				if err != nil {
					return err
				}
				if len(tasks) == 0 {
					fmt.Println("no tasks")
					return nil
				}
				taskID = tasks[0].ID
			} else {
				return fmt.Errorf("provide a task ID or use --last")
			}

			entries, err := s.ListLogByTask(taskID)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if showContext && e.Event == "prompt_built" && e.Detail != nil {
					fmt.Println(*e.Detail)
					return nil
				}
				detail := ""
				if e.Detail != nil {
					detail = *e.Detail
					if len(detail) > 80 {
						detail = detail[:77] + "..."
					}
				}
				fmt.Printf("%s  %s  %s\n", e.Timestamp.Format(time.RFC3339), e.Event, detail)
			}
			return nil
		},
	}
	cmd.Flags().Bool("last", false, "Show most recent task")
	cmd.Flags().Bool("context", false, "Show full prompt for prompt_built event")
	return cmd
}

func agentCmd() *cobra.Command {
	ag := &cobra.Command{
		Use:   "agent",
		Short: "Manage agent adapters",
	}
	ag.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer closeWithLog("store", s)

			agents, err := s.ListAgents()
			if err != nil {
				return err
			}
			if len(agents) == 0 {
				fmt.Println("no agents configured")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "NAME\tADAPTER\tHEALTHY\tCONTEXT"); err != nil {
				return err
			}
			for _, a := range agents {
				healthy := "no"
				if a.Healthy {
					healthy = "yes"
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", a.Name, a.Adapter, healthy, a.ContextWindow); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	})
	return ag
}

func scheduleCmd() *cobra.Command {
	sc := &cobra.Command{
		Use:   "schedule",
		Short: "Manage recurring tasks",
	}
	sc.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List recurring tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer closeWithLog("store", s)

			tasks, err := s.ListRecurring()
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				fmt.Println("no recurring tasks")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tSTATUS\tCRON\tWHAT\tNEXT RUN"); err != nil {
				return err
			}
			for _, t := range tasks {
				what := t.What
				if len(what) > 40 {
					what = what[:37] + "..."
				}
				cronExpr := ""
				if t.Cron != nil {
					cronExpr = *t.Cron
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, cronExpr, what, t.RunAt.Format(time.RFC3339)); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	})
	sc.AddCommand(&cobra.Command{
		Use:   "remove [id]",
		Short: "Remove cron schedule from a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer closeWithLog("store", s)

			t, err := s.GetTask(args[0])
			if err != nil {
				return err
			}
			if t == nil {
				return fmt.Errorf("task not found: %s", args[0])
			}
			if err := s.ClearTaskCron(args[0]); err != nil {
				return err
			}
			fmt.Printf("removed schedule from %s (%s)\n", t.ID, t.What)
			return nil
		},
	})
	return sc
}

func retryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry [task-id]",
		Short: "Retry a failed task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer closeWithLog("store", s)

			t, err := s.GetTask(args[0])
			if err != nil {
				return err
			}
			if t == nil {
				return fmt.Errorf("task not found: %s", args[0])
			}
			if t.Status != "failed" {
				return fmt.Errorf("only failed tasks can be retried (status: %s)", t.Status)
			}

			newTask := &store.Task{
				ID:             genTaskID(),
				Type:           t.Type,
				What:           t.What,
				RunAt:          time.Now().UTC(),
				Agent:          t.Agent,
				Model:          t.Model,
				TimeoutSeconds: t.TimeoutSeconds,
				Isolation:      t.Isolation,
				Memory:         t.Memory,
				Cron:           t.Cron,
				ParentID:       &t.ID,
				Status:         "pending",
				MaxRetries:     t.MaxRetries,
				CWD:            t.CWD,
				PromptName:     t.PromptName,
				PromptRevision: t.PromptRevision,
				Principal:      t.Principal,
				EggConfigYAML:  t.EggConfigYAML,
			}
			if err := s.CreateTask(newTask); err != nil {
				return err
			}
			fmt.Printf("retried: %s\n", newTask.ID)
			return nil
		},
	}
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize ~/.wingthing directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			dirs := []string{cfg.Dir, cfg.MemoryDir(), cfg.SkillsDir()}
			for _, d := range dirs {
				if err := os.MkdirAll(d, 0755); err != nil {
					return fmt.Errorf("create %s: %w", d, err)
				}
			}

			// Seed index.md
			indexPath := filepath.Join(cfg.MemoryDir(), "index.md")
			if _, err := os.Stat(indexPath); errors.Is(err, os.ErrNotExist) {
				if err := os.WriteFile(indexPath, []byte("# Memory Index\n\nThis file is always loaded into every prompt.\n"), 0644); err != nil {
					return fmt.Errorf("seed memory index: %w", err)
				}
			} else if err != nil {
				return fmt.Errorf("inspect memory index: %w", err)
			}

			// Seed identity.md
			idPath := filepath.Join(cfg.MemoryDir(), "identity.md")
			if _, err := os.Stat(idPath); errors.Is(err, os.ErrNotExist) {
				if err := os.WriteFile(idPath, []byte("---\nname: \"\"\n---\n# Identity\n\nEdit this file with your name, role, and preferences.\n"), 0644); err != nil {
					return fmt.Errorf("seed identity: %w", err)
				}
			} else if err != nil {
				return fmt.Errorf("inspect identity: %w", err)
			}

			// Init database
			s, err := store.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("init db: %w", err)
			}
			if err := s.Close(); err != nil {
				return fmt.Errorf("close initialized database: %w", err)
			}

			// Detect agents
			fmt.Println("initialized:", cfg.Dir)
			fmt.Println("  memory:", cfg.MemoryDir())
			fmt.Println("  skills:", cfg.SkillsDir())
			fmt.Println("  db:", cfg.DBPath())

			for _, definition := range agent.Definitions() {
				if _, err := exec.LookPath(definition.Command); err == nil {
					fmt.Printf("  agent found: %s\n", definition.Name)
				}
			}

			return nil
		},
	}
}

func loginCmd() *cobra.Command {
	var roostFlag string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate this device with the roost",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if roostFlag != "" {
				cfg.RoostURL = roostFlag
			}

			ts := auth.NewTokenStore(cfg.Dir)

			existing, err := ts.Load()
			if err != nil {
				return err
			}
			reusable, err := reusableLoginForTarget(ts, existing, roostFlag, auth.ValidateTokenRemote)
			if err != nil {
				return err
			}
			if reusable {
				fmt.Println("already logged in")
				return nil
			}
			if ts.IsValid(existing) && roostFlag != "" {
				fmt.Println("existing login is not accepted by the requested roost; starting a new device login")
			}

			if cfg.RoostURL == "" {
				cfg.RoostURL = "https://wingthing.ai"
			}

			// Generate or load X25519 keypair for E2E encryption
			pubKeyB64, err := auth.EnsureKeyPair(cfg.Dir)
			if err != nil {
				return fmt.Errorf("keypair: %w", err)
			}

			dcr, err := auth.RequestDeviceCode(cfg.RoostURL, cfg.WingID, pubKeyB64)
			if err != nil {
				return err
			}

			fmt.Printf("Visit: %s\n", dcr.VerificationURL)

			// Opening the browser is a convenience; the printed URL remains usable.
			switch runtime.GOOS {
			case "darwin":
				if err := exec.Command("open", dcr.VerificationURL).Start(); err != nil {
					log.Printf("open login URL: %v", err)
				}
			case "linux":
				if err := exec.Command("xdg-open", dcr.VerificationURL).Start(); err != nil {
					log.Printf("open login URL: %v", err)
				}
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			tr, err := auth.PollForToken(ctx, cfg.RoostURL, dcr.DeviceCode, dcr.Interval)
			if err != nil {
				return err
			}

			token := &auth.DeviceToken{
				Token:     tr.Token,
				ExpiresAt: tr.ExpiresAt,
				IssuedAt:  time.Now().Unix(),
				DeviceID:  cfg.WingID,
				PublicKey: pubKeyB64,
			}
			if err := ts.Save(token); err != nil {
				return err
			}

			if tr.DisplayName != "" || tr.Email != "" {
				info := &auth.UserInfo{DisplayName: tr.DisplayName, Email: tr.Email, Provider: tr.Provider}
				fmt.Printf("logged in as %s\n", formatUserIdentity(info))
			} else {
				fmt.Println("logged in successfully")
			}

			// If a daemon was running, restart it so it picks up the new token
			if err := restartWingDaemonIfRunning(); err != nil {
				fmt.Printf("warning: failed to restart wing daemon: %v\n", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&roostFlag, "roost", "", "roost URL (default: config or wingthing.ai)")
	return cmd
}

func reusableLoginForTarget(store *auth.TokenStore, existing *auth.DeviceToken, explicitTarget string, validate func(string, string) error) (bool, error) {
	if !store.IsValid(existing) {
		return false, nil
	}
	if explicitTarget == "" {
		return true, nil
	}
	target := strings.TrimRight(explicitTarget, "/")
	if err := validate(target, existing.Token); err != nil {
		if errors.Is(err, auth.ErrAuthFailed) {
			return false, nil
		}
		return false, fmt.Errorf("verify existing login with requested roost: %w", err)
	}
	return true, nil
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove device authentication",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			lifecycleLock, lockErr := acquireDaemonLifecycleLock()
			if lockErr != nil {
				return lockErr
			}
			defer closeWithLog("daemon lifecycle lock", lifecycleLock)

			// Stop wing daemon if running (prevents orphaned daemon with revoked token)
			if pid, kind, pidErr := readDaemon(); pidErr == nil {
				fmt.Printf("stopping wing daemon (pid %d)...\n", pid)
				if stopErr := stopDaemonAndWait(pid, kind, 5*time.Second); stopErr != nil {
					return fmt.Errorf("refusing to delete login while daemon is still running: %w", stopErr)
				}
				if err := removeFiles(wingPidPath(), wingArgsPath(), roostPidPath(), roostArgsPath()); err != nil {
					return fmt.Errorf("remove stopped daemon metadata: %w", err)
				}
			}

			ts := auth.NewTokenStore(cfg.Dir)
			if err := ts.Delete(); err != nil {
				return err
			}

			fmt.Println("logged out")
			return nil
		},
	}
}

// restartWingDaemonIfRunning stops the running wing daemon and starts a new one
// with the same args so it picks up the new auth token.
func restartWingDaemonIfRunning() error {
	lifecycleLock, lockErr := acquireDaemonLifecycleLock()
	if lockErr != nil {
		return lockErr
	}
	defer closeWithLog("daemon lifecycle lock", lifecycleLock)

	pid, err := readPidFrom(wingPidPath(), wingDaemon)
	if err != nil {
		if daemonAbsentError(err) {
			return nil // no standalone wing daemon running, nothing to do
		}
		return fmt.Errorf("inspect wing daemon state: %w", err)
	}

	// Read saved args so we can restart with same flags
	argsData, err := os.ReadFile(wingArgsPath())
	if err != nil {
		return fmt.Errorf("can't read wing.args (stop and restart manually: wt stop && wt start): %w", err)
	}
	savedArgs, err := parseSavedDaemonArgs(argsData, wingDaemon)
	if err != nil {
		return fmt.Errorf("can't use wing.args (stop and restart manually: wt stop && wt start): %w", err)
	}

	// Stop the old daemon
	fmt.Printf("restarting wing daemon (pid %d)...\n", pid)
	if err := stopDaemonAndWait(pid, wingDaemon, 5*time.Second); err != nil {
		return fmt.Errorf("refusing to start a competing daemon: %w", err)
	}
	if err := removeFiles(wingPidPath(), wingArgsPath(), wingStatusPath()); err != nil {
		return fmt.Errorf("remove stopped daemon metadata: %w", err)
	}

	// Start new daemon with same args
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	if err := rotateLog(wingLogPath()); err != nil {
		return err
	}
	logFile, err := os.OpenFile(wingLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		closeWithLog("wing log", logFile)
		return fmt.Errorf("resolve user home: %w", err)
	}
	child := exec.Command(exe, savedArgs...)
	child.Dir = home
	child.Stdout = logFile
	child.Stderr = logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		closeWithLog("wing log", logFile)
		return fmt.Errorf("start daemon: %w", err)
	}
	closeWithLog("wing log", logFile)

	if err := writeDaemonMetadata(wingPidPath(), wingArgsPath(), child.Process.Pid, savedArgs); err != nil {
		abandonStartedDaemon(child)
		return fmt.Errorf("restart daemon: %w", err)
	}

	result := waitForWingStatus(child.Process.Pid, 5*time.Second)
	switch result {
	case "connected":
		fmt.Printf("wing daemon restarted (pid %d)\n", child.Process.Pid)
		fmt.Printf("  relay: connected\n")
	case "auth_failed":
		abandonStartedDaemon(child)
		if err := removeFiles(wingPidPath(), wingArgsPath(), wingStatusPath()); err != nil {
			return errors.Join(fmt.Errorf("wing daemon restarted but auth failed — run: wt logout && wt login"), fmt.Errorf("remove failed daemon metadata: %w", err))
		}
		return fmt.Errorf("wing daemon restarted but auth failed — run: wt logout && wt login")
	default:
		fmt.Printf("wing daemon restarted (pid %d)\n", child.Process.Pid)
		fmt.Printf("  relay: connecting...\n")
	}
	if err := child.Process.Release(); err != nil {
		log.Printf("warning: failed to release restarted daemon process handle: %v", err)
	}
	return nil
}

// resolveRelayHTTPURL returns the relay's HTTP base URL from config.
func resolveRelayHTTPURL(cfg *config.Config) string {
	relayURL := cfg.RoostURL
	if relayURL == "" {
		if wc, err := config.LoadWingConfig(cfg.Dir); err == nil && wc.Roost != "" {
			relayURL = wc.Roost
		}
	}
	if relayURL == "" {
		relayURL = "https://ws.wingthing.ai"
	}
	return normalizeRelayHTTPURL(relayURL)
}

// normalizeRelayHTTPURL converts a wing/coordinator URL to an HTTP base URL.
func normalizeRelayHTTPURL(relayURL string) string {
	if relayURL == "" {
		return ""
	}
	relayURL = strings.TrimRight(relayURL, "/")
	relayURL = strings.Replace(relayURL, "wss://", "https://", 1)
	relayURL = strings.Replace(relayURL, "ws://", "http://", 1)
	if !strings.HasPrefix(relayURL, "http://") && !strings.HasPrefix(relayURL, "https://") {
		relayURL = "https://" + relayURL
	}
	return relayURL
}

// relayMetadataURL removes URL components that must not be persisted or copied
// into support bundles. Coordinator API routing can retain a path prefix, but
// userinfo, queries, and fragments are never part of the coordinator identity.
func relayMetadataURL(relayURL string) string {
	normalized := normalizeRelayHTTPURL(relayURL)
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

// resolveWingRelayHTTPURL mirrors the daemon's precedence: explicit flag,
// wing.yaml, local default, config.yaml, then the hosted coordinator.
func resolveWingRelayHTTPURL(cfg *config.Config, explicit string, local bool) string {
	relayURL := explicit
	if relayURL == "" && cfg != nil {
		if wingCfg, err := config.LoadWingConfig(cfg.Dir); err == nil {
			relayURL = wingCfg.Roost
		}
	}
	if relayURL == "" && local {
		relayURL = "http://localhost:8080"
	}
	if relayURL == "" && cfg != nil {
		relayURL = cfg.RoostURL
	}
	if relayURL == "" {
		relayURL = "https://ws.wingthing.ai"
	}
	return normalizeRelayHTTPURL(relayURL)
}

// roostBrowserURL returns the UI served by the selected coordinator. The
// public service has split ws/app hosts; a self-hosted roost serves its app at
// /app/ on the same origin.
func roostBrowserURL(roostURL string) string {
	httpURL := normalizeRelayHTTPURL(roostURL)
	parsed, err := url.Parse(httpURL)
	if err != nil || parsed.Hostname() == "" {
		return httpURL
	}
	// The browser destination is display output. Never echo URL credentials,
	// even if a caller supplied a credentialed coordinator URL.
	parsed.User = nil
	switch strings.ToLower(parsed.Hostname()) {
	case "ws.wingthing.ai", "wingthing.ai", "app.wingthing.ai":
		parsed.Scheme = "https"
		parsed.Host = "app.wingthing.ai"
		parsed.Path = "/"
	default:
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/app/"
	}
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func wingRoostFlags(args []string) (roost string, local bool) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--local":
			local = true
		case arg == "--roost" && index+1 < len(args):
			index++
			roost = args[index]
		case strings.HasPrefix(arg, "--roost="):
			roost = strings.TrimPrefix(arg, "--roost=")
		}
	}
	return roost, local
}

// activeWingRelayHTTPURL uses the exact coordinator recorded by a new daemon,
// then falls back to saved launch args for an already-running older daemon.
func activeWingRelayHTTPURL(cfg *config.Config, status *wingStatus) string {
	if status != nil && status.RoostURL != "" {
		if relayURL := relayMetadataURL(status.RoostURL); relayURL != "" {
			return relayURL
		}
	}
	if data, err := os.ReadFile(wingArgsPath()); err == nil {
		if args, parseErr := parseSavedDaemonArgs(data, wingDaemon); parseErr == nil {
			roost, local := wingRoostFlags(args)
			return resolveWingRelayHTTPURL(cfg, roost, local)
		}
	}
	return resolveWingRelayHTTPURL(cfg, "", false)
}

// formatUserIdentity formats a user identity string from auth.UserInfo.
func formatUserIdentity(info *auth.UserInfo) string {
	identity := info.DisplayName
	if info.Email != "" {
		if identity != "" {
			identity += " (" + info.Email + ")"
		} else {
			identity = info.Email
		}
	}
	if info.Provider != "" {
		identity += " via " + info.Provider
	}
	if identity == "" {
		identity = info.UserID
	}
	return identity
}

func whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the currently logged-in user",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			ts := auth.NewTokenStore(cfg.Dir)
			tok, err := ts.Load()
			if err != nil {
				return err
			}
			if !ts.IsValid(tok) {
				return fmt.Errorf("not logged in — run: wt login")
			}

			relayURL := resolveRelayHTTPURL(cfg)
			info, err := auth.FetchUserInfo(relayURL, tok.Token)
			if err != nil {
				if errors.Is(err, auth.ErrAuthFailed) {
					return fmt.Errorf("token expired — run: wt logout && wt login")
				}
				return fmt.Errorf("relay: %w", err)
			}

			fmt.Println(formatUserIdentity(info))
			fmt.Printf("  wing_id: %s\n", cfg.WingID)
			return nil
		},
	}
}

func supportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "support",
		Short: "Collect diagnostic bundle for troubleshooting",
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			ts := time.Now().Format("20060102-150405")
			zipPath := filepath.Join(os.TempDir(), fmt.Sprintf("wt-support-%s.zip", ts))
			f, err := os.Create(zipPath)
			if err != nil {
				return fmt.Errorf("create zip: %w", err)
			}
			defer func() {
				if err := f.Close(); err != nil {
					runErr = errors.Join(runErr, fmt.Errorf("close support bundle: %w", err))
				}
			}()
			zw := zip.NewWriter(f)
			defer func() {
				if err := zw.Close(); err != nil {
					runErr = errors.Join(runErr, fmt.Errorf("finalize support bundle: %w", err))
				}
			}()

			// meta.json
			hostname, _ := os.Hostname()
			meta := map[string]any{
				"wing_id":   cfg.WingID,
				"hostname":  hostname,
				"platform":  runtime.GOOS,
				"version":   version,
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			}
			if pid, pidErr := readPid(); pidErr == nil {
				meta["daemon_pid"] = pid
			}
			tok, _ := auth.NewTokenStore(cfg.Dir).Load()
			if tok != nil {
				meta["token_expires_at"] = tok.ExpiresAt
				meta["token_device_id"] = tok.DeviceID
			}
			var currentWingStatus *wingStatus
			if status, statusErr := readWingStatus(); statusErr == nil {
				currentWingStatus = status
				meta["wing_status"] = status.State
				if roostURL := relayMetadataURL(status.RoostURL); roostURL != "" {
					meta["wing_status_roost"] = roostURL
				}
				if status.Error != "" {
					meta["wing_status_error"] = status.Error
				}
			}
			// Try whoami
			if tok != nil {
				relayURL := activeWingRelayHTTPURL(cfg, currentWingStatus)
				if info, infoErr := auth.FetchUserInfo(relayURL, tok.Token); infoErr == nil {
					meta["account"] = formatUserIdentity(info)
				} else {
					meta["account_error"] = infoErr.Error()
				}
			}
			metaJSON, err := json.MarshalIndent(meta, "", "  ")
			if err != nil {
				return fmt.Errorf("encode support metadata: %w", err)
			}
			if err := addZipFile(zw, "meta.json", metaJSON); err != nil {
				return err
			}

			// wing.log (last 10000 lines)
			if err := addZipTail(zw, "wing.log", wingLogPath(), 10000); err != nil {
				return err
			}

			// egg.log (last 1000 lines)
			if err := addZipTail(zw, "egg.log", filepath.Join(cfg.Dir, "egg.log"), 1000); err != nil {
				return err
			}

			// Session logs (preserved from ~/.wingthing/logs/)
			logsDir := filepath.Join(cfg.Dir, "logs")
			if logEntries, logErr := os.ReadDir(logsDir); logErr == nil {
				for _, e := range logEntries {
					if err := addZipTail(zw, "logs/"+e.Name(), filepath.Join(logsDir, e.Name()), 500); err != nil {
						return err
					}
				}
			}

			// wing.yaml (redact secrets)
			if err := addZipRedacted(zw, "wing.yaml", filepath.Join(cfg.Dir, "wing.yaml"),
				[]string{"jwt_key:", "allow_keys:", "- public_key:"}); err != nil {
				return err
			}

			// wing.status
			if err := addZipCopy(zw, "wing.status", wingStatusPath()); err != nil {
				return err
			}

			// doctor output
			if doctorOut, doctorErr := exec.Command(os.Args[0], "doctor").CombinedOutput(); doctorErr == nil {
				if err := addZipFile(zw, "doctor.txt", doctorOut); err != nil {
					return err
				}
			}

			fmt.Printf("diagnostic bundle: %s\n", zipPath)
			return nil
		},
	}
}

func addZipFile(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create support bundle entry %s: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write support bundle entry %s: %w", name, err)
	}
	return nil
}

func addZipRedacted(zw *zip.Writer, name, srcPath string, redactPrefixes []string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read support source %s: %w", srcPath, err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		redacted := false
		for _, prefix := range redactPrefixes {
			if strings.HasPrefix(trimmed, prefix) {
				out = append(out, strings.SplitN(line, ":", 2)[0]+": <redacted>")
				redacted = true
				break
			}
		}
		if !redacted {
			out = append(out, line)
		}
	}
	return addZipFile(zw, name, []byte(strings.Join(out, "\n")))
}

func addZipCopy(zw *zip.Writer, name, srcPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read support source %s: %w", srcPath, err)
	}
	return addZipFile(zw, name, data)
}

func addZipTail(zw *zip.Writer, name, srcPath string, maxLines int) error {
	f, err := os.Open(srcPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open support source %s: %w", srcPath, err)
	}
	defer closeWithLog("support source", f)

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > maxLines {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan support source %s: %w", srcPath, err)
	}

	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create support bundle entry %s: %w", name, err)
	}
	for _, line := range lines {
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return fmt.Errorf("write support bundle entry %s: %w", name, err)
		}
	}
	return nil
}
