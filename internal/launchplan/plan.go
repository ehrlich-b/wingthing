// Package launchplan explains a proposed launch using explicit, read-only
// inputs. It has no process, network, login, daemon, or policy mutation path.
package launchplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

const (
	Pass  = "PASS"
	Fail  = "FAIL"
	Unrun = "REQUIRED-UNRUN"
)

// Fact states are declarations from a captured input, never native probes.
// Empty/unknown stays unknown, even when every other fixture is green.
type Fact string

const (
	Unknown     Fact = "unknown"
	Supported   Fact = "supported"
	Unsupported Fact = "unsupported"
)

type Capabilities struct {
	OS                   string `json:"os"`
	Network              string `json:"network"` // unknown, none, proxy-only, full
	WorkspaceWritable    Fact   `json:"workspace_writable"`
	ProviderHomeWritable Fact   `json:"provider_home_writable"`
	NativeSandbox        Fact   `json:"native_sandbox"`
	ParentSandbox        string `json:"parent_sandbox"` // unknown, none, sandboxed, scoped
	UserBus              Fact   `json:"user_bus"`
	RequiresUserBus      bool   `json:"requires_user_bus"`
	ElanHome             string `json:"elan_home,omitempty"` // explicit inherited path only
	ElanHomeWritable     Fact   `json:"elan_home_writable"`
	RawSocketsRequired   bool   `json:"raw_sockets_required"`
}

// ProviderEvidence is saved version/help output from the selected executable.
// A digest binds it to today's executable, but still does not prove authentication,
// hooks, a provider tool call, or OS confinement. Help is never exported.
type ProviderEvidence struct {
	BinarySHA256  string `json:"binary_sha256"`
	VersionOutput string `json:"version_output"`
	HelpOutput    string `json:"help_output"`
	CompleteHelp  bool   `json:"complete_help"`
}

type Request struct {
	Route            string
	Agent            string
	CWD              string
	PathBase         string // required only for relative CWD; never ambient Getwd
	Home             string
	AllowedHomeRoot  string
	Executable       string // explicit absolute path; never LookPath
	ProviderURL      string // explicit routing metadata; only derived host is exported
	ProviderStateDir string // explicit CODEX_HOME/etc override, never ambient
	PolicyPath       string // empty explicitly chooses built-in defaults
	BasesDir         string
	CapabilitiesPath string
	EvidencePath     string
	CredentialPaths  []string // stat metadata only; contents never opened
	Unattended       bool
}

type Gate struct {
	Code       string `json:"code"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Source     string `json:"source"`
	Correction string `json:"correction,omitempty"`
}
type PathEvidence struct {
	Path      string `json:"path"`               // cleaned display path, retaining aliases
	Resolved  string `json:"resolved,omitempty"` // canonical identity for filesystem checks
	Available bool   `json:"available"`
	Source    string `json:"source"`
}
type Report struct {
	Acceptance         string                 `json:"acceptance"`
	ProviderVersion    string                 `json:"provider_version,omitempty"`
	SchemaVersion      int                    `json:"schema_version"`
	ReadOnly           bool                   `json:"read_only"`
	Readiness          string                 `json:"readiness"`
	Route              string                 `json:"route"`
	Agent              string                 `json:"agent,omitempty"`
	CWD                PathEvidence           `json:"cwd"`
	Home               PathEvidence           `json:"home"`
	Executable         PathEvidence           `json:"executable"`
	ExecutableSHA256   string                 `json:"executable_sha256,omitempty"`
	EvidenceSHA256     string                 `json:"evidence_sha256,omitempty"`
	CapabilitiesSHA256 string                 `json:"capabilities_sha256,omitempty"`
	PolicySource       string                 `json:"policy_source"`
	PolicyRules        []egg.PolicyRuleSource `json:"policy_rules"`
	Network            string                 `json:"network"`
	Domains            []string               `json:"domains"`
	LocalPorts         []int                  `json:"local_ports"`
	ProviderArgv       []string               `json:"provider_argv"`
	ArgvScope          string                 `json:"argv_scope"`
	Credentials        []PathEvidence         `json:"credentials"`
	Provenance         map[string]string      `json:"provenance"`
	Gates              []Gate                 `json:"gates"`
}

func (r *Report) gate(code, status, reason, source, correction string) {
	r.Gates = append(r.Gates, Gate{code, status, reason, source, correction})
	if status == Fail {
		r.Readiness = "blocked"
	} else if status == Unrun && r.Readiness != "blocked" {
		r.Readiness = "unverified"
	}
}
func validFact(value Fact) bool {
	return value == "" || value == Unknown || value == Supported || value == Unsupported
}
func (c Capabilities) validate() error {
	if c.OS != "darwin" && c.OS != "linux" {
		return errors.New("capabilities os must be darwin or linux")
	}
	for _, value := range []Fact{c.WorkspaceWritable, c.ProviderHomeWritable, c.NativeSandbox, c.UserBus, c.ElanHomeWritable} {
		if !validFact(value) {
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

// Inspect reads only named policy/evidence inputs and path metadata. Required
// facts and absent paths become gates. Malformed contract inputs fail closed.
func Inspect(req Request) (Report, error) {
	report := Report{SchemaVersion: 1, ReadOnly: true, Acceptance: "externally-pending", Readiness: "ready-local", Route: req.Route, Agent: req.Agent, Provenance: map[string]string{
		"route": "explicit request", "agent": "explicit request", "cwd": "explicit request (relative paths require path_base)",
		"home": "explicit request", "executable": "explicit path, metadata and digest only", "credentials": "explicit paths, stat only; availability is not authentication",
		"policy": "production parser/merge and agent profile; explicit policy or built-in defaults", "capabilities": "explicit saved facts; no native probe", "argv": "production interactive catalog and egg builders; no prompt or passthrough",
	}}
	switch req.Route {
	case "terminal_start", "agent_start", "agent_run":
	default:
		return report, errors.New("route must be terminal_start, agent_start, or agent_run")
	}
	if req.Route == "terminal_start" && req.Agent != "" {
		return report, errors.New("terminal_start is a command session; omit agent")
	}
	if req.Route != "terminal_start" {
		if _, ok := agent.LookupDefinition(req.Agent); !ok {
			return report, errors.New("explicit supported agent required")
		}
	}
	for _, path := range []string{req.Home, req.AllowedHomeRoot, req.Executable} {
		if !filepath.IsAbs(path) {
			return report, errors.New("explicit absolute home, allowed home root, and executable required")
		}
	}
	if req.CWD == "" {
		return report, errors.New("explicit cwd required")
	}
	cwd := req.CWD
	if !filepath.IsAbs(cwd) {
		if !filepath.IsAbs(req.PathBase) {
			return report, errors.New("relative cwd requires absolute path_base")
		}
		cwd = filepath.Join(req.PathBase, cwd)
	}
	report.CWD = inspectPath(cwd, "explicit cwd")
	report.Home = inspectPath(req.Home, "explicit home")
	report.Executable = inspectPath(req.Executable, "explicit executable")
	info, err := os.Stat(report.CWD.Resolved)
	if err != nil || !info.IsDir() {
		report.gate("cwd", Fail, "proposed cwd is unavailable or not a directory", "path metadata", "select an existing workspace inside the configured scope")
	} else {
		report.gate("cwd", Pass, "cwd directory exists; writability is assessed separately", "path metadata", "")
	}
	homeRoot, err := canonical(req.AllowedHomeRoot)
	if err != nil {
		return report, errors.New("allowed home root cannot be resolved")
	}
	if !within(report.Home.Resolved, homeRoot) {
		report.gate("home.scope", Fail, "provider home resolves outside the explicitly allowed scope", "explicit allowed_home_root + canonical paths", "select a disposable provider home within the allowed scope; do not copy ambient credentials")
	} else {
		report.gate("home.scope", Pass, "provider home is within the declared scope", "explicit allowed_home_root + canonical paths", "")
	}
	if report.Executable.Available {
		info, err := os.Stat(report.Executable.Resolved)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			report.ExecutableSHA256, err = hashExecutable(report.Executable.Resolved)
			if err != nil {
				report.gate("executable", Unrun, "executable digest unavailable or executable exceeds 512 MiB", "explicit executable", "")
			} else {
				report.gate("executable", Pass, "explicit executable is present and executable; it was not invoked", "path metadata + SHA256", "")
			}
		} else {
			report.gate("executable", Fail, "selected executable is not an executable regular file", "path metadata", "select an existing provider executable without installing or replacing it")
		}
	} else {
		report.gate("executable", Fail, "selected executable is missing", "explicit executable metadata", "select an existing executable; planner never searches PATH")
	}
	for _, path := range req.CredentialPaths {
		if !filepath.IsAbs(path) {
			return report, errors.New("credential paths must be absolute")
		}
		entry := inspectPath(path, "explicit credential path; stat only")
		report.Credentials = append(report.Credentials, entry)
		if !within(entry.Resolved, report.Home.Resolved) {
			report.gate("credential.scope", Fail, "credential path resolves outside the selected provider home", "canonical credential path", "select authentication already provisioned inside the declared disposable home")
		}
		info, err := os.Stat(entry.Resolved)
		if err != nil || !info.Mode().IsRegular() {
			report.gate("credential.path", Unrun, "declared credential file is absent or not regular; no contents read", "path metadata", "provision disposable authentication separately if required")
		}
	}
	if req.Agent != "" {
		report.gate("authentication", Unrun, "credential path availability does not prove provider authentication", "no credential contents or login probe", "use the separately authorized disposable provider canary")
	}
	cfg, rules, err := egg.ResolveLaunchConfig(req.PolicyPath, req.BasesDir, req.Home)
	if err != nil {
		return report, err
	}
	report.PolicyRules = rules
	for _, rule := range rules {
		if rule.Section == "env_invalid" {
			report.gate("environment.selector", Unrun, "environment selector contains value-like text; provenance redacted; runtime selects literal variable names", rule.Source, "review variable-name selectors locally without exporting credential values")
		}
	}
	report.PolicySource = req.PolicyPath
	if req.PolicyPath == "" {
		report.PolicySource = "built-in defaults (explicit selection)"
	}
	caps := Capabilities{}
	capSource := "no saved capability facts"
	if req.CapabilitiesPath != "" {
		report.CapabilitiesSHA256, err = readSaved(req.CapabilitiesPath, &caps)
		if err != nil {
			return report, err
		}
		if err = caps.validate(); err != nil {
			return report, err
		}
		capSource = "declared capability facts sha256:" + report.CapabilitiesSHA256
	}
	// Canonicalize all paths before interpreting denials, including missing leaves.
	resolved := *cfg
	resolved.FS = append([]string(nil), cfg.FS...)
	for i, entry := range cfg.FS {
		mode, path, ok := strings.Cut(entry, ":")
		if !ok {
			mode, path = "rw", entry
		}
		if path == "~" {
			path = req.Home
		} else if strings.HasPrefix(path, "~/") {
			path = filepath.Join(req.Home, path[2:])
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(report.CWD.Resolved, path)
		}
		real, err := canonical(path)
		if err != nil {
			return report, errors.New("policy filesystem path cannot be resolved")
		}
		resolved.FS[i] = mode + ":" + real
	}
	policy, err := egg.ResolvePolicyWithProvider(&resolved, req.Agent, report.Home.Resolved, req.ProviderURL)
	if err != nil {
		return report, errors.New("effective provider policy cannot be resolved")
	}
	for _, denied := range policy.Deny {
		if within(report.Executable.Resolved, denied) && denied != "/" {
			report.gate("executable.policy", Fail, "effective deny rule blocks the selected executable", denialSources("deny:", denied, rules, report.CWD.Resolved, report.Home.Resolved), "select an executable allowed by the configured policy")
		}
		for _, credential := range report.Credentials {
			if within(credential.Resolved, denied) && denied != "/" {
				report.gate("credential.policy", Fail, "effective deny rule blocks the declared credential path", denialSources("deny:", denied, rules, report.CWD.Resolved, report.Home.Resolved), "select separately provisioned authentication within the allowed scope")
			}
		}
	}
	report.Domains = policy.Domains
	report.LocalPorts = policy.LocalPorts
	report.Network = transport(policy.NetworkNeed, caps.OS, egg.RequiresSandbox(cfg, req.Agent))
	for _, hole := range policy.Drilled {
		report.PolicyRules = append(report.PolicyRules, egg.PolicyRuleSource{Section: hole.Kind, Rule: hole.Value, Source: "agent profile: " + hole.Agent})
	}
	for _, hole := range policy.Derived {
		report.PolicyRules = append(report.PolicyRules, egg.PolicyRuleSource{Section: hole.Kind, Rule: hole.Value, Source: hole.Reason})
	}
	for _, hole := range policy.Suppressed {
		report.PolicyRules = append(report.PolicyRules, egg.PolicyRuleSource{Section: "suppressed_" + hole.Kind, Rule: hole.Value, Source: hole.Reason})
	}
	if req.Agent != "" && policy.NetworkNeed == sandbox.NetworkNone && len(policy.LocalPorts) == 0 {
		report.gate("network.provider", Fail, "effective policy provides no route to the selected provider", "effective policy + suppressed agent domains", "review declared domains while preserving the isolation boundary")
	}
	if caps.Network == "none" && report.Network != "none" {
		report.gate("network.parent", Fail, "outer network-none policy prevents provider traffic even when egg policy allows it", capSource, "run the disposable acceptance under a parent that permits the configured provider proxy")
	}
	if caps.Network == "" || caps.Network == "unknown" {
		report.gate("network.parent", Unrun, "effective outer network capability is unknown", capSource, "capture the parent network policy outside the worker")
	}
	if caps.RawSocketsRequired && (report.Network != "full" || caps.Network == "proxy-only") {
		report.gate("network.raw_sockets", Fail, "required raw sockets cannot use the planned proxy or TCP relay", capSource+" + effective transport", "use a provider/tool transport compatible with the configured proxy boundary")
	}
	if len(policy.LocalPorts) > 0 {
		report.gate("network.loopback", Unrun, "declared host-loopback TCP forwards require native relay verification", "effective local_ports", "verify the precise declared ports with disposable native acceptance")
	}
	report.pathWriteGate("workspace.write", report.CWD.Resolved, policy, caps.WorkspaceWritable, capSource, rules, caps.OS)
	if req.Agent != "" {
		profile := egg.Profile(req.Agent)
		for _, dir := range append(append([]string(nil), profile.WriteRegex...), profile.WriteDirs...) {
			path, err := canonical(filepath.Join(report.Home.Resolved, dir))
			if err != nil {
				return report, errors.New("provider state path cannot be resolved")
			}
			report.pathWriteGate("provider_home.write", path, policy, caps.ProviderHomeWritable, capSource, rules, caps.OS)
		}
	}
	if req.ProviderStateDir != "" {
		if !filepath.IsAbs(req.ProviderStateDir) {
			return report, errors.New("provider state dir must be absolute")
		}
		state, err := canonical(req.ProviderStateDir)
		if err != nil {
			return report, errors.New("provider state dir cannot be resolved")
		}
		if !within(state, homeRoot) {
			report.gate("provider_home.scope", Fail, "explicit provider state override resolves outside allowed scope", "explicit provider_state_dir", "select an existing disposable state directory inside the configured scope")
		}
		report.pathWriteGate("provider_state.write", state, policy, caps.ProviderHomeWritable, capSource, rules, caps.OS)
	}
	if caps.ElanHome != "" {
		path, err := canonical(caps.ElanHome)
		if err != nil {
			return report, errors.New("inherited elan_home cannot be resolved")
		}
		report.pathWriteGate("environment.elan_home", path, policy, caps.ElanHomeWritable, capSource, rules, caps.OS)
		if !cfg.IsAllEnv() && !contains([]string(cfg.Env), "ELAN_HOME") {
			report.gate("environment.elan_home.filtered", Unrun, "declared inherited ELAN_HOME is not selected by the policy environment; verify the launch environment", report.PolicySource, "set an explicit toolchain path and private matching cache for the disposable launch")
		}
	}
	if caps.OS == "linux" && caps.ParentSandbox == "scoped" {
		report.gate("parent.scoped", Fail, "Linux scoped parent enforcement is unsupported", capSource, "retain the existing supported parent boundary; do not downgrade isolation")
	}
	sandboxRequired := egg.RequiresSandbox(cfg, req.Agent)
	if sandboxRequired {
		report.factGate("sandbox.native", caps.NativeSandbox, "native egg confinement has not been verified", "declared host cannot create the required egg sandbox", capSource, "run the isolated native sandbox gate outside the worker")
	}
	if caps.RequiresUserBus {
		report.factGate("user_bus", caps.UserBus, "this requested launch requires a user bus but its availability is unknown", "requested bus-dependent mode has no user bus", capSource, "use an explicitly available bus for that mode")
	}
	saved := ProviderEvidence{}
	version := ""
	evidenceBound := false
	if req.EvidencePath != "" {
		report.EvidenceSHA256, err = readSaved(req.EvidencePath, &saved)
		if err != nil {
			return report, err
		}
		if saved.BinarySHA256 != "" && saved.BinarySHA256 == report.ExecutableSHA256 {
			version = saved.VersionOutput
			evidenceBound = true
			if req.Agent == "codex" {
				report.ProviderVersion, _ = egg.CodexVersionFromSavedOutput(version)
				if report.ProviderVersion == "" {
					report.gate("provider.version", Unrun, "saved version output is unrecognized by the production Codex parser", "bound saved evidence", "capture version output from the selected executable outside the worker")
					version = ""
				}
			}
			report.gate("provider.evidence", Pass, "saved evidence is bound to the selected executable digest", "saved evidence sha256:"+report.EvidenceSHA256, "")
		} else {
			report.gate("provider.evidence", Unrun, "saved version/help is not bound to the selected executable digest", "saved evidence sha256:"+report.EvidenceSHA256, "capture bounded version/help and the executable digest outside the worker")
		}
	} else if req.Agent != "" {
		report.gate("provider.evidence", Unrun, "no saved provider version/help evidence was supplied", "explicit evidence input missing", "capture version/help outside the worker; do not invoke a provider from preflight")
	}
	if req.Route == "terminal_start" {
		report.ProviderArgv = []string{report.Executable.Resolved}
		report.ArgvScope = "exact command executable; no agent auto-drilling, prompt, or passthrough arguments"
	} else {
		argv, native, err := egg.LaunchCommandPreview(req.Agent, report.CWD.Resolved, report.Home.Resolved, version, req.Unattended || cfg.DangerouslySkipPermissions)
		if err != nil {
			return report, err
		}
		argv[0] = report.Executable.Resolved
		report.ProviderArgv = argv
		if req.Agent == "codex" && !contains(argv, "--dangerously-bypass-approvals-and-sandbox") && (sandboxRequired || caps.ParentSandbox == "sandboxed" || caps.ParentSandbox == "scoped") {
			report.gate("sandbox.nested", Fail, "planned Codex invocation retains an inner sandbox inside a declared outer sandbox", "runtime adapter argv + declared parent boundary", "review the supported unattended egg route under the existing boundary; planner does not change flags")
		}
		report.ArgvScope = "runtime adapter arguments; disposable identity launch-plan; prompt excluded"
		if req.Agent == "claude" {
			report.ArgvScope += "; generated lifecycle settings/session IDs are deferred to launch"
			report.gate("provider.lifecycle_argv", Unrun, "Claude lifecycle settings require launch-time generation; base argv only", "runtime Claude lifecycle builder", "verify generated lifecycle settings in disposable native acceptance")
		}
		if req.Route == "agent_run" {
			if req.Agent != "claude" && req.Agent != "codex" {
				report.gate("route.agent_run", Fail, "this adapter has no native supervised run contract", "runtime nativeRunSupported", "select an adapter with native run evidence, or use agent_start for a terminal")
			} else if req.Agent == "codex" && version == "" {
				report.gate("route.agent_run", Unrun, "Codex native run compatibility requires bound version evidence", "runtime Codex version parser", "")
			} else if !native {
				report.gate("route.agent_run", Fail, "saved Codex version does not support the native hooks required by agent_run", "runtime CodexLifecycleVersionSupported", "use agent_start or separately select a verified compatible installed executable")
			} else {
				report.gate("route.agent_run", Pass, "runtime adapter declares native run support for the supplied version evidence", "runtime nativeRunSupported; fixture compatibility only", "")
			}
		}
		checkOptions(&report, argv, saved, evidenceBound)
	}
	return report, nil
}

func (r *Report) factGate(code string, fact Fact, unknown, denied, source, correction string) {
	switch fact {
	case Supported:
		r.gate(code, Pass, "declared capability supported; native behavior remains external acceptance", source, "")
	case Unsupported:
		r.gate(code, Fail, denied, source, correction)
	default:
		r.gate(code, Unrun, unknown, source, correction)
	}
}
func (r *Report) pathWriteGate(code, path string, policy egg.EffectivePolicy, fact Fact, source string, rules []egg.PolicyRuleSource, goos string) {
	for _, deny := range policy.Deny {
		if within(path, deny) {
			if deny == "/" && goos == "linux" {
				r.gate(code, Unrun, "Linux root-deny policy requires native verification of its mount exceptions", denialSources("deny:", deny, rules, r.CWD.Resolved, r.Home.Resolved), "verify the exact configured mounts under the same boundary")
				return
			}
			r.gate(code, Fail, "effective deny rule blocks the required path: "+path, denialSources("deny:", deny, rules, r.CWD.Resolved, r.Home.Resolved), "select an allowed disposable path; do not auto-widen policy")
			return
		}
	}
	for _, deny := range policy.DenyWrite {
		if path == deny || goos == "linux" && within(path, deny) {
			r.gate(code, Fail, "effective deny-write rule blocks the required path: "+path, denialSources("deny-write:", deny, rules, r.CWD.Resolved, r.Home.Resolved), "select an allowed disposable path")
			return
		}
	}
	granted := false
	for _, mount := range policy.Mounts {
		if !mount.ReadOnly && within(path, mount.Source) {
			granted = true
		}
	}
	if !granted {
		r.gate(code, Unrun, "no declared writable grant for required path: "+path, "effective policy", "verify actual platform rules under the preserved boundary")
		return
	}
	r.factGate(code, fact, "outer filesystem writability is unknown for required path: "+path, "outer read-only filesystem prevents required writes: "+path, source, "select a writable disposable path within the existing allowed scope")
}
func denialSources(prefix, deny string, rules []egg.PolicyRuleSource, cwd, home string) string {
	var sources []string
	for _, rule := range rules {
		path, ok := strings.CutPrefix(rule.Rule, prefix)
		if rule.Section != "fs" || !ok {
			continue
		}
		if path == "~" {
			path = home
		} else if strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, path[2:])
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		real, err := canonical(path)
		if err == nil && real == deny && !contains(sources, rule.Source) {
			sources = append(sources, rule.Source)
		}
	}
	if len(sources) == 0 {
		return "effective policy"
	}
	return strings.Join(sources, ", ")
}
func transport(need sandbox.NetworkNeed, goos string, sandboxed bool) string {
	if !sandboxed {
		return "full"
	}
	switch need {
	case sandbox.NetworkNone:
		return "none"
	case sandbox.NetworkLocal:
		return "loopback-tcp"
	case sandbox.NetworkFull:
		if goos == "darwin" {
			return "full"
		}
		return "proxy-only"
	default:
		return "proxy-only"
	}
}

var optionRE = regexp.MustCompile(`^(--[a-zA-Z][a-zA-Z0-9-]*|-[a-zA-Z])$`)

func checkOptions(report *Report, argv []string, saved ProviderEvidence, bound bool) {
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
	for _, arg := range argv[1:] {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		flag, _, _ := strings.Cut(arg, "=")
		if flag == "--" || seen[flag] {
			continue
		}
		seen[flag] = true
		status, reason := Unrun, "planned option has no bound saved help evidence: "+flag
		if bound && options[flag] {
			status, reason = Pass, "planned option appears in bound saved help: "+flag
		} else if bound && saved.CompleteHelp {
			status, reason = Fail, "planned option is absent from complete bound saved help: "+flag
		}
		report.gate("provider.option", status, reason, "runtime argv + saved help", "capture help for the same executable and route; do not change sandbox flags automatically")
	}
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func within(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
func canonical(path string) (string, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", errors.New("absolute path required")
	}
	real, err := filepath.EvalSymlinks(path)
	if err == nil {
		return real, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err := canonical(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}
func inspectPath(path, source string) PathEvidence {
	entry := PathEvidence{Path: filepath.Clean(path), Source: source}
	entry.Resolved, _ = canonical(path)
	_, err := os.Stat(path)
	entry.Available = err == nil
	return entry
}
func readSaved(path string, destination any) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("saved inputs must have explicit absolute paths")
	}
	file, err := fsutil.OpenBoundedRegular(path, 64<<10)
	if err != nil {
		return "", errors.New("saved input cannot be opened")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return "", errors.New("saved input must be a regular file at most 64 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return "", errors.New("saved input exceeded its read bound")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return "", errors.New("invalid saved input schema (values redacted)")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", errors.New("saved input must contain one JSON object")
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
func hashExecutable(path string) (string, error) {
	file, err := fsutil.OpenBoundedRegular(path, 512<<20)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, (512<<20)+1))
	if err != nil || size > 512<<20 {
		return "", errors.New("executable digest exceeded its bound")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func WriteJSON(w io.Writer, report Report) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
func Render(w io.Writer, report Report) error {
	version := report.ProviderVersion
	if version == "" {
		version = "unverified"
	}
	var flags []string
	for _, arg := range report.ProviderArgv[1:] {
		flag, _, _ := strings.Cut(arg, "=")
		if optionRE.MatchString(flag) && !contains(flags, flag) {
			flags = append(flags, flag)
		}
	}
	if _, err := fmt.Fprintf(w, "Provider version: %s\nPlanned options: %s (full argv in --json)\n", version, strings.Join(flags, " ")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Launch plan: %s (%s)\nPolicy: %s\nCWD: %s\nExecutable: %s\nNetwork: %s\nArgv scope: %s\n", report.Readiness, report.Route, report.PolicySource, report.CWD.Resolved, report.Executable.Resolved, report.Network, report.ArgvScope); err != nil {
		return err
	}
	for _, gate := range report.Gates {
		if _, err := fmt.Fprintf(w, "%s %s: %s\n  source: %s\n", gate.Status, gate.Code, gate.Reason, gate.Source); err != nil {
			return err
		}
		if gate.Correction != "" {
			if _, err := fmt.Fprintf(w, "  next: %s\n", gate.Correction); err != nil {
				return err
			}
		}
	}
	return nil
}
