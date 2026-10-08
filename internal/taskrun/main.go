package taskrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/memory"
	"github.com/ehrlich-b/wingthing/internal/orchestrator"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
)

func ResolveRunEggConfigYAML(configPath, cwd string, unsandboxed bool) (string, error) {
	if unsandboxed {
		if configPath != "" {
			return "", errors.New("--config and --unsandboxed cannot be combined")
		}
		return "", nil
	}
	eggCfg, err := eggclient.LoadSpawnEggConfig(configPath, cwd, false)
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
	eggCfg, err := egg.LoadTaskEggConfigFromYAML(t.EggConfigYAML)
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

func RunTask(ctx context.Context, cfg *config.Config, s *store.Store, t *store.Task) error {
	return RunTaskTo(ctx, cfg, s, t, os.Stdout)
}

func RunTaskTo(ctx context.Context, cfg *config.Config, s *store.Store, t *store.Task, destination io.Writer) error {
	return RunTaskToWithOptions(ctx, cfg, s, t, destination, TaskRunOptions{})
}

type TaskRunOptions struct {
	UserHome     string
	SharedHost   bool
	AllowedPaths []string
}

func RunTaskToWithOptions(ctx context.Context, cfg *config.Config, s *store.Store, t *store.Task, destination io.Writer, options TaskRunOptions) (runErr error) {
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
		canonical, err := eggclient.ValidateSharedHostWorkspacePaths(cfg, options.AllowedPaths)
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
		canonicalWorkDir := wingpolicy.CanonicalSessionPath(workDir)
		if len(options.AllowedPaths) == 0 || !wingpolicy.IsUnderPaths(canonicalWorkDir, options.AllowedPaths) {
			err := fmt.Errorf("working directory %q is outside this user's roost paths", workDir)
			return err
		}
		workDir = canonicalWorkDir
	}
	contextCfg, err := config.LoadContextConfig(cfg.Dir)
	if err != nil {
		return fmt.Errorf("load context config: %w", err)
	}
	if contextCfg != nil && pr.Isolation == "privileged" {
		return errors.New("context: cannot protect secret_file with privileged isolation; use a sandboxed run")
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
	if (options.SharedHost || config.Channel() == "preview") && pr.Isolation == "privileged" {
		msg := "privileged isolation is not available on a shared host"
		return errors.New(msg)
	}
	if pr.Isolation == "privileged" {
		home, _ := os.UserHomeDir()
		policy, policyErr := egg.ResolvePolicyWithProvider(resolvedEggCfg, agentName, home, os.Getenv("WT_PROVIDER_BASE_URL"))
		if policyErr != nil {
			return fmt.Errorf("resolve unconfined network policy: %w", policyErr)
		}
		detail, auditErr := AppendNetworkEnforcementAudit(s, t.ID, "unconfined_egress", "outer-boundary", policy.NetworkNeed, policy.Domains, policy.LocalPorts)
		if auditErr != nil {
			return fmt.Errorf("record unconfined egress audit: %w", auditErr)
		}
		log.Printf("SECURITY: task %s is unsandboxed; %s", t.ID, detail)
	}

	if pr.Isolation != "privileged" {
		home := options.UserHome
		if config.Channel() == "preview" {
			home = cfg.ProviderDataHome()
		}
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
			stateErr = eggclient.PrepareSharedAgentHome(home, dirs)
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
			if installErr := eggclient.InstallSharedAgentBinary(agentBin, home, agentDefinition.Command); installErr != nil {
				return fmt.Errorf("prepare shared-host %s runtime: %w", agentName, installErr)
			}
			// Shared-host tasks intentionally drop ambient provider credentials.
			// Give Claude the same file-backed helper used by interactive org
			// sessions so the secret never enters the agent environment.
			if err := eggclient.SetupAPIKeyHelper(agentName, map[string]string{}, home); err != nil {
				return fmt.Errorf("prepare shared-host credential helper: %w", err)
			}
		}

		mountPaths := taskSandboxMountPaths(pr.Mounts, workDir, options)
		sbCfg, policyErr := directAgentSandboxConfigForTask(resolvedEggCfg, agentName, pr.Isolation, home, workDir, mountPaths, options.SharedHost, contextCfg)
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
		detail, auditErr := AppendNetworkEnforcementAudit(s, t.ID, "sandbox_enforcement", eggclient.ExplainEnforcement(sbCfg.NetworkNeed, runtime.GOOS, sbCfg.NetworkMode), sbCfg.NetworkNeed, sbCfg.Domains, sbCfg.LocalPorts)
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
		agentEnv := directAgentEnvWithPolicy(agentName, home, sbCfg.ProxyPort, !options.SharedHost && config.Channel() != "preview")
		if config.Channel() == "preview" && runtime.GOOS == "darwin" && agentName == "claude" {
			values := make(map[string]string)
			for _, entry := range agentEnv {
				key, value, _ := strings.Cut(entry, "=")
				values[key] = value
			}
			if _, err := egg.ApplyPreviewClaudeOSContext(values, home); err != nil {
				return err
			}
			agentEnv = agentEnv[:0]
			for key, value := range values {
				agentEnv = append(agentEnv, key+"="+value)
			}
		}
		policyArgs, err := eggclient.IsolatedClaudePolicyArgs(agentName, options.UserHome != "" || config.Channel() == "preview")
		if err != nil {
			return err
		}
		runOpts.CmdFactory = func(ctx context.Context, name string, args []string) (*exec.Cmd, error) {
			executable, resolveErr := sandboxAgentExecutable(name, home, options.SharedHost)
			if resolveErr != nil {
				return nil, resolveErr
			}
			cmd, execErr := sb.Exec(ctx, executable, append(append([]string(nil), policyArgs...), args...))
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
	var partial strings.Builder
	for {
		chunk, ok := stream.Next()
		if !ok {
			break
		}
		if _, err := fmt.Fprint(destination, chunk.Text); err != nil {
			return fmt.Errorf("write agent output: %w", err)
		}
		partial.WriteString(chunk.Text)
		// Persist messages while the provider is alive. Losing a supervisor
		// must not erase the transcript already received from the provider.
		if err := s.SetTaskOutput(t.ID, partial.String()); err != nil {
			return fmt.Errorf("record partial agent output: %w", err)
		}
		if err := s.AppendLog(t.ID, "agent_message", &chunk.Text); err != nil {
			return fmt.Errorf("record agent message: %w", err)
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
			Summary:    cmdutil.Truncate(output, 200),
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

func AppendNetworkEnforcementAudit(s *store.Store, taskID, event, enforcement string, need sandbox.NetworkNeed, domains []string, localPorts []int) (string, error) {
	detail := networkEnforcementDetail(enforcement, need, domains, localPorts)
	return detail, s.AppendLog(taskID, event, &detail)
}

func taskSandboxMountPaths(promptMounts []string, workDir string, options TaskRunOptions) []string {
	if options.SharedHost {
		// Shared-host mounts come exclusively from the administrator's egg.yaml.
		return nil
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
