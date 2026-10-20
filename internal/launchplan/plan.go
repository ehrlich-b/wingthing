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
	"strings"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/fsutil"
)

const (
	Pass  = egg.LaunchPass
	Fail  = egg.LaunchFail
	Unrun = egg.LaunchUnrun
)

// Shared preparation owns capability, compatibility and policy decisions.
type Fact = egg.LaunchFact

const (
	Unknown     = egg.FactUnknown
	Supported   = egg.FactSupported
	Unsupported = egg.FactUnsupported
)

type Capabilities = egg.LaunchCapabilities
type ProviderEvidence = egg.LaunchProviderEvidence

type Request struct {
	Route            string
	Agent            string
	CWD              string
	PathBase         string // required only for relative CWD; never ambient Getwd
	Home             string
	MergeHome        string // host HOME used during inheritance; empty explicitly uses Home
	PolicyBase       string // configured root for relative FS rules; empty uses proposed CWD
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

type Gate = egg.LaunchCheck
type PathEvidence struct {
	Path      string `json:"path"`               // cleaned display path, retaining aliases
	Resolved  string `json:"resolved,omitempty"` // canonical identity for filesystem checks
	Available bool   `json:"available"`
	Source    string `json:"source"`
}
type Report struct {
	Harness            *egg.ClaudeHarnessRender `json:"harness,omitempty"`
	Acceptance         string                   `json:"acceptance"`
	ProviderVersion    string                   `json:"provider_version,omitempty"`
	SchemaVersion      int                      `json:"schema_version"`
	ReadOnly           bool                     `json:"read_only"`
	SandboxRequired    bool                     `json:"sandbox_required"`
	Readiness          string                   `json:"readiness"`
	Route              string                   `json:"route"`
	Agent              string                   `json:"agent,omitempty"`
	CWD                PathEvidence             `json:"cwd"`
	Home               PathEvidence             `json:"home"`
	Executable         PathEvidence             `json:"executable"`
	ExecutableSHA256   string                   `json:"executable_sha256,omitempty"`
	EvidenceSHA256     string                   `json:"evidence_sha256,omitempty"`
	CapabilitiesSHA256 string                   `json:"capabilities_sha256,omitempty"`
	PolicyBase         PathEvidence             `json:"policy_base"`
	MergeHome          PathEvidence             `json:"merge_home"`
	PolicySource       string                   `json:"policy_source"`
	PolicyRules        []egg.PolicyRuleSource   `json:"policy_rules"`
	Network            string                   `json:"network"`
	Domains            []string                 `json:"domains"`
	LocalPorts         []int                    `json:"local_ports"`
	ProviderArgv       []string                 `json:"provider_argv"`
	ArgvScope          string                   `json:"argv_scope"`
	Credentials        []PathEvidence           `json:"credentials"`
	Provenance         map[string]string        `json:"provenance"`
	Gates              []Gate                   `json:"gates"`
}

func (r *Report) gate(code, status, reason, source, correction string) {
	r.Gates = append(r.Gates, Gate{Code: code, Status: status, Reason: reason, Source: source, Correction: correction})
	if status == Fail {
		r.Readiness = "blocked"
	} else if status == Unrun && r.Readiness != "blocked" {
		r.Readiness = "unverified"
	}
}

// Inspect reads only named policy/evidence inputs and path metadata. Required
// facts and absent paths become gates. Malformed contract inputs fail closed.
func Inspect(req Request) (Report, error) {
	report := Report{SchemaVersion: 1, ReadOnly: true, Acceptance: "externally-pending", Readiness: "ready-local", Route: req.Route, Agent: req.Agent, Provenance: map[string]string{
		"route": "explicit request", "agent": "explicit request", "cwd": "explicit request (relative paths require path_base)",
		"home": "explicit request", "executable": "explicit path, metadata and digest only", "credentials": "explicit paths, stat only; availability is not authentication",
		"policy": "shared runtime resolver and agent profile; explicit policy or built-in defaults", "capabilities": "explicit saved facts; no native probe", "argv": "production interactive catalog and egg builders; no prompt or passthrough",
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
		info, err := os.Stat(entry.Resolved)
		if err != nil || !info.Mode().IsRegular() {
			report.gate("credential.path", Unrun, "declared credential file is absent or not regular; no contents read", "path metadata", "provision disposable authentication separately if required")
		}
	}
	mergeHome := req.MergeHome
	mergeSource := "explicit merge_home"
	if mergeHome == "" {
		mergeHome = req.Home
		mergeSource = "explicit home also selected as merge-time host HOME"
	}
	if !filepath.IsAbs(mergeHome) {
		return report, errors.New("merge home must be absolute")
	}
	report.MergeHome = inspectPath(mergeHome, mergeSource)
	policyBase := req.PolicyBase
	baseSource := "explicit configured policy base"
	if policyBase == "" {
		policyBase = report.CWD.Resolved
		baseSource = "proposed cwd selected as direct-launch policy base"
	}
	if !filepath.IsAbs(policyBase) {
		return report, errors.New("policy base must be absolute")
	}
	report.PolicyBase = inspectPath(policyBase, baseSource)
	baseInfo, baseErr := os.Stat(report.PolicyBase.Resolved)
	if baseErr != nil || !baseInfo.IsDir() {
		report.gate("policy.base", Fail, "policy base is unavailable or not a directory", baseSource, "select an existing configured root")
	}
	report.Provenance["merge_home"] = mergeSource
	report.Provenance["policy_base"] = baseSource
	cfg, rules, err := egg.ResolveLaunchConfig(req.PolicyPath, req.BasesDir, mergeHome)
	if err != nil {
		return report, err
	}
	report.PolicyRules = rules
	report.SandboxRequired = egg.RequiresSandbox(cfg, req.Agent)
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
		if err = caps.Validate(); err != nil {
			return report, err
		}
		capSource = "declared capability facts sha256:" + report.CapabilitiesSHA256
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
	resolved := egg.RebaseLaunchConfig(cfg, report.PolicyBase.Resolved)
	// Keep temporary creation outside inspected input trees, even when TMPDIR
	// is under the selected HOME/workspace. This preserves directory mtimes.
	tempParent, err := preparationTempParent(req, report.CWD.Resolved)
	if err != nil {
		return report, err
	}
	root, err := os.MkdirTemp(tempParent, "wt-launch-preflight-*")
	if err != nil {
		return report, errors.New("disposable preparation state unavailable")
	}
	defer os.RemoveAll(root)
	state, err := egg.NewDryLaunchState(root)
	if err != nil {
		return report, err
	}
	rc := egg.RunConfig{
		Harness: cfg.Harness, Agent: req.Agent, CWD: report.CWD.Resolved, UserHome: req.Home, SkipHostAgentEnv: true,
		OuterBoundary: !egg.RequiresSandbox(cfg, req.Agent),
		FS:            resolved.FS, Network: resolved.Network.Domains, LocalPorts: resolved.Network.LocalPorts,
		NetworkMode: resolved.Network.Mode, AgentDomains: resolved.Network.AgentDomains,
		DangerouslySkipPermissions: req.Unattended || cfg.DangerouslySkipPermissions,
		ProviderSessionID:          "launch-plan", OmitBrowserBridge: true,
		CPULimit: cfg.Resources.CPUDuration(), MemLimit: cfg.Resources.MemBytes(),
		MaxFDs: cfg.Resources.MaxFDs, PidLimit: cfg.Resources.MaxPids, Trace: cfg.Trace,
	}
	if req.Route == "terminal_start" {
		rc.Command = []string{report.Executable.Resolved}
	}
	prepared, preparationErr := egg.PrepareLaunch(rc, egg.LaunchPreparationOptions{
		Binary: report.Executable.Resolved, Home: req.Home, HostHome: mergeHome,
		EggDir: filepath.Join(root, "launch-plan"), ProviderURL: req.ProviderURL,
		CodexCapability: egg.SavedCodexCapability(version), Platform: caps.OS, ClaudeVersion: version,
		EnvironmentConfig: cfg,
		Requirements: &egg.LaunchRequirements{
			Route: req.Route, AllowedHomeRoot: req.AllowedHomeRoot,
			CredentialPaths: req.CredentialPaths, ProviderStateDir: req.ProviderStateDir,
			Capabilities: &caps, CapabilitySource: capSource, PolicySource: report.PolicySource,
			Rules: rules, PolicyBase: report.PolicyBase.Resolved,
			Evidence: &saved, EvidenceBound: evidenceBound, VersionKnown: version != "",
		},
	}, state)
	report.Harness = prepared.Harness
	if prepared.Harness != nil {
		report.gate("harness.native", Unrun, "native sandbox runtime conformance has not been executed", "Claude harness driver", "verify the pinned artifact inside the selected outer sandbox")
	}
	policy := prepared.Policy
	if policy.Agent != req.Agent {
		if preparationErr != nil {
			report.gate("provider.preparation", Fail, preparationErr.Error(), "egg.PrepareLaunch", "correct the requested native harness configuration and artifact evidence")
			return report, nil
		}
		return report, errors.New("effective provider policy cannot be resolved")
	}
	if preparationErr != nil {
		report.gate("provider.preparation", Fail, "runtime launch preparation failed: "+preparationErr.Error(), "egg.PrepareLaunch (disposable state)", "select a valid disposable directory layout and preserve the configured policy")
	} else {
		report.gate("provider.preparation", Pass, "runtime launch preparation completed against disposable state", "egg.PrepareLaunch (disposable state)", "")
	}
	for _, deferred := range prepared.Deferred {
		report.gate("provider.preparation.deferred", Unrun, deferred+" requires native acceptance", "egg.PrepareLaunch effects boundary", "verify this step in disposable native acceptance")
	}
	report.Provenance["preparation"] = "same egg.PrepareLaunch as Server.RunSession; metadata mirror; no source writes or credential copies"
	// Render preparation's decisions without deriving policy in the consumer.
	for _, check := range prepared.Checks {
		report.gate(check.Code, check.Status, check.Reason, check.Source, check.Correction)
	}
	report.Domains = policy.Domains
	report.LocalPorts = policy.LocalPorts
	report.Network = prepared.Transport
	for _, hole := range policy.Drilled {
		report.PolicyRules = append(report.PolicyRules, egg.PolicyRuleSource{Section: hole.Kind, Rule: hole.Value, Source: "agent profile: " + hole.Agent})
	}
	for _, hole := range policy.Derived {
		report.PolicyRules = append(report.PolicyRules, egg.PolicyRuleSource{Section: hole.Kind, Rule: hole.Value, Source: hole.Reason})
	}
	for _, hole := range policy.Suppressed {
		report.PolicyRules = append(report.PolicyRules, egg.PolicyRuleSource{Section: "suppressed_" + hole.Kind, Rule: hole.Value, Source: hole.Reason})
	}
	if req.Route == "terminal_start" {
		report.ProviderArgv = []string{report.Executable.Resolved}
		report.ArgvScope = "exact command executable; no agent auto-drilling, prompt, or passthrough arguments"
	} else {
		if preparationErr != nil {
			return report, nil
		}
		argv := append([]string{report.Executable.Resolved}, prepared.Args...)
		argv[0] = report.Executable.Resolved
		report.ProviderArgv = argv
		report.ArgvScope = "runtime adapter arguments; disposable identity launch-plan; prompt excluded"
		if req.Agent == "claude" {
			report.ArgvScope += "; generated lifecycle settings/session IDs are deferred to launch"
		}

	}
	return report, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func preparationTempParent(req Request, cwd string) (string, error) {
	parent, err := canonical(os.TempDir())
	if err != nil {
		return "", errors.New("preparation temp parent cannot be resolved")
	}
	roots := []string{cwd, req.Home, req.AllowedHomeRoot}
	for _, file := range append([]string{req.PolicyPath, req.EvidencePath, req.CapabilitiesPath}, req.CredentialPaths...) {
		if file != "" {
			roots = append(roots, filepath.Dir(file))
		}
	}
	for {
		overlaps := false
		for _, root := range roots {
			resolved, err := canonical(root)
			if err != nil {
				continue
			}
			// / is a scope, not an inventory of every system directory.
			if resolved != "/" && within(parent, resolved) {
				overlaps = true
			}
		}
		if !overlaps {
			return parent, nil
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", errors.New("preparation temp parent overlaps inputs; select an external TMPDIR")
		}
		parent = next
	}
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
	argv := report.ProviderArgv
	if len(argv) > 0 {
		argv = argv[1:]
	}
	for _, arg := range argv {
		flag, _, _ := strings.Cut(arg, "=")
		if egg.IsLaunchOption(flag) && !contains(flags, flag) {
			flags = append(flags, flag)
		}
	}
	if _, err := fmt.Fprintf(w, "Provider version: %s\nPlanned options: %s (full argv in --json)\n", version, strings.Join(flags, " ")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Launch plan: %s (%s)\nPolicy: %s\nPolicy base: %s\nMerge HOME: %s\nCWD: %s\nExecutable: %s\nNetwork: %s\nArgv scope: %s\n", report.Readiness, report.Route, report.PolicySource, report.PolicyBase.Path, report.MergeHome.Path, report.CWD.Resolved, report.Executable.Resolved, report.Network, report.ArgvScope); err != nil {
		return err
	}
	for _, gate := range report.Gates {
		if gate.Status != Fail {
			continue
		}
		if _, err := fmt.Fprintf(w, "Blocked phase: %s\n  reason: %s\n  policy/evidence source: %s\n  next action: %s\n", gate.Code, gate.Reason, gate.Source, gate.Correction); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "Provider HOME: %s\nEgg reach (policy evidence; native confinement requires acceptance):\n", report.Home.Path); err != nil {
		return err
	}
	boundary := "native sandbox required by selected policy; confinement is not verified by this report"
	if !report.SandboxRequired {
		boundary = "trusted outer host boundary; full OS-user authority (explicit policy selection)"
	}
	if _, err := fmt.Fprintf(w, "Boundary: %s\n", boundary); err != nil {
		return err
	}
	for _, rule := range report.PolicyRules {
		if _, err := fmt.Fprintf(w, "  %s: %s [source: %s]\n", rule.Section, rule.Rule, rule.Source); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "Provider domains: %s\nLocal ports: %v\nAcceptance: %s\n", strings.Join(report.Domains, ", "), report.LocalPorts, report.Acceptance); err != nil {
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
