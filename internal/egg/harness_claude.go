package egg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"gopkg.in/yaml.v3"
)

// HarnessConfig opts a session into a versioned native policy compiler.
// Nil preserves the v1 launch contract. This driver supplements the outer
// sandbox; it never authorizes removing that boundary.
type HarnessConfig struct {
	Driver         string `yaml:"driver" json:"driver"`
	Version        string `yaml:"version" json:"version"`
	PermissionMode string `yaml:"permission_mode,omitempty" json:"permission_mode,omitempty"`
}

type harnessConfigError struct{ err error }

func (e *harnessConfigError) Error() string { return e.err.Error() }

func (h *HarnessConfig) UnmarshalYAML(n *yaml.Node) (err error) {
	defer func() {
		if err != nil {
			err = &harnessConfigError{err}
		}
	}()
	if n.Kind != yaml.MappingNode {
		return errors.New("harness must be a mapping")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case "driver", "version", "permission_mode":
		default:
			return errors.New("unknown harness field")
		}
	}
	type plain HarnessConfig
	if err := n.Decode((*plain)(h)); err != nil {
		return err
	}
	return h.validate()
}

var claudeHarnessVersion = regexp.MustCompile(`^2\.1\.([0-9]+)$`)

func (h HarnessConfig) validate() error {
	if h.Driver != "claude-code" {
		return errors.New("harness.driver must be claude-code")
	}
	match := claudeHarnessVersion.FindStringSubmatch(h.Version)
	if match == nil {
		return errors.New("harness.version must pin Claude Code 2.1.285 or later in major 2")
	}
	patch, err := strconv.Atoi(match[1])
	if err != nil || patch < 285 {
		return errors.New("harness.version requires Claude Code 2.1.285 or later in major 2")
	}
	switch h.PermissionMode {
	case "", "default", "acceptEdits", "plan", "dontAsk":
	default:
		return errors.New("unsupported harness.permission_mode; use default, acceptEdits, plan or dontAsk")
	}
	return nil
}

// ClaudeHarnessRender is safe to inspect before startup: no environment values,
// credentials, user settings, or repository configuration are read by rendering.
// Unverified controls remain explicitly assigned to the existing outer backend.
type ClaudeHarnessRender struct {
	Driver         string            `json:"driver"`
	Version        string            `json:"version"`
	PermissionMode string            `json:"permission_mode"`
	Settings       json.RawMessage   `json:"settings"`
	SHA256         string            `json:"sha256"`
	Coverage       map[string]string `json:"coverage"`
	Exceptions     []string          `json:"exceptions"`
}

func validateClaudeHarness(rc RunConfig, opts LaunchPreparationOptions, state LaunchState) error {
	if err := rc.Harness.validate(); err != nil {
		return err
	}
	if rc.Agent != "claude" || len(rc.Command) != 0 {
		return errors.New("claude-code harness requires a Claude agent session")
	}
	if rc.OuterBoundary {
		return errors.New("claude-code harness requires the Wingthing outer sandbox")
	}
	if rc.DangerouslySkipPermissions {
		return errors.New("claude-code harness conflicts with dangerously_skip_permissions; select harness.permission_mode")
	}
	if rc.ProviderSessionID == "" {
		return errors.New("claude-code harness requires an exact provider session identity")
	}
	platform := opts.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	if platform != "linux" && platform != "darwin" {
		return errors.New("claude-code harness supports Linux and macOS only")
	}
	// Keep all policy-affecting CLI controls under the driver, including aliases
	// and future flags. Preference and identity options are explicitly bounded.
	valueFlags := map[string]bool{"--model": true, "--effort": true, "--session-id": true, "--resume": true, "--settings": true}
	for i := 0; i < len(rc.AgentArgs); i++ {
		flag, value, inline := strings.Cut(rc.AgentArgs[i], "=")
		if !valueFlags[flag] {
			return errors.New("claude-code harness rejects policy-affecting passthrough arguments")
		}
		if !inline {
			i++
			if i >= len(rc.AgentArgs) || strings.HasPrefix(rc.AgentArgs[i], "-") {
				return errors.New("claude-code harness argument requires a value")
			}
			value = rc.AgentArgs[i]
		}
		if flag == "--settings" && !claudeHarnessPreferences(value) {
			return errors.New("claude-code harness accepts only inline model/effort preferences in --settings")
		}
	}
	version := opts.ClaudeVersion
	if version == "" {
		if _, native := state.(nativeLaunchState); !native {
			return errors.New("claude-code harness requires saved exact version evidence for dry preparation")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, opts.Binary, "--version")
		// Version discovery is headless and does not inherit provider credentials.
		cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "DISABLE_AUTOUPDATER=1"}
		output, err := cmd.Output()
		if err != nil || len(output) > 1024 {
			return errors.New("cannot identify installed Claude Code version")
		}
		version = string(output)
	}
	version = strings.TrimSpace(version)
	if version != rc.Harness.Version+" (Claude Code)" {
		return errors.New("installed Claude Code does not match harness.version")
	}
	return nil
}

// RenderClaudeHarness compiles the resolved egg intent, including profile holes.
// Relative policy paths anchor at the configured CWD, never the model's cwd.
func RenderClaudeHarness(h HarnessConfig, policy EffectivePolicy, cwd string) (ClaudeHarnessRender, error) {
	var out ClaudeHarnessRender
	if err := h.validate(); err != nil {
		return out, err
	}
	if policy.Agent != "claude" || !filepath.IsAbs(cwd) {
		return out, errors.New("Claude policy and absolute configured cwd required")
	}
	path := func(value string) (string, error) {
		if !filepath.IsAbs(value) {
			value = filepath.Join(cwd, value)
		}
		// Native path grammar treats these as patterns, not literal paths. Refuse
		// ambiguous roots rather than accidentally widening the native grant.
		if strings.ContainsAny(value, "*?[]{}()!\x00\r\n") || strings.HasPrefix(value, "~") {
			return "", errors.New("Claude native path cannot represent a policy root literally")
		}
		return filepath.Clean(value), nil
	}
	sorted := func(values []string) []string {
		result := append([]string{}, values...)
		sort.Strings(result)
		return compactHarnessStrings(result)
	}
	var allowWrite, denyRead, denyWrite, deny []string
	for _, mount := range policy.Mounts {
		target := mount.Target
		if target == "" {
			target = mount.Source
		}
		root, err := path(target)
		if err != nil {
			return out, err
		}
		if mount.ReadOnly {
			continue
		}
		allowWrite = append(allowWrite, root)
		// Regex provider holes are conservatively narrower natively. The outer
		// backend retains its own existing semantics for adjacent state files.
	}
	for _, item := range policy.Deny {
		root, err := path(item)
		if err != nil {
			return out, err
		}
		denyRead = append(denyRead, root)
		denyWrite = append(denyWrite, root)
		for _, suffix := range []string{"", "/**"} {
			deny = append(deny, "Read(/"+root+suffix+")", "Edit(/"+root+suffix+")")
		}
	}
	for _, item := range policy.DenyWrite {
		root, err := path(item)
		if err != nil {
			return out, err
		}
		denyWrite = append(denyWrite, root)
		deny = append(deny, "Edit(/"+root+")", "Edit(/"+root+"/**)")
	}
	mode := h.PermissionMode
	if mode == "" {
		mode = "default"
	}
	settings := map[string]any{
		"permissions": map[string]any{"defaultMode": mode, "deny": sorted(deny)},
		"sandbox": map[string]any{
			"enabled": true, "failIfUnavailable": true, "autoAllowBashIfSandboxed": false,
			"allowUnsandboxedCommands": false, "excludedCommands": []string{},
			"enableWeakerNestedSandbox": false, "enableWeakerNetworkIsolation": false,
			"filesystem": map[string]any{"allowWrite": sorted(allowWrite), "denyRead": sorted(denyRead), "denyWrite": sorted(denyWrite)},
			"network":    map[string]any{"allowedDomains": sorted(policy.Domains), "allowAllUnixSockets": false, "allowUnixSockets": []string{}, "allowLocalBinding": false},
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(data)
	out = ClaudeHarnessRender{Driver: h.Driver, Version: h.Version, PermissionMode: mode, Settings: data, SHA256: hex.EncodeToString(sum[:]),
		Coverage:   map[string]string{"shell": "Claude native sandbox plus Wingthing outer sandbox; runtime unverified", "file_tools": "Claude permissions plus Wingthing outer sandbox", "startup": "Wingthing outer sandbox", "mcp": "Wingthing outer sandbox", "hooks": "Wingthing outer sandbox", "lsp": "Wingthing outer sandbox", "setup": "Wingthing outer sandbox", "git_helpers": "Wingthing outer sandbox", "subagents": "Wingthing outer sandbox", "resources": "Wingthing outer sandbox", "network": "Wingthing outer sandbox across controller and tools; native Bash allowlist supplements it"},
		Exceptions: []string{"native nesting and installed enforcement require runtime conformance; failIfUnavailable forbids fallback", "managed settings can override native CLI settings; the outer sandbox remains authoritative", "native cwd/temp defaults and provider regex holes are not equivalent to the outer filesystem policy", "native network does not cover all controller, MCP or hook traffic; outer network enforcement remains required", "deny-rename, symlink traversal and executable roots remain owned by the outer sandbox"},
	}
	return out, nil
}

func compactHarnessStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

// String intentionally excludes the rendered policy and all environment values.
func (r ClaudeHarnessRender) String() string {
	return fmt.Sprintf("%s %s settings sha256:%s", r.Driver, r.Version, r.SHA256)
}

// Only preference JSON produced by the isolated model-policy adapter may be
// merged before native policy. File paths and all executable settings fail.
func claudeHarnessPreferences(value string) bool {
	var settings map[string]json.RawMessage
	if json.Unmarshal([]byte(value), &settings) != nil || settings == nil {
		return false
	}
	for key, raw := range settings {
		switch key {
		case "model", "effortLevel":
			var v string
			if json.Unmarshal(raw, &v) != nil {
				return false
			}
		case "env":
			var env map[string]string
			if json.Unmarshal(raw, &env) != nil {
				return false
			}
			for k, v := range env {
				switch k {
				case "DISABLE_AUTOUPDATER":
					if v != "1" {
						return false
					}
				case "CLAUDE_CODE_EFFORT_LEVEL":
					switch v {
					case "low", "medium", "high", "xhigh", "max", "auto":
					default:
						return false
					}
				default:
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func persistClaudeHarnessReport(dir string, report *ClaudeHarnessRender) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return daemonctl.WriteAtomicMetadataFile(filepath.Join(dir, "harness-policy.json"), data, 0600)
}
