package egg

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
)

// CodexCapability isolates the installed --version probe. Dry preparation uses
// digest-bound saved output through the same production version parser.
type CodexCapability interface{ Supported(binary string) bool }
type InstalledCodexCapability struct{}

func (InstalledCodexCapability) Supported(binary string) bool { return codexLifecycleSupported(binary) }

type SavedCodexCapability string

func (s SavedCodexCapability) Supported(string) bool {
	return CodexLifecycleVersionSupported(string(s))
}

type LaunchPreparationOptions struct {
	Binary, Home, HostHome, EggDir                                    string
	ProviderURL, BrowserRequestsPath, ToolBinary, PreviewClaudeOSHome string
	Environment                                                       map[string]string
	CodexCapability                                                   CodexCapability
	// A different saved platform can describe capabilities, but this process
	// cannot execute that platform's final policy builder.
	Platform string
	// EnvironmentConfig selects from an explicit snapshot when the caller has
	// not already selected Environment. Production can pass its selected map.
	EnvironmentConfig    *EggConfig
	InheritedEnvironment []string
	Requirements         *LaunchRequirements
	DriverID             string
	RequiredCapabilities []CapabilityRequirement
}

type PreparedLaunch struct {
	Driver                DriverReport
	Args                  []string
	CodexRun, NativeRun   bool
	Sandboxed             bool
	Policy                EffectivePolicy
	Sandbox               sandbox.Config
	ProfileDirs           []string
	LifecycleSettingsPath string
	Deferred              []string
	ProviderHost          string
	NetworkFailure        string
	Transport             string
	Checks                []LaunchCheck
	Environment           map[string]string
	FailureReason         string // fixed classifier for a failed preparation step
}

// PrepareLaunch is the production preparation boundary, shared by Server.RunSession
// and doctor. It resolves policy, builds the sandbox specification, calls the
// actual provider builders, and validates their spools. No proxy, native
// sandbox, snapshot, control endpoint, PTY or provider process starts here.
func PrepareLaunch(rc RunConfig, opts LaunchPreparationOptions, state LaunchState) (out PreparedLaunch, err error) {
	driver, err := SelectDriver(opts.DriverID)
	if err != nil {
		return out, err
	}
	return driver.Prepare(rc, opts, state)
}

func prepareLegacyLaunch(rc RunConfig, opts LaunchPreparationOptions, state LaunchState) (out PreparedLaunch, err error) {
	if rc.Agent == "codex" && rc.InitialRun != nil {
		if err := ValidateInitialCodexPrompt(rc.InitialRun.Prompt); err != nil {
			return out, err
		}
	}
	if state == nil || opts.CodexCapability == nil {
		return out, errors.New("explicit launch state and provider capability required")
	}
	name, args := sessionCommand(rc)
	if name == "" {
		return out, errors.New("unsupported agent")
	}
	if rc.Kind == "" {
		rc.Kind = "agent"
		if len(rc.Command) > 0 {
			rc.Kind = "command"
		}
	}
	selected := opts.Environment
	inherited := append([]string(nil), opts.InheritedEnvironment...)
	if opts.Requirements != nil && opts.Requirements.Capabilities != nil {
		if path := opts.Requirements.Capabilities.ElanHome; path != "" {
			inherited = append(inherited, "ELAN_HOME="+path)
		}
	}
	if opts.EnvironmentConfig != nil {
		selected = map[string]string{}
		for _, entry := range opts.EnvironmentConfig.SelectEnv(opts.Home, inherited) {
			k, v, ok := strings.Cut(entry, "=")
			if ok {
				selected[k] = v
			}
		}
	}
	out.Environment = LaunchEnvironment(rc, selected, inherited)
	if opts.Requirements != nil && opts.Requirements.ProviderStateDir != "" {
		if !filepath.IsAbs(opts.Requirements.ProviderStateDir) {
			return out, errors.New("provider state dir must be absolute")
		}
		if key := Profile(rc.Agent).StateHomeEnv; key != "" {
			out.Environment[key] = opts.Requirements.ProviderStateDir
			out.Deferred = append(out.Deferred, "installed provider honoring its declared state-home environment override")
		} else {
			out.Deferred = append(out.Deferred, "provider profile has no state-home environment override contract")
		}
	}
	opts.Environment = out.Environment
	fsHome := opts.HostHome
	if rc.UserHome != "" {
		fsHome = rc.UserHome
	}
	config := runConfigPolicy(rc)
	if !rc.OuterBoundary && opts.BrowserRequestsPath != "" {
		config.FS = append(append([]string(nil), config.FS...), "rw:"+opts.BrowserRequestsPath)
	}
	policy, err := ResolvePolicyWithProvider(config, rc.Agent, fsHome, opts.ProviderURL)
	if err != nil {
		return out, err
	}
	out.Policy = policy
	out.Sandboxed = !rc.OuterBoundary
	out.ProviderHost, err = ProviderDestination(rc.Agent, opts.ProviderURL)
	if err != nil {
		return out, err
	}
	if rc.Agent != "" && !rc.OuterBoundary {
		out.NetworkFailure = policy.providerNetworkFailure(out.ProviderHost)
		if out.ProviderHost == "" && out.NetworkFailure == "" && (policy.NetworkNeed == sandbox.NetworkNone || policy.NetworkNeed == sandbox.NetworkLocal || policy.NetworkNeed == sandbox.NetworkHTTPS && policy.Mode != "observe") {
			out.Deferred = append(out.Deferred, "provider destination absent from the runtime profile; restricted transport reachability")
		}
	}
	out.Sandbox = sandbox.Config{
		Mounts: append([]sandbox.Mount(nil), policy.Mounts...), Deny: policy.Deny, DenyWrite: policy.DenyWrite,
		DenyRename: denyRenamePaths(rc.FS, fsHome), NetworkNeed: policy.NetworkNeed, NetworkMode: policy.Mode,
		Domains: policy.Domains, LocalPorts: policy.LocalPorts, CPULimit: rc.CPULimit, MemLimit: rc.MemLimit,
		MaxFDs: rc.MaxFDs, PidLimit: rc.PidLimit, SessionID: filepath.Base(opts.EggDir), UserHome: rc.UserHome, HostHome: opts.HostHome,
		Trace: rc.Trace, AllowSockets: sandboxAllowedSockets(rc.ToolSocketPath, opts.Environment),
		ProtectedWriteTargets: append([]string(nil), rc.ProtectedWriteTargets...),
		RoostPolicyPaths:      append([]string(nil), rc.RoostPolicyPaths...),
	}
	defer func() { out.assess(rc, opts, state, err == nil) }()
	if !rc.OuterBoundary {
		if opts.HostHome != "" && len(out.Sandbox.Mounts) > 0 {
			binDir := filepath.Dir(opts.Binary)
			if strings.HasPrefix(binDir, opts.HostHome+string(filepath.Separator)) {
				root := installRoot(binDir, opts.HostHome)
				out.Sandbox.Mounts = append(out.Sandbox.Mounts, sandbox.Mount{Source: root, Target: root, ReadOnly: true})
			}
		}
		if rc.ToolSocketPath != "" && len(rc.ToolNames) > 0 {
			for _, dir := range []string{filepath.Dir(opts.ToolBinary), filepath.Dir(rc.ToolSocketPath)} {
				out.Sandbox.Mounts = append(out.Sandbox.Mounts, sandbox.Mount{Source: dir, Target: dir, ReadOnly: true})
			}
		}
		if opts.PreviewClaudeOSHome != "" {
			protected, err := GuardPreviewClaudeMounts(out.Sandbox.Mounts, opts.PreviewClaudeOSHome)
			if err != nil {
				return out, err
			}
			out.Sandbox.Deny = append(out.Sandbox.Deny, protected...)
		}
		// ResolvePolicyWithProvider owns profile drilling. Prepare exactly those
		// directories, including profiles whose regex also covers adjacent files.
		out.ProfileDirs = append([]string(nil), policy.profileDirs...)
	}
	recording := &recordingLaunchState{LaunchState: state}
	defer func() { out.Sandbox.RequiredWriteDirs = append([]string(nil), recording.spools...) }()
	if rc.Agent == "codex" && len(rc.Command) == 0 && opts.CodexCapability.Supported(opts.Binary) {
		args, out.CodexRun, err = codexRunArgs(args, opts.Home, filepath.Base(opts.EggDir), recording)
		if err != nil {
			return out, fmt.Errorf("prepare Codex lifecycle directories: %w", err)
		}
	}
	if rc.Agent == "codex" && len(rc.Command) == 0 {
		args = codexStartupArgs(args, rc.CWD)
	}
	if rc.InitialRun != nil {
		if !nativeRunSupported(rc.Kind, rc.Agent, rc.Command, out.CodexRun) || rc.Agent != "codex" || rc.ResumeSessionID != "" || providerOptionsEnd(args) != len(args) {
			return out, errors.New("initial run requires a fresh native Codex TUI without a positional prompt")
		}
		args = append(args, "--", rc.InitialRun.Prompt)
	}
	out.Args = args
	for _, dir := range out.ProfileDirs {
		if err := state.MkdirAll(dir); err != nil {
			out.FailureReason = "provider_home_denied"
			return out, fmt.Errorf("create agent profile directory %s: %w", dir, err)
		}
	}
	if rc.Agent == "claude" && len(rc.Command) == 0 && rc.ProviderSessionID != "" {
		if _, native := state.(nativeLaunchState); native {
			metadata, err := sessionrole.Read(opts.EggDir)
			if err != nil {
				return out, err
			}
			if metadata.Role != "" {
				if err := sessionrole.ValidateClaudeEnvironment(out.Environment, filepath.Join(opts.Home, ".claude")); err != nil {
					return out, err
				}
			}
		}
		var policy *sandbox.Config
		if !rc.OuterBoundary {
			policy = &out.Sandbox
		}
		var deferred bool
		out.Args, deferred, err = state.ClaudeSettings(args, opts.Home, opts.EggDir, rc.ProviderSessionID, rc.CWD, policy)
		if err != nil {
			out.FailureReason = "provider_config_denied"
			var refusal *settingsPolicyFailure
			if errors.As(err, &refusal) {
				out.readCheck("provider_config.policy", refusal.path, "provider config", opts, runtime.GOOS)
			}
			return out, fmt.Errorf("prepare native lifecycle hooks: %w", err)
		}
		if deferred {
			out.Deferred = append(out.Deferred, "Claude lifecycle settings merge and file generation")
			out.check("provider.lifecycle_argv", LaunchUnrun, "Claude lifecycle settings require launch-time generation; base argv only", "runtime Claude lifecycle builder", "verify generated lifecycle settings in disposable native acceptance")
		} else {
			out.LifecycleSettingsPath = out.Args[providerOptionsEnd(out.Args)-1]
			out.Sandbox.Mounts = append(out.Sandbox.Mounts, sandbox.Mount{Source: out.LifecycleSettingsPath, Target: out.LifecycleSettingsPath, ReadOnly: true})
			if runtime.GOOS == "linux" && len(rc.ProtectedWriteTargets) > 0 {
				out.Sandbox.ReadOnlyFiles = append(out.Sandbox.ReadOnlyFiles, out.LifecycleSettingsPath)
			}
		}
	}
	out.NativeRun = nativeRunSupported(rc.Kind, rc.Agent, rc.Command, out.CodexRun)
	out.Sandbox.RequiredWriteDirs = append([]string(nil), recording.spools...)
	if !rc.OuterBoundary {
		if opts.Platform != "" && opts.Platform != runtime.GOOS {
			out.Deferred = append(out.Deferred, "final sandbox policy for the saved foreign platform")
		} else {
			verified, err := sandbox.ValidateLaunchPreparation(out.Sandbox)
			if err != nil {
				return out, fmt.Errorf("validate prepared sandbox: %w", err)
			}
			if !verified {
				out.Deferred = append(out.Deferred, "final native sandbox mount policy")
			}
		}
	}
	if out.NetworkFailure != "" {
		return out, errors.New(out.NetworkFailure)
	}
	return out, nil
}
