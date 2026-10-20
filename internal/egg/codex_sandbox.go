package egg

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// CodexSandboxRender supplements the existing outer sandbox. It is not
// evidence that native enforcement has been probed. Args use CLI precedence.
type CodexSandboxRender struct {
	SandboxMode    string            `json:"sandbox_mode"`
	ApprovalPolicy string            `json:"approval_policy"`
	WritableRoots  []string          `json:"writable_roots"`
	NetworkAccess  bool              `json:"network_access"`
	Args           []string          `json:"args"`
	Coverage       map[string]string `json:"coverage"`
}

// RenderCodexSandbox translates declared egg tool intent. Provider/control
// network holes and writable provider state belong to the outer controller;
// they do not implicitly grant the model tool access.
func RenderCodexSandbox(cfg *EggConfig, cwd, home string, unattended bool) (CodexSandboxRender, error) {
	out := CodexSandboxRender{SandboxMode: "read-only", ApprovalPolicy: "on-request", WritableRoots: []string{}, Coverage: map[string]string{
		"tool_writes":                       "Codex native tool sandbox intersected with Wingthing outer sandbox",
		"read_denials_and_write_exceptions": "Wingthing outer sandbox",
		"network_destinations":              "Wingthing outer sandbox",
		"startup_mcp_hooks_and_resources":   "Wingthing outer sandbox",
		"native_availability":               "unverified until native launch; no bypass fallback",
	}}
	if cfg == nil || !filepath.IsAbs(cwd) || !filepath.IsAbs(home) {
		return out, errors.New("Codex sandbox rendering requires egg intent and absolute cwd and home")
	}
	if unattended {
		out.ApprovalPolicy = "never"
	}
	canonicalCWD, err := sandbox.CanonicalPath(cwd)
	if err != nil {
		return out, fmt.Errorf("canonical Codex cwd: %w", err)
	}
	seen := map[string]bool{}
	for _, rule := range cfg.FS {
		mode, path, ok := strings.Cut(rule, ":")
		if !ok {
			mode, path = "rw", rule
		}
		switch mode {
		case "ro", "deny", "deny-write", "deny-rename":
			continue
		case "rw":
		default:
			return out, fmt.Errorf("Codex sandbox cannot translate filesystem mode %q", mode)
		}
		if path == "" {
			return out, errors.New("Codex writable root must not be empty")
		}
		path = expandTilde(path, home)
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		root, err := sandbox.CanonicalPath(path)
		if err != nil {
			return out, fmt.Errorf("canonical Codex writable root: %w", err)
		}
		if !seen[root] {
			out.WritableRoots = append(out.WritableRoots, root)
			seen[root] = true
		}
	}
	sort.Strings(out.WritableRoots)
	if len(out.WritableRoots) > 0 {
		out.SandboxMode = "workspace-write"
		// Codex implicitly grants cwd. Reject accidental grants when all declared
		// roots are elsewhere; the model cannot select another policy anchor.
		covered := false
		for _, root := range out.WritableRoots {
			rel, err := filepath.Rel(root, canonicalCWD)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				covered = true
			}
		}
		if !covered {
			return out, errors.New("Codex workspace-write implicitly grants cwd; egg must declare a writable root covering cwd")
		}
	}
	out.NetworkAccess = len(cfg.Network.Domains) > 0 || len(cfg.Network.LocalPorts) > 0
	if out.SandboxMode == "read-only" && out.NetworkAccess {
		return out, errors.New("Codex read-only mode cannot represent enabled tool network access; declare a writable workspace or disable tool network")
	}
	quoted := make([]string, len(out.WritableRoots))
	for i, root := range out.WritableRoots {
		wire, _ := json.Marshal(root)
		quoted[i] = string(wire)
	}
	for _, value := range []string{
		"sandbox_mode=" + strconv.Quote(out.SandboxMode),
		"approval_policy=" + strconv.Quote(out.ApprovalPolicy),
		"sandbox_workspace_write.writable_roots=[" + strings.Join(quoted, ",") + "]",
		"sandbox_workspace_write.network_access=" + strconv.FormatBool(out.NetworkAccess),
		"sandbox_workspace_write.exclude_slash_tmp=true",
		"sandbox_workspace_write.exclude_tmpdir_env_var=true",
	} {
		out.Args = append(out.Args, "-c", value)
	}
	return out, nil
}

// Bound passthrough until additional options have explicit driver support.
func validateCodexSandboxOverrides(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return nil
		}
		if arg == "-m" || arg == "--model" {
			i++
			if i < len(args) && args[i] != "" {
				continue
			}
		} else if strings.HasPrefix(arg, "--model=") && len(arg) > len("--model=") {
			continue
		}
		return errors.New("Codex native sandbox accepts only --model/-m overrides and a prompt after --; policy, profile, cwd and feature overrides are unsupported")
	}
	return nil
}
