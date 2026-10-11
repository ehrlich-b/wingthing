package egg

import (
	"errors"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func launchFailureReason(code string) string {
	switch code {
	case "executable.policy":
		return "executable_denied"
	case "provider_home.write", "provider_home.scope", "home.scope", "provider_state.write":
		return "provider_home_denied"
	case "workspace.write":
		return "workspace_denied"
	case "credential.policy", "credential.scope", "provider_config.policy":
		return "provider_config_denied"
	case "network.provider", "network.parent":
		return "network_denied"
	}
	return ""
}

// settingsPolicyFailure preserves the lifecycle builder's existing error text
// while exposing only the denied identity to the shared policy evaluator.
type settingsPolicyFailure struct{ path string }

func (*settingsPolicyFailure) Error() string {
	return "settings file is outside the session filesystem policy"
}

// ProjectLaunchFailure consumes the existing preparation checks without a
// second assessor, filesystem discovery, provider probe, or raw error parsing.
func ProjectLaunchFailure(err error, policy *LaunchPolicySelection, prepared PreparedLaunch, stage, reason string) error {
	d := control.LaunchDiagnostic{Stage: stage, Reason: reason}
	if prepared.FailureReason != "" {
		d.Reason = prepared.FailureReason
	}
	if policy != nil {
		d.PolicyOrigin, d.PolicyPath = policy.Origin, policy.Path
	}
	for _, check := range prepared.Checks {
		if classified := launchFailureReason(check.Code); check.Status == LaunchFail && classified != "" {
			d.Stage, d.Reason, d.PolicySource = "admission/policy", classified, check.Source
			break
		}
	}
	return control.WithLaunchDiagnostic(err, d)
}

const (
	LaunchPass  = "PASS"
	LaunchFail  = "FAIL"
	LaunchUnrun = "REQUIRED-UNRUN"
)

// LaunchFact states are declarations from a captured input, never native probes.
// Empty/unknown stays unknown, even when every other fixture is green.
type LaunchFact string

const (
	FactUnknown     LaunchFact = "unknown"
	FactSupported   LaunchFact = "supported"
	FactUnsupported LaunchFact = "unsupported"
)

type LaunchCapabilities struct {
	OS                   string     `json:"os"`
	Network              string     `json:"network"` // unknown, none, proxy-only, full
	WorkspaceWritable    LaunchFact `json:"workspace_writable"`
	ProviderHomeWritable LaunchFact `json:"provider_home_writable"`
	NativeSandbox        LaunchFact `json:"native_sandbox"`
	ParentSandbox        string     `json:"parent_sandbox"` // unknown, none, sandboxed, scoped
	UserBus              LaunchFact `json:"user_bus"`
	RequiresUserBus      bool       `json:"requires_user_bus"`
	ElanHome             string     `json:"elan_home,omitempty"` // explicit inherited path only
	ElanHomeWritable     LaunchFact `json:"elan_home_writable"`
	RawSocketsRequired   bool       `json:"raw_sockets_required"`
}

// LaunchProviderEvidence is saved version/help output from the selected executable.
// A digest binds it to today's executable, but still does not prove authentication,
// hooks, a provider tool call, or OS confinement. Help is never exported.
type LaunchProviderEvidence struct {
	BinarySHA256  string `json:"binary_sha256"`
	VersionOutput string `json:"version_output"`
	HelpOutput    string `json:"help_output"`
	CompleteHelp  bool   `json:"complete_help"`
}

func validLaunchFact(value LaunchFact) bool {
	return value == "" || value == FactUnknown || value == FactSupported || value == FactUnsupported
}
func (c LaunchCapabilities) Validate() error {
	if c.OS != "darwin" && c.OS != "linux" {
		return errors.New("capabilities os must be darwin or linux")
	}
	for _, value := range []LaunchFact{c.WorkspaceWritable, c.ProviderHomeWritable, c.NativeSandbox, c.UserBus, c.ElanHomeWritable} {
		if !validLaunchFact(value) {
			return errors.New("invalid capability state")
		}
	}
	switch c.Network {
	case "", "unknown", "none", "proxy-only", "full":
	default:
		return errors.New("invalid capability network")
	}
	switch c.ParentSandbox {
	case "", "unknown", "none", "sandboxed", "scoped":
	default:
		return errors.New("invalid parent sandbox")
	}
	if c.ElanHome != "" && !filepath.IsAbs(c.ElanHome) {
		return errors.New("inherited elan_home must be absolute")
	}
	return nil
}

// LaunchRequirements carries explicit caller constraints and captured host
// facts. Nil means facts were not supplied, never permission to probe the host.
// Preparation returns these diagnoses on both native and mirrored state.
type LaunchRequirements struct {
	Route, AllowedHomeRoot, ProviderStateDir   string
	CredentialPaths                            []string
	Capabilities                               *LaunchCapabilities
	CapabilitySource, PolicySource, PolicyBase string
	Rules                                      []PolicyRuleSource
	Evidence                                   *LaunchProviderEvidence
	EvidenceBound, VersionKnown                bool
	policyHome                                 string
}

type LaunchCheck struct {
	Code       string `json:"code"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Source     string `json:"source"`
	Correction string `json:"correction,omitempty"`
}

func (p *PreparedLaunch) check(code, status, reason, source, correction string) {
	p.Checks = append(p.Checks, LaunchCheck{code, status, reason, source, correction})
}

func (p *PreparedLaunch) assess(rc RunConfig, opts LaunchPreparationOptions, state LaunchState, complete bool) {
	req := LaunchRequirements{}
	if opts.Requirements != nil {
		req = *opts.Requirements
	}
	req.policyHome = opts.HostHome
	if rc.UserHome != "" {
		req.policyHome = rc.UserHome
	}
	if req.PolicyBase == "" {
		req.PolicyBase = rc.CWD
	}
	if rc.Agent != "" {
		p.check("authentication", LaunchUnrun, "credential path availability does not prove provider authentication", "no credential contents or login probe", "use the separately authorized disposable provider canary")
	}
	for _, rule := range req.Rules {
		if rule.Section == "env_invalid" {
			p.check("environment.selector", LaunchUnrun, "environment selector contains value-like text; provenance redacted; runtime selects literal variable names", rule.Source, "review variable-name selectors locally without exporting credential values")
		}
	}
	caps := LaunchCapabilities{}
	if req.Capabilities != nil {
		caps = *req.Capabilities
	}
	platform := opts.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	p.Transport = sandbox.LaunchTransport(p.Sandbox.NetworkNeed, platform, p.Sandboxed)
	source := req.CapabilitySource
	if source == "" {
		source = "no saved capability facts"
	}
	if req.AllowedHomeRoot != "" {
		p.scope("home.scope", opts.Home, req.AllowedHomeRoot, "provider home", "explicit allowed_home_root + canonical paths")
	}
	for _, credential := range req.CredentialPaths {
		p.scope("credential.scope", credential, opts.Home, "credential path", "canonical credential path")
	}
	if p.Sandboxed {
		p.readCheck("executable.policy", opts.Binary, "selected executable", opts, platform)
		for _, credential := range req.CredentialPaths {
			p.readCheck("credential.policy", credential, "declared credential path", opts, platform)
		}
	}
	if p.NetworkFailure != "" {
		p.check("network.provider", LaunchFail, p.NetworkFailure, "runtime preparation + enforcing proxy matcher", "review declared domains while preserving the isolation boundary")
	}
	if req.Capabilities != nil {
		if caps.Network == "none" && p.Transport != "none" {
			p.check("network.parent", LaunchFail, "outer network-none policy prevents provider traffic even when egg policy allows it", source, "run the disposable acceptance under a parent that permits the configured provider proxy")
		}
		if caps.Network == "" || caps.Network == "unknown" {
			p.check("network.parent", LaunchUnrun, "effective outer network capability is unknown", source, "capture the parent network policy outside the worker")
		}
		if caps.RawSocketsRequired && (p.Transport != "full" || caps.Network == "proxy-only") {
			p.check("network.raw_sockets", LaunchFail, "required raw sockets cannot use the planned proxy or TCP relay", source+" + effective transport", "use a provider/tool transport compatible with the configured proxy boundary")
		}
		if platform == "linux" && caps.ParentSandbox == "scoped" {
			p.check("parent.scoped", LaunchFail, "Linux scoped parent enforcement is unsupported", source, "retain the existing supported parent boundary; do not downgrade isolation")
		}
		if p.Sandboxed {
			p.factCheck("sandbox.native", caps.NativeSandbox, "native egg confinement has not been verified", "declared host cannot create the required egg sandbox", source, "run the isolated native sandbox gate outside the worker")
		}
		if caps.RequiresUserBus {
			p.factCheck("user_bus", caps.UserBus, "this requested launch requires a user bus but its availability is unknown", "requested bus-dependent mode has no user bus", source, "use an explicitly available bus for that mode")
		}
	}
	if p.Sandboxed && len(p.Policy.LocalPorts) > 0 {
		p.check("network.loopback", LaunchUnrun, "declared host-loopback TCP forwards require native relay verification", "effective local_ports", "verify the precise declared ports with disposable native acceptance")
	}
	if rc.CWD != "" {
		p.writeCheck("workspace.write", rc.CWD, state, caps.WorkspaceWritable, source, req, platform)
	}
	for _, dir := range p.ProfileDirs {
		if req.AllowedHomeRoot != "" {
			p.scope("provider_home.scope", dir, req.AllowedHomeRoot, "required provider state", "runtime prepared profile directory + explicit allowed_home_root")
		}
		p.writeCheck("provider_home.write", dir, state, caps.ProviderHomeWritable, source, req, platform)
	}
	if req.ProviderStateDir != "" {
		if req.AllowedHomeRoot != "" {
			p.scope("provider_home.scope", req.ProviderStateDir, req.AllowedHomeRoot, "explicit provider state override", "explicit provider_state_dir")
		}
		p.writeCheck("provider_state.write", req.ProviderStateDir, state, caps.ProviderHomeWritable, source, req, platform)
	}
	// Selected environment comes from the same helper used by RunSession; the
	// supplied candidate is diagnostic metadata and never an ambient read.
	if caps.ElanHome != "" {
		if p.Environment["ELAN_HOME"] == "" {
			p.check("environment.elan_home.filtered", LaunchPass, "declared inherited ELAN_HOME is discarded by the launch environment selection", req.PolicySource, "")
		} else {
			p.writeCheck("environment.elan_home", p.Environment["ELAN_HOME"], state, caps.ElanHomeWritable, source, req, platform)
		}
	} else if path := p.Environment["ELAN_HOME"]; path != "" {
		p.writeCheck("environment.elan_home", path, state, FactUnknown, source, req, platform)
	}
	if !complete {
		return
	}
	if rc.Agent == "codex" && len(rc.Command) == 0 && !containsExact(p.Args, "--dangerously-bypass-approvals-and-sandbox") && (p.Sandboxed || caps.ParentSandbox == "sandboxed" || caps.ParentSandbox == "scoped") {
		p.check("sandbox.nested", LaunchFail, "planned Codex invocation retains an inner sandbox inside a declared outer sandbox", "runtime adapter argv + declared parent boundary", "review the supported unattended egg route under the existing boundary; planner does not change flags")
	}
	if req.Route == "agent_run" {
		switch {
		case rc.Agent == "codex" && !req.VersionKnown:
			p.check("route.agent_run", LaunchUnrun, "Codex native run compatibility requires bound version evidence", "runtime Codex version parser", "")
		case !p.NativeRun:
			reason := "this adapter has no native supervised run contract"
			if rc.Agent == "codex" {
				reason = "saved Codex version does not support the native hooks required by agent_run"
			}
			p.check("route.agent_run", LaunchFail, reason, "runtime nativeRunSupported", "use agent_start or separately select a verified compatible installed executable")
		default:
			p.check("route.agent_run", LaunchPass, "runtime adapter declares native run support for the supplied version evidence", "runtime nativeRunSupported; fixture compatibility only", "")
		}
	}
	if req.Evidence != nil && len(rc.Command) == 0 {
		p.checkOptions(p.Args, *req.Evidence, req.EvidenceBound)
	}
}

func launchWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (p *PreparedLaunch) scope(code, path, root, label, source string) {
	real, pathErr := sandbox.CanonicalPath(path)
	base, rootErr := sandbox.CanonicalPath(root)
	if pathErr != nil || rootErr != nil {
		p.check(code, LaunchFail, label+" scope cannot be resolved", source, "select a disposable path inside the declared scope")
	} else if !launchWithin(real, base) {
		p.check(code, LaunchFail, label+" resolves outside the explicitly allowed scope", source, "select a disposable path inside the declared scope")
	} else if code == "home.scope" {
		p.check(code, LaunchPass, "provider home is within the declared scope", source, "")
	}
}

func (p *PreparedLaunch) readCheck(code, path, label string, opts LaunchPreparationOptions, platform string) {
	if path == "" {
		return
	}
	check := sandbox.CheckFileReads(p.Sandbox, path, platform)
	source := "final sandbox read policy"
	if opts.Requirements != nil && check.DenialPath != "" {
		req := opts.Requirements
		policyHome := opts.HostHome
		if p.Sandbox.UserHome != "" {
			policyHome = p.Sandbox.UserHome
		}
		source = PolicyDenialSources("deny:", check.DenialPath, req.Rules, req.PolicyBase, policyHome)
	}
	if !check.Verified {
		p.check(code, LaunchUnrun, check.Reason, source, "verify actual platform rules under the preserved boundary")
	} else if !check.Allowed {
		p.check(code, LaunchFail, "effective deny rule blocks the "+label+": "+check.Reason, source, "select a path allowed by the configured policy")
	}
}

func (p *PreparedLaunch) factCheck(code string, fact LaunchFact, unknown, denied, source, correction string) {
	switch fact {
	case FactSupported:
		p.check(code, LaunchPass, "declared capability supported; native behavior remains external acceptance", source, "")
	case FactUnsupported:
		p.check(code, LaunchFail, denied, source, correction)
	default:
		p.check(code, LaunchUnrun, unknown, source, correction)
	}
}
func (p *PreparedLaunch) writeCheck(code, path string, state LaunchState, fact LaunchFact, source string, req LaunchRequirements, goos string) {
	var err error
	path, err = sandbox.CanonicalPath(path)
	if err != nil {
		p.check(code, LaunchFail, "required directory cannot be resolved", source, "select a valid disposable path")
		return
	}
	if err := state.DirectoryLayout(path); err != nil {
		p.check(code, LaunchFail, "required directory cannot be prepared: "+err.Error(), "runtime preparation in disposable state", "select a valid disposable directory layout")
		return
	}
	check := sandbox.DirectoryWriteCheck{Allowed: true, Verified: true}
	if p.Sandboxed {
		check = sandbox.CheckDirectoryWrites(p.Sandbox, path, goos)
	}
	if check.DenialPath != "" {
		source = PolicyDenialSources(check.DenialKind+":", check.DenialPath, req.Rules, req.PolicyBase, req.policyHome)
	}
	if !check.Verified {
		p.check(code, LaunchUnrun, check.Reason, source, "verify actual platform rules under the preserved boundary")
		return
	}
	if !check.Allowed {
		p.check(code, LaunchFail, check.Reason, source, "select an allowed disposable path; do not auto-widen policy")
		return
	}
	p.factCheck(code, fact, "outer filesystem writability is unknown for required path: "+path, "outer read-only filesystem prevents required writes: "+path, source, "select a writable disposable path within the existing allowed scope")
}

// IsLaunchOption recognizes the same option syntax for assessment and display.
func IsLaunchOption(flag string) bool { return optionRE.MatchString(flag) }

var optionRE = regexp.MustCompile(`^(--[a-zA-Z][a-zA-Z0-9-]*|-[a-zA-Z])$`)

func (p *PreparedLaunch) checkOptions(args []string, saved LaunchProviderEvidence, bound bool) {
	options := map[string]bool{}
	for _, line := range strings.Split(saved.HelpOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-") {
			continue
		}
		for _, token := range strings.Fields(line) {
			flag, _, _ := strings.Cut(strings.TrimSuffix(token, ","), "=")
			if !optionRE.MatchString(flag) {
				break
			}
			options[flag] = true
		}
	}
	seen := map[string]bool{}
	for _, arg := range args[:providerOptionsEnd(args)] {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		flag, _, _ := strings.Cut(arg, "=")
		if flag == "--" || seen[flag] {
			continue
		}
		seen[flag] = true
		status, reason := LaunchUnrun, "planned option has no bound saved help evidence: "+flag
		if bound && options[flag] {
			status, reason = LaunchPass, "planned option appears in bound saved help: "+flag
		} else if bound && saved.CompleteHelp {
			status, reason = LaunchFail, "planned option is absent from complete bound saved help: "+flag
		}
		p.check("provider.option", status, reason, "runtime argv + saved help", "capture help for the same executable and route; do not change sandbox flags automatically")
	}
}
