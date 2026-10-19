package egg

import (
	"fmt"
	"runtime"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// Driver owns preparation and the execution boundary. Sandbox retains the
// existing Exec/PostStart/Destroy lifecycle; the server owns PTY streaming,
// cancellation and session retention. Native harness drivers can implement
// this contract without changing v1 policy or selecting a weaker fallback.
type Driver interface {
	ID() string
	Capabilities(platform string, outerBoundary bool) DriverReport
	Probe() (DriverReport, error)
	Prepare(RunConfig, LaunchPreparationOptions, LaunchState) (PreparedLaunch, error)
	Open(sandbox.Config) (sandbox.Sandbox, error)
}

const (
	CapabilitySupported   = "supported"
	CapabilityUnsupported = "unsupported"
	CapabilityUnverified  = "unverified"
)

type DriverCapability struct {
	Control  string `json:"control"`
	Scope    string `json:"scope"`
	Status   string `json:"status"`
	Owner    string `json:"owner"`
	Evidence string `json:"evidence"`
}

type CapabilityRequirement struct{ Control, Scope string }

type DriverCoverage struct {
	Process   string `json:"process"`
	Owner     string `json:"owner"`
	Exception string `json:"exception,omitempty"`
}

// DriverReport contains no environment values, credential paths or native
// provider output. Static policy coverage never attests deployed enforcement.
type DriverReport struct {
	ID           string             `json:"id"`
	Platform     string             `json:"platform"`
	Backend      string             `json:"backend"`
	Execution    string             `json:"execution"`
	Protocol     string             `json:"protocol"`
	Artifact     string             `json:"artifact"`
	Capabilities []DriverCapability `json:"capabilities"`
	Coverage     []DriverCoverage   `json:"coverage"`
}

// Require rejects missing controls, scope mismatches and unverified claims.
// v1 has no new requirements; its existing admission checks remain unchanged.
func (r DriverReport) Require(required []CapabilityRequirement) error {
	for _, req := range required {
		found := false
		for _, capability := range r.Capabilities {
			if capability.Control == req.Control && capability.Scope == req.Scope && capability.Status == CapabilitySupported {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("driver %s cannot verify required %s in scope %s", r.ID, req.Control, req.Scope)
		}
	}
	return nil
}

type LegacyDriver struct{}

var _ Driver = LegacyDriver{}

// SelectDriver is explicit and closed: an unknown driver never becomes legacy.
func SelectDriver(id string) (Driver, error) {
	if id == "" || id == "legacy" {
		return LegacyDriver{}, nil
	}
	return nil, fmt.Errorf("unknown egg driver %q", id)
}

func (LegacyDriver) ID() string { return "legacy" }

func (d LegacyDriver) Capabilities(platform string, outerBoundary bool) DriverReport {
	if platform == "" {
		platform = runtime.GOOS
	}
	r := DriverReport{ID: d.ID(), Platform: platform, Backend: "unavailable", Execution: "local", Protocol: "egg/v1", Artifact: "built-in Wingthing sandbox; harness version and certification unverified"}
	if platform == "linux" {
		r.Backend = "linux-namespaces-seccomp"
	}
	if platform == "darwin" {
		r.Backend = "macos-seatbelt"
	}
	status := CapabilityUnverified
	if outerBoundary || r.Backend == "unavailable" {
		status = CapabilityUnsupported
	}
	owner := r.Backend
	if outerBoundary {
		r.Backend, owner = "explicit-outer-boundary", "external boundary (not verified by Wingthing)"
	}
	for _, control := range []string{"filesystem", "network", "resources"} {
		r.Capabilities = append(r.Capabilities, DriverCapability{control, "sandbox-process-tree", status, owner, "existing platform policy builder; per-config creation and native acceptance still required"})
	}
	for _, control := range []string{"scoped-environment", "controller-only-auth", "artifact-lock", "native-policy", "versioned-resume"} {
		r.Capabilities = append(r.Capabilities, DriverCapability{control, "whole-harness", CapabilityUnsupported, "none", "v1 legacy adapter provides no such contract"})
	}
	r.Capabilities = append(r.Capabilities, DriverCapability{"sandbox-startup", "sandbox-process-tree", status, owner, "no deployment probe performed"})
	for _, process := range []string{"harness-startup", "shell", "file-tools", "mcp", "lsp", "hooks", "setup", "git-helpers", "subagents"} {
		r.Coverage = append(r.Coverage, DriverCoverage{Process: process, Owner: owner, Exception: "only descendants launched inside the sandbox; host services, privileged tool callbacks and remote execution are outside this boundary"})
	}
	r.Coverage = append(r.Coverage, DriverCoverage{Process: "wingthing-controller", Owner: "host", Exception: "policy preparation, authentication state, proxy and privileged callbacks run outside the sandbox; v1 environment and credentials may be shared with tools"})
	return r
}

// Probe preserves the legacy prerequisite probe. It does not certify arbitrary
// policy, cgroup quotas, provider versions, authentication or remote tools.
func (d LegacyDriver) Probe() (DriverReport, error) {
	r := d.Capabilities(runtime.GOOS, false)
	ok, help := sandbox.CheckCapability()
	c := &r.Capabilities[len(r.Capabilities)-1]
	c.Status, c.Evidence = CapabilitySupported, "legacy sandbox creation and cleanup probe succeeded"
	if !ok {
		c.Status, c.Evidence = CapabilityUnsupported, "legacy sandbox creation or cleanup probe failed"
		return r, fmt.Errorf("%s", help)
	}
	return r, nil
}

func (d LegacyDriver) Prepare(rc RunConfig, opts LaunchPreparationOptions, state LaunchState) (PreparedLaunch, error) {
	r := d.Capabilities(opts.Platform, rc.OuterBoundary)
	if err := r.Require(opts.RequiredCapabilities); err != nil {
		return PreparedLaunch{Driver: r}, err
	}
	p, err := prepareLegacyLaunch(rc, opts, state)
	p.Driver = r
	return p, err
}

func (LegacyDriver) Open(cfg sandbox.Config) (sandbox.Sandbox, error) { return sandbox.New(cfg) }
