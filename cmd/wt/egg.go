package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func eggCmd() *cobra.Command {
	var configFlag string
	var traceFlag bool
	var resumeFlag string
	var nameFlag string
	var unsandboxedFlag bool

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
			return eggSpawn(cmd.Context(), args[0], configFlag, traceFlag, resumeFlag, nameFlag, unsandboxedFlag, args[1:])
		},
		Example: "  wt egg claude\n" +
			"  wt egg claude --name research -- --model sonnet\n" +
			"  wt egg codex -- -m gpt-5.6-terra",
	}

	cmd.Flags().StringVar(&configFlag, "config", "", "path to egg.yaml (default: discover from cwd, then ~/.wingthing/egg.yaml, then built-in)")
	cmd.Flags().BoolVar(&traceFlag, "trace", false, "wrap sandbox with strace for syscall tracing (Linux only)")
	cmd.Flags().StringVar(&resumeFlag, "resume", "", "resume a previous session by session ID")
	cmd.Flags().StringVarP(&nameFlag, "name", "n", "", "human-readable session name")
	cmd.Flags().BoolVar(&unsandboxedFlag, "unsandboxed", false, "trust the host boundary; disable Wingthing filesystem, network, syscall, and resource isolation")
	cmd.MarkFlagsMutuallyExclusive("config", "unsandboxed")

	cmd.AddCommand(eggRunCmd())
	cmd.AddCommand(eggStopCmd())
	cmd.AddCommand(eggListCmd())
	cmd.AddCommand(eggExplainCmd())
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
		networkFlag                []string
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
		toolNamesFlag              []string
		toolSocketFlag             string
		kindFlag                   string
		commandFlag                []string
		agentArgFlag               []string
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
			if err := validateSessionID(sessionID); err != nil {
				return err
			}
			envPath := filepath.Join(cfg.Dir, "eggs", sessionID, ".egg.env")
			envMap, err := readEggEnvironment(envPath, envFlag, envFileRequired)
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
				memLimit = parseMemFlag(memFlag)
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
				Network:                    networkFlag,
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
				ToolNames:                  toolNamesFlag,
				ToolSocketPath:             toolSocketFlag,
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
	cmd.Flags().StringVar(&agentDomainsFlag, "agent-domains", "", "agent domain policy: merge or none (internal)")
	cmd.Flags().StringArrayVar(&envFlag, "env", nil, "environment variables (KEY=VAL)")
	cmd.Flags().BoolVar(&envFileRequired, "env-file-required", false, "require the internal environment payload")
	cmd.Flags().MarkHidden("env-file-required")
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
	cmd.Flags().StringArrayVar(&toolNamesFlag, "tool-name", nil, "privileged tool names (internal)")
	cmd.Flags().StringVar(&toolSocketFlag, "tool-socket", "", "tool socket path (internal)")
	cmd.Flags().StringVar(&kindFlag, "kind", "agent", "session kind (internal)")
	cmd.Flags().StringArrayVar(&commandFlag, "command-arg", nil, "command argument (internal)")
	cmd.Flags().StringArrayVar(&agentArgFlag, "agent-arg", nil, "extra agent argument (internal)")
	cmd.MarkFlagRequired("session-id")

	return cmd
}

const maxEggEnvironmentBytes = 1 << 20

func readEggEnvironment(path string, entries []string, required bool) (map[string]string, error) {
	environment := make(map[string]string)
	if path != "" {
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) && !required {
			file = nil
		} else if err != nil {
			return nil, fmt.Errorf("read egg environment: %w", err)
		}
		if file != nil {
			data, readErr := io.ReadAll(io.LimitReader(file, maxEggEnvironmentBytes+1))
			closeErr := file.Close()
			removeErr := os.Remove(path)
			if readErr != nil {
				return nil, fmt.Errorf("read egg environment: %w", readErr)
			}
			if closeErr != nil {
				return nil, fmt.Errorf("close egg environment: %w", closeErr)
			}
			if removeErr != nil {
				return nil, fmt.Errorf("remove egg environment: %w", removeErr)
			}
			if len(data) > maxEggEnvironmentBytes {
				return nil, errors.New("egg environment exceeds 1 MiB")
			}
			if err := json.Unmarshal(data, &environment); err != nil {
				return nil, fmt.Errorf("decode egg environment: %w", err)
			}
			if environment == nil {
				environment = make(map[string]string)
			}
		}
	}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("invalid environment entry %q", entry)
		}
		environment[key] = value
	}
	for key, value := range environment {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("invalid environment variable %q", key)
		}
	}
	return environment, nil
}

func writeEggEnvironment(dir string, environment map[string]string) (string, error) {
	data, err := json.Marshal(environment)
	if err != nil {
		return "", fmt.Errorf("encode egg environment: %w", err)
	}
	if len(data) > maxEggEnvironmentBytes {
		return "", errors.New("egg environment exceeds 1 MiB")
	}
	path := filepath.Join(dir, ".egg.env")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("create egg environment: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return "", fmt.Errorf("write egg environment: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close egg environment: %w", err)
	}
	return path, nil
}

func prepareEggEnvironmentTransport(dir string, args []string, environment map[string]string) ([]string, string, error) {
	path, err := writeEggEnvironment(dir, environment)
	if err != nil {
		return nil, "", err
	}
	return append(args, "--env-file-required"), path, nil
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
			session, ec, err := openLocalEgg(cmd.Context(), cfg, args[0])
			if err != nil {
				return err
			}
			defer ec.Close()
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

// explainedPolicy is the wire shape of `wt egg explain`. The sandbox is egg.yaml
// plus holes drilled automatically for the agent, and until now nothing could
// report what that added up to. These field names are an API contract.
type explainedPolicy struct {
	Agent        string           `json:"agent"`
	ConfigSource string           `json:"config_source"`
	Isolation    string           `json:"isolation"`
	NetworkNeed  string           `json:"network_need"`
	Enforcement  string           `json:"enforcement"`
	Domains      []string         `json:"domains"`
	LocalPorts   []int            `json:"local_ports"`
	Mode         string           `json:"mode"`
	Mounts       []explainedMount `json:"mounts"`
	Deny         []string         `json:"deny"`
	DenyWrite    []string         `json:"deny_write"`
	Drilled      []explainedHole  `json:"drilled"`
	Derived      []explainedHole  `json:"derived"`
	Suppressed   []explainedHole  `json:"suppressed"`
}

type explainedMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type explainedHole struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
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
			eggCfg, source, err := loadEggConfigForExplain(configFlag, cwd)
			if err != nil {
				return err
			}
			home, _ := os.UserHomeDir()

			policy, err := explainPolicyWithProvider(eggCfg, agentName, home, source, os.Getenv("WT_PROVIDER_BASE_URL"))
			if err != nil {
				return err
			}
			if jsonFlag {
				return writePolicyJSON(cmd.OutOrStdout(), policy)
			}
			return renderPolicy(cmd.OutOrStdout(), policy)
		},
	}

	cmd.Flags().StringVar(&configFlag, "config", "", "path to egg.yaml (default: discover from cwd, then built-in)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print the policy as JSON")
	return cmd
}

// loadEggConfigForExplain resolves the same config eggSpawn would use, and
// reports where it came from. An explicit --config that does not load is an
// error here, unlike discovery, which is allowed to fall back.
func loadEggConfigForExplain(configPath, cwd string) (*egg.EggConfig, string, error) {
	if configPath != "" {
		cfg, err := egg.ResolveEggConfig(configPath)
		if err != nil {
			return nil, "", fmt.Errorf("load egg config: %w", err)
		}
		return cfg, configPath, nil
	}
	source := "built-in defaults"
	if cwd != "" {
		if path := filepath.Join(cwd, "egg.yaml"); fileExists(path) {
			source = path
		}
	}
	return egg.DiscoverEggConfig(cwd, nil), source, nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// explainEnforcement reports how the network policy is actually held, which is
// not the same on both platforms. macOS denies all egress in the seatbelt
// profile and allows only the proxy port. Linux strips CLONE_NEWNET for any
// need above NetworkNone, so HTTPS_PROXY is the only thing steering traffic and
// the sandboxed process is free to ignore it. Saying "proxy" there would be a
// lie, and this command exists to stop the sandbox being unauditable.
func explainEnforcement(need sandbox.NetworkNeed, goos string) string {
	switch need {
	case sandbox.NetworkNone:
		return "none"
	case sandbox.NetworkFull:
		return "unrestricted"
	}
	if goos == "linux" {
		return "advisory"
	}
	if need == sandbox.NetworkHTTPS {
		return "proxy"
	}
	return "kernel"
}

func explainPolicy(cfg *egg.EggConfig, agentName, home, source string) explainedPolicy {
	policy, _ := explainPolicyWithProvider(cfg, agentName, home, source, "")
	return policy
}

func explainPolicyWithProvider(cfg *egg.EggConfig, agentName, home, source, providerURL string) (explainedPolicy, error) {
	resolved, err := egg.ResolvePolicyWithProvider(cfg, agentName, home, providerURL)
	if err != nil {
		return explainedPolicy{}, err
	}
	isolation := "wingthing-sandbox"
	if !egg.RequiresSandbox(cfg, agentName) {
		isolation = "outer-boundary"
		// Agent profile write directories are holes in a sandbox. They are not
		// mounts or restrictions when the outer host is the boundary.
		resolved.Mounts = nil
		resolved.Deny = nil
		resolved.DenyWrite = nil
	}

	p := explainedPolicy{
		Agent:        agentName,
		ConfigSource: source,
		Isolation:    isolation,
		NetworkNeed:  resolved.NetworkNeed.String(),
		Enforcement:  explainEnforcement(resolved.NetworkNeed, runtime.GOOS),
		Domains:      nonNilStrings(resolved.Domains),
		LocalPorts:   resolved.LocalPorts,
		Mode:         resolved.Mode,
		Deny:         nonNilStrings(resolved.Deny),
		DenyWrite:    nonNilStrings(resolved.DenyWrite),
		Mounts:       make([]explainedMount, 0, len(resolved.Mounts)),
		Drilled:      make([]explainedHole, 0, len(resolved.Drilled)),
		Derived:      make([]explainedHole, 0, len(resolved.Derived)),
		Suppressed:   make([]explainedHole, 0, len(resolved.Suppressed)),
	}
	if p.LocalPorts == nil {
		p.LocalPorts = []int{}
	}
	for _, m := range resolved.Mounts {
		p.Mounts = append(p.Mounts, explainedMount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	for _, h := range resolved.Drilled {
		p.Drilled = append(p.Drilled, explainedHole{Kind: h.Kind, Value: h.Value, Agent: h.Agent, Reason: h.Reason})
	}
	for _, h := range resolved.Derived {
		p.Derived = append(p.Derived, explainedHole{Kind: h.Kind, Value: h.Value, Agent: h.Agent, Reason: h.Reason})
	}
	for _, h := range resolved.Suppressed {
		p.Suppressed = append(p.Suppressed, explainedHole{Kind: h.Kind, Value: h.Value, Agent: h.Agent, Reason: h.Reason})
	}
	return p, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func writePolicyJSON(w io.Writer, p explainedPolicy) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

func renderPolicy(w io.Writer, p explainedPolicy) error {
	agentName := p.Agent
	if agentName == "" {
		agentName = "(none — shell session)"
	}
	mode := p.Mode
	if mode == "" {
		mode = "default"
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "agent\t%s\n", agentName)
	fmt.Fprintf(tw, "config\t%s\n", p.ConfigSource)
	fmt.Fprintf(tw, "isolation\t%s\n", p.Isolation)
	fmt.Fprintf(tw, "network\t%s\n", p.NetworkNeed)
	fmt.Fprintf(tw, "enforcement\t%s\n", p.Enforcement)
	fmt.Fprintf(tw, "mode\t%s\n", mode)
	if err := tw.Flush(); err != nil {
		return err
	}

	drilledDomains := make(map[string]string, len(p.Drilled))
	for _, h := range p.Drilled {
		if h.Kind == "domain" {
			drilledDomains[h.Value] = h.Reason
		}
	}
	derivedDomains := make(map[string]string, len(p.Derived))
	for _, h := range p.Derived {
		if h.Kind == "domain" {
			derivedDomains[h.Value] = h.Reason
		}
	}

	if len(p.Domains) > 0 {
		fmt.Fprintf(w, "\ndomains (%d)\n", len(p.Domains))
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, d := range p.Domains {
			if reason, ok := derivedDomains[d]; ok {
				fmt.Fprintf(tw, "  %s\tderived\t%s\n", d, reason)
			} else if reason, ok := drilledDomains[d]; ok {
				fmt.Fprintf(tw, "  %s\tauto\t%s\n", d, reason)
			} else {
				fmt.Fprintf(tw, "  %s\tdeclared\t\n", d)
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(p.Suppressed) > 0 {
		fmt.Fprintln(w, "\nsuppressed agent domains")
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, h := range p.Suppressed {
			fmt.Fprintf(tw, "  %s\tsuppressed\t%s\n", h.Value, h.Reason)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(p.LocalPorts) > 0 {
		fmt.Fprintf(w, "\nforwarded loopback ports\n")
		for _, port := range p.LocalPorts {
			fmt.Fprintf(w, "  %d\n", port)
		}
	}

	if len(p.Mounts) > 0 {
		fmt.Fprintf(w, "\nmounts (%d)\n", len(p.Mounts))
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, m := range p.Mounts {
			access := "rw"
			if m.ReadOnly {
				access = "ro"
			}
			fmt.Fprintf(tw, "  %s\t%s\n", access, m.Source)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	for _, section := range []struct {
		title string
		paths []string
	}{{"denied", p.Deny}, {"deny-write", p.DenyWrite}} {
		if len(section.paths) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s (%d)\n", section.title, len(section.paths))
		for _, path := range section.paths {
			fmt.Fprintf(w, "  %s\n", path)
		}
	}

	if len(p.Drilled) > 0 {
		fmt.Fprintf(w, "\nauto-drilled for %s (%d)\n", p.Agent, len(p.Drilled))
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, h := range p.Drilled {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", h.Kind, h.Value, h.Reason)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
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
func eggSpawn(ctx context.Context, agentName, configPath string, trace bool, resumeID, name string, unsandboxed bool, agentArgs []string) error {
	if trace && runtime.GOOS != "linux" {
		return fmt.Errorf("--trace requires Linux (strace is not available on %s)", runtime.GOOS)
	}
	if trace && unsandboxed {
		return errors.New("--trace and --unsandboxed cannot be combined")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	cwd, _ := os.Getwd()
	eggCfg, err := loadSpawnEggConfig(configPath, cwd, unsandboxed)
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

	sessionID := uuid.New().String()[:8]

	// Handle --resume: restore chat history and get agent session ID
	var agentResumeID string
	if resumeID != "" {
		home, _ := os.UserHomeDir()
		eggDir := filepath.Join(cfg.Dir, "eggs", resumeID)
		var restoreErr error
		agentResumeID, restoreErr = egg.RestoreSessionHistory(agentName, cwd, eggDir, home)
		if restoreErr != nil {
			return fmt.Errorf("restore session: %w", restoreErr)
		}
	}

	// Spawn egg as child process
	ec, err := spawnEgg(cfg, sessionID, agentName, eggCfg, uint32(rows), uint32(cols), cwd, false, false, trace, EggIdentity{}, 0, spawnEggOpts{ResumeSessionID: agentResumeID, Label: name, Kind: "agent", AgentArgs: agentArgs})
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

	// Read stdin → egg input. Ctrl+B Q detaches the local client while the
	// setsid egg process and its agent keep running.
	detachCh := make(chan struct{}, 1)
	go func() {
		filter := &attachInputFilter{}
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				data, detach := filter.filter(buf[:n])
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

	if exitCode != 0 {
		// Dump egg.log so the user can see why the agent crashed
		logPath := filepath.Join(cfg.Dir, "eggs", sessionID, "egg.log")
		if logData, err := os.ReadFile(logPath); err == nil && len(logData) > 0 {
			os.Stderr.Write(logData)
		}
		return fmt.Errorf("agent exited with code %d", exitCode)
	}
	return nil
}

// loadSpawnEggConfig is shared by the human CLI and the local MCP server so an
// LLM gets the same trusted-VM behavior. Unsandboxed mode deliberately ignores
// discovered policy; combining it with an explicit config is rejected rather
// than giving a false impression that any of that config is enforced.
func loadSpawnEggConfig(configPath, cwd string, unsandboxed bool) (*egg.EggConfig, error) {
	if unsandboxed {
		if configPath != "" {
			return nil, errors.New("--config and --unsandboxed cannot be combined")
		}
		return egg.UnsandboxedEggConfig(), nil
	}
	if configPath != "" {
		cfg, err := egg.ResolveEggConfig(configPath)
		if err != nil {
			return nil, fmt.Errorf("load egg config: %w", err)
		}
		return cfg, nil
	}
	return egg.DiscoverEggConfig(cwd, nil), nil
}

// EggIdentity holds the authenticated user's identity for per-session env injection.
// Zero value means no identity (local egg, no authenticated user).
type EggIdentity struct {
	UserID       string   // relay user ID
	Email        string   // authenticated email (e.g. from Google OAuth)
	DisplayName  string   // human-readable name (Google full name, GitHub login)
	OrgWing      bool     // true if this is an org wing — all users get per-user isolation
	SharedHost   bool     // true when several owners use one OS account through a roost
	AllowedPaths []string // canonical host roots this owner may reach on a shared host
	SealedFS     bool     // replace caller filesystem rules with the shared-host allowlist jail
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

func writeEggOwner(dir, userID, email string) error {
	if userID == "" {
		return nil
	}
	if len(userID) > 256 || strings.ContainsAny(userID, "\r\n\x00") {
		return errors.New("invalid egg owner ID")
	}
	if len(email) > 512 || strings.ContainsAny(email, "\r\n\x00") {
		return errors.New("invalid egg owner email")
	}
	ownerData := userID
	if email != "" {
		ownerData += "\n" + email
	}
	path := filepath.Join(dir, "egg.owner")
	if err := os.WriteFile(path, []byte(ownerData), 0600); err != nil {
		return fmt.Errorf("write egg owner: %w", err)
	}
	return os.Chmod(path, 0600)
}

// spawnEggOpts holds optional parameters for spawnEgg.
// validateAgentArgs checks caller-supplied agent arguments before any process
// is spawned. These become argv entries verbatim, so an empty or NUL-bearing
// argument is rejected here rather than confusing the agent's own flag parser.
func validateAgentArgs(args []string) error {
	for _, arg := range args {
		if strings.TrimSpace(arg) == "" {
			return errors.New("agent arguments cannot be empty")
		}
		if strings.IndexByte(arg, 0) >= 0 {
			return errors.New("agent arguments cannot contain NUL bytes")
		}
	}
	return nil
}

type spawnEggOpts struct {
	ResumeSessionID string
	ToolNames       []string
	ToolSocketPath  string
	Label           string
	Kind            string
	Command         []string
	AgentArgs       []string
	Principal       string
}

// spawnEgg starts a per-session egg child process and returns a connected client.
func spawnEgg(cfg *config.Config, sessionID, agentName string, eggCfg *egg.EggConfig, rows, cols uint32, cwd string, debug, vte, trace bool, identity EggIdentity, idleTimeout time.Duration, opts ...spawnEggOpts) (*egg.Client, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	var o spawnEggOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	if err := validateSessionName(o.Label); err != nil {
		return nil, err
	}
	if len(o.Command) > 0 && o.Command[0] == "" {
		return nil, errors.New("command executable cannot be empty")
	}
	for _, arg := range o.Command {
		if strings.IndexByte(arg, 0) >= 0 {
			return nil, errors.New("command arguments cannot contain NUL bytes")
		}
	}
	if o.Label != "" {
		if err := ensureSessionNameAvailable(cfg, o.Label, sessionID); err != nil {
			return nil, err
		}
	}
	if identity.SealedFS {
		if runtime.GOOS != "linux" {
			return nil, errors.New("shared-host credential isolation requires the Linux filesystem jail")
		}
		sealed, err := sealedSharedHostEggConfig(cfg, eggCfg, cwd, identity.AllowedPaths)
		if err != nil {
			return nil, err
		}
		eggCfg = sealed
	}
	// Pre-flight: verify the sandbox can work before spawning a child process.
	// Catches AppArmor userns restrictions, missing sysctl, etc. with a clear
	// error instead of a silent 5s timeout.
	if egg.RequiresSandbox(eggCfg, agentName) {
		if ok, help := sandbox.CheckCapability(); !ok {
			return nil, fmt.Errorf("sandbox not available: %s\nrun: wt doctor --fix", help)
		}
	}

	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create egg dir: %w", err)
	}
	if err := writeSessionPrincipal(dir, o.Principal); err != nil {
		return nil, err
	}
	if err := writeEggOwner(dir, identity.UserID, identity.Email); err != nil {
		return nil, err
	}
	if o.Label != "" {
		if err := writeSessionName(dir, o.Label); err != nil {
			return nil, err
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find executable: %w", err)
	}

	args := []string{"egg", "run",
		"--session-id", sessionID,
		"--agent", agentName,
		"--kind", o.Kind,
		"--cwd", cwd,
		"--rows", strconv.Itoa(int(rows)),
		"--cols", strconv.Itoa(int(cols)),
	}
	for _, arg := range o.Command {
		args = append(args, "--command-arg="+arg)
	}
	if err := validateAgentArgs(o.AgentArgs); err != nil {
		return nil, err
	}
	for _, arg := range o.AgentArgs {
		args = append(args, "--agent-arg="+arg)
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
	for _, d := range eggCfg.Network.Domains {
		args = append(args, "--network", d)
	}
	if eggCfg.Network.AgentDomains != "" {
		args = append(args, "--agent-domains", eggCfg.Network.AgentDomains)
	}
	// Per-user home directory for multi-user isolation on org wings and shared roosts.
	// On personal wings, the owner IS the machine — use real HOME so
	// agent auth (e.g. Claude Code /login) and config persist normally.
	// On org wings, ALL users get per-user homes for isolation.
	// Computed before BuildEnvMap so ~ expansion in FS rules (e.g. deny:~/.ssh)
	// resolves against the correct home.
	realHome, _ := os.UserHomeDir()
	effectiveHome := realHome
	isolatedUser := identity.UserID != "" && (identity.OrgWing || identity.SharedHost)
	if isolatedUser {
		effectiveHome = filepath.Join(cfg.Dir, "user-homes", userHash(identity.UserID))
	}
	envMap := eggCfg.BuildEnvMap(effectiveHome)
	if identity.SharedHost {
		safe := map[string]bool{
			"HOME": true, "PATH": true, "TERM": true, "LANG": true,
			"USER": true, "SHELL": true, "TMPDIR": true,
		}
		for key := range envMap {
			if !safe[key] {
				delete(envMap, key)
			}
		}
	}
	// Inject agent profile env vars from host env (e.g. ANTHROPIC_API_KEY for claude).
	// BuildEnvMap uses the egg config whitelist which may not include these.
	profile := egg.Profile(agentName)
	for _, k := range profile.EnvVars {
		if identity.SharedHost {
			continue
		}
		if _, ok := envMap[k]; !ok {
			if v := os.Getenv(k); v != "" {
				envMap[k] = v
			}
		}
	}
	// Platform-specific env vars the agent needs (e.g. macOS Keychain access for Claude).
	for _, k := range profile.PlatformEnv {
		if identity.SharedHost {
			continue
		}
		if _, ok := envMap[k]; !ok {
			if v := os.Getenv(k); v != "" {
				envMap[k] = v
			}
		}
	}
	// Agent-forced env defaults (e.g. CLAUDE_CODE_DISABLE_MOUSE). Host/config still wins.
	for k, v := range profile.SetEnv {
		if _, ok := envMap[k]; !ok {
			envMap[k] = v
		}
	}
	if isolatedUser {
		perUserHome := effectiveHome
		if identity.SharedHost {
			profileDirs := append(append([]string(nil), profile.WriteRegex...), profile.WriteDirs...)
			profileDirs = append(profileDirs, filepath.Join(".local", "bin"))
			if err := prepareSharedAgentHome(perUserHome, profileDirs); err != nil {
				return nil, fmt.Errorf("prepare shared-host agent home: %w", err)
			}
		} else if err := os.MkdirAll(perUserHome, 0700); err != nil {
			return nil, fmt.Errorf("prepare agent home: %w", err)
		}
		// Seed shell + agent config symlinks from real HOME
		if realHome != "" && !identity.SharedHost {
			for _, rc := range []string{".bashrc", ".zshrc", ".profile"} {
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
		if !identity.SharedHost {
			if err := os.MkdirAll(localBin, 0755); err != nil {
				return nil, fmt.Errorf("prepare agent bin directory: %w", err)
			}
		}
		if agentBin, err := exec.LookPath(agentName); err == nil {
			dst := filepath.Join(localBin, agentName)
			if identity.SealedFS {
				if err := installSharedAgentBinary(agentBin, perUserHome, agentName); err != nil {
					return nil, fmt.Errorf("prepare shared-host %s runtime: %w", agentName, err)
				}
			} else if _, err := os.Lstat(dst); err != nil {
				os.Symlink(agentBin, dst)
			}
		}
		args = append(args, "--user-home", perUserHome)
	}
	// Claude keeps its monolithic config (onboarding completion, theme, project
	// trust, MCP servers) in $CLAUDE_CONFIG_DIR/.claude.json, defaulting to
	// ~/.claude.json at the HOME root. On isolated (org/shared) homes the HOME
	// root is mounted read-only, so that file — and the .claude.json.lock it
	// writes beside it — can only be created in the overlay COW layer, which is
	// copied back to the real home only when the session process exits cleanly.
	// Browser sessions routinely outlive their tab and are reaped hard (or never
	// reaped), so onboarding never persisted and every new session re-prompted.
	// Point CLAUDE_CONFIG_DIR at ~/.claude, which is bind-mounted read-write to
	// the per-user home: onboarding and theme now land there immediately and
	// survive across sessions regardless of how the previous one ended.
	if isolatedUser && agentName == "claude" {
		claudeDir := filepath.Join(effectiveHome, ".claude")
		envMap["CLAUDE_CONFIG_DIR"] = claudeDir
		// One-time migration: users who already completed onboarding under the
		// old layout have their config at ~/.claude.json (HOME root). Relocating
		// CLAUDE_CONFIG_DIR would leave that behind and re-prompt them once on
		// release. Seed the new path from the old file if it hasn't been created
		// yet. Only a regular file is migrated — a symlink at the root is the
		// shared empty stub, whose users never had persisted state to preserve.
		newCfg := filepath.Join(claudeDir, ".claude.json")
		oldCfg := filepath.Join(effectiveHome, ".claude.json")
		if _, err := os.Stat(newCfg); os.IsNotExist(err) {
			if fi, lerr := os.Lstat(oldCfg); lerr == nil && fi.Mode().IsRegular() {
				if data, rerr := os.ReadFile(oldCfg); rerr == nil {
					os.MkdirAll(claudeDir, 0700)
					os.WriteFile(newCfg, data, 0600)
				}
			}
		}
	}
	// Rebuild agent settings every session for org wing users.
	// Reads existing prefs, layers host settings on top (host always wins
	// for permissions), then injects agent-specific overrides.
	if isolatedUser && !identity.SharedHost {
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
			} else if realHome != "" && !identity.SharedHost {
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
			if len(baseSettings) > 0 {
				if data, err := json.MarshalIndent(baseSettings, "", "  "); err == nil {
					os.WriteFile(settingsDst, append(data, '\n'), 0644)
				}
			}
		}
	}
	// Write ANTHROPIC_API_KEY to a stable file and use apiKeyHelper to read
	// it. The key never enters the agent's environment. The file lives at
	// effectiveHome/.anthropic_key (not per-session) so the settings.json
	// path doesn't go stale when sessions end or race with each other.
	setupAPIKeyHelper(agentName, envMap, effectiveHome)
	sessionEnv := make(map[string]string, len(envMap)+6)
	for k, v := range envMap {
		// Skip WT_ prefix — reserved for session identity injection
		if strings.HasPrefix(k, "WT_") {
			continue
		}
		sessionEnv[k] = v
	}
	// Inject per-session identity vars (always override, not configurable via egg.yaml).
	// All values are sanitized to prevent shell injection.
	sessionEnv["WT_SESSION_ID"] = sessionID
	// Preview: session-specific file so multi-user previews don't collide.
	// The shim writes to $WT_PREVIEW_DIR/$WT_PREVIEW_FILE.
	sessionEnv["WT_PREVIEW_DIR"] = cwd
	sessionEnv["WT_PREVIEW_FILE"] = ".wt-preview-" + sessionID
	if identity.Email != "" {
		sessionEnv["WT_USER"] = NormalizeUser(identity.Email)
		sessionEnv["WT_USER_EMAIL"] = sanitizeEnvValue(identity.Email)
	}
	if identity.DisplayName != "" {
		sessionEnv["WT_USER_NAME"] = sanitizeEnvValue(identity.DisplayName)
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
	if trace || eggCfg.Trace {
		args = append(args, "--trace")
	}
	if idleTimeout > 0 {
		args = append(args, "--idle-timeout", idleTimeout.String())
	}
	if o.ResumeSessionID != "" {
		args = append(args, "--resume-session", o.ResumeSessionID)
	}
	if o.ToolSocketPath != "" && len(o.ToolNames) > 0 {
		args = append(args, "--tool-socket", o.ToolSocketPath)
		for _, tn := range o.ToolNames {
			args = append(args, "--tool-name", tn)
		}
	}
	if identity.SharedHost {
		args = append(args, "--skip-host-agent-env")
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
	args, envPath, err := prepareEggEnvironmentTransport(dir, args, sessionEnv)
	if err != nil {
		logFile.Close()
		return nil, err
	}
	defer os.Remove(envPath)

	child := exec.Command(exe, args...)
	// Always build a clean env for the wt-egg-run child process.
	// Base system vars only. Session values move through an owner-only file so
	// credentials never appear in the wrapper's argv or ambient environment.
	// This prevents server secrets (WT_JWT_SECRET, GOOGLE_CLIENT_SECRET)
	// from leaking when eggs are spawned from the roost process (org wings),
	// while still passing platform vars agents need (e.g. macOS Keychain).
	{
		allowed := map[string]bool{
			"HOME": true, "PATH": true, "TERM": true, "LANG": true,
			"USER": true, "SHELL": true, "TMPDIR": true, "WINGTHING_DIR": true,
		}
		var childEnv []string
		for _, e := range os.Environ() {
			k, _, ok := strings.Cut(e, "=")
			if ok && allowed[k] {
				childEnv = append(childEnv, e)
			}
		}
		child.Env = childEnv
	}
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

func sealedSharedHostEggConfig(cfg *config.Config, source *egg.EggConfig, cwd string, allowedPaths []string) (*egg.EggConfig, error) {
	if source == nil {
		return nil, errors.New("shared-host egg config is required")
	}
	rules, canonical, err := sharedHostFilesystemRules(cfg, allowedPaths)
	if err != nil {
		return nil, err
	}
	resolvedCWD := canonicalSessionPath(cwd)
	if !isUnderPaths(resolvedCWD, canonical) {
		return nil, fmt.Errorf("working directory %q is outside this user's roost paths", cwd)
	}
	sealed := *source
	sealed.FS = rules
	sealed.AgentSettings = nil
	sealed.Env = append(egg.EnvField(nil), source.Env...)
	sealed.Network.Domains = append([]string(nil), source.Network.Domains...)
	sealed.Network.LocalPorts = append([]int(nil), source.Network.LocalPorts...)
	return &sealed, nil
}

func sharedHostFilesystemRules(cfg *config.Config, allowedPaths []string) ([]string, []string, error) {
	canonical := canonicalPaths(allowedPaths)
	if len(canonical) == 0 {
		return nil, nil, errors.New("shared-host sessions require at least one configured workspace path")
	}
	stateDir := canonicalSessionPath(cfg.Dir)
	hostHome, _ := os.UserHomeDir()
	hostHome = canonicalSessionPath(hostHome)
	for _, path := range canonical {
		if path == string(filepath.Separator) {
			return nil, nil, errors.New("the filesystem root cannot be a shared-roost workspace path")
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			if err == nil {
				err = errors.New("not a directory")
			}
			return nil, nil, fmt.Errorf("shared-roost workspace %q: %w", path, err)
		}
		if isUnderPaths(stateDir, []string{path}) || isUnderPaths(path, []string{stateDir}) {
			return nil, nil, fmt.Errorf("shared-roost workspace %q overlaps Wingthing state", path)
		}
		if hostHome != "." && isUnderPaths(hostHome, []string{path}) {
			return nil, nil, fmt.Errorf("shared-roost workspace %q contains the host account home", path)
		}
	}

	rules := []string{"deny:/"}
	for _, path := range sharedHostSystemReadPaths() {
		rules = append(rules, "ro:"+path)
	}
	for _, path := range canonical {
		rules = append(rules, "rw:"+path)
	}
	return rules, canonical, nil
}

func sharedHostSystemReadPaths() []string {
	candidates := []string{
		"/usr", "/lib", "/lib64",
		"/etc/ssl", "/etc/pki", "/etc/ca-certificates",
		"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf",
		"/etc/passwd", "/etc/group", "/etc/ld.so.cache",
		"/nix/store",
	}
	paths := make([]string, 0, len(candidates))
	for _, path := range candidates {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink == 0 {
			paths = append(paths, path)
		}
	}
	return paths
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

// setupAPIKeyHelper moves ANTHROPIC_API_KEY out of the environment and into a
// stable file + apiKeyHelper setting. This prevents the key from entering the
// agent's env and avoids the v0.128.0 race where per-session paths in
// settings.json went stale.
func setupAPIKeyHelper(agentName string, envMap map[string]string, effectiveHome string) {
	if agentName != "claude" {
		return
	}
	v, ok := envMap["ANTHROPIC_API_KEY"]
	if ok {
		delete(envMap, "ANTHROPIC_API_KEY")
	} else {
		// Shared-host mode strips provider creds from the agent env and skips
		// injecting them into envMap, so the key never reaches this point via
		// envMap. Source it straight from the roost's own environment: it lands
		// only in the 0400 helper file below and still never enters the agent's
		// environment. Without this, authenticated (shared-host) sessions get no
		// credential and every user is forced to log in manually.
		v = os.Getenv("ANTHROPIC_API_KEY")
	}
	if v == "" {
		return
	}
	keyFile := filepath.Join(effectiveHome, ".anthropic_key")
	os.Remove(keyFile) // remove old 0400 file so WriteFile can create fresh
	os.WriteFile(keyFile, []byte(v), 0400)
	agentProfile := egg.Profile(agentName)
	if agentProfile.SettingsFile == "" {
		return
	}
	settingsDst := filepath.Join(effectiveHome, agentProfile.SettingsFile)
	os.MkdirAll(filepath.Dir(settingsDst), 0700)
	settings := make(map[string]any)
	if data, err := os.ReadFile(settingsDst); err == nil {
		json.Unmarshal(data, &settings)
	}
	settings["apiKeyHelper"] = "cat " + keyFile
	if data, err := json.MarshalIndent(settings, "", "  "); err == nil {
		os.WriteFile(settingsDst, append(data, '\n'), 0644)
	}
}
