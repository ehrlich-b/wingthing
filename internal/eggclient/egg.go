package eggclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"os"
	"os/exec"

	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/egg"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/google/uuid"
)

const maxEggEnvironmentBytes = 1 << 20

func ReadEggEnvironment(path string, entries []string, required bool) (map[string]string, error) {
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
		return "", errors.Join(fmt.Errorf("write egg environment: %w", err), cmdutil.CloseAndJoin("egg environment", file, nil), cmdutil.RemoveIfExists(path))
	}
	if err := file.Close(); err != nil {
		return "", errors.Join(fmt.Errorf("close egg environment: %w", err), cmdutil.RemoveIfExists(path))
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

// explainedPolicy is the wire shape of `wt egg explain`. The sandbox is egg.yaml
// plus holes drilled automatically for the agent, and until now nothing could
// report what that added up to. These field names are an API contract.
type ExplainedPolicy struct {
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
	Drilled      []ExplainedHole  `json:"drilled"`
	Derived      []ExplainedHole  `json:"derived"`
	Suppressed   []ExplainedHole  `json:"suppressed"`
}

type explainedMount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type ExplainedHole struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
}

// loadEggConfigForExplain resolves the same config eggSpawn would use, and
// reports where it came from. An explicit --config that does not load is an
// error here, unlike discovery, which is allowed to fall back.
func LoadEggConfigForExplain(configPath, cwd string) (*egg.EggConfig, string, error) {
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

// explainEnforcement reports how the network policy is actually held. macOS
// Seatbelt allows only the proxy endpoint. Linux keeps CLONE_NEWNET and exposes
// only inherited proxy/loopback relays, so ignoring HTTPS_PROXY fails closed.
func ExplainEnforcement(need sandbox.NetworkNeed, goos, mode string) string {
	switch need {
	case sandbox.NetworkNone:
		return "none"
	case sandbox.NetworkFull:
		if goos == "linux" {
			return "proxy"
		}
		return "unrestricted"
	}
	if need == sandbox.NetworkHTTPS {
		if mode == "observe" {
			return "proxy-observe"
		}
		return "proxy"
	}
	if goos == "linux" {
		return "proxy"
	}
	return "kernel"
}

func ExplainPolicy(cfg *egg.EggConfig, agentName, home, source string) ExplainedPolicy {
	policy, _ := ExplainPolicyWithProvider(cfg, agentName, home, source, "")
	return policy
}

func ExplainPolicyWithProvider(cfg *egg.EggConfig, agentName, home, source, providerURL string) (ExplainedPolicy, error) {
	resolved, err := egg.ResolvePolicyWithProvider(cfg, agentName, home, providerURL)
	if err != nil {
		return ExplainedPolicy{}, err
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

	p := ExplainedPolicy{
		Agent:        agentName,
		ConfigSource: source,
		Isolation:    isolation,
		NetworkNeed:  resolved.NetworkNeed.String(),
		Enforcement:  ExplainEnforcement(resolved.NetworkNeed, runtime.GOOS, resolved.Mode),
		Domains:      nonNilStrings(resolved.Domains),
		LocalPorts:   resolved.LocalPorts,
		Mode:         resolved.Mode,
		Deny:         nonNilStrings(resolved.Deny),
		DenyWrite:    nonNilStrings(resolved.DenyWrite),
		Mounts:       make([]explainedMount, 0, len(resolved.Mounts)),
		Drilled:      make([]ExplainedHole, 0, len(resolved.Drilled)),
		Derived:      make([]ExplainedHole, 0, len(resolved.Derived)),
		Suppressed:   make([]ExplainedHole, 0, len(resolved.Suppressed)),
	}
	if isolation == "outer-boundary" {
		// There is no wingthing network namespace or proxy boundary in this
		// mode. In particular, do not claim Linux proxy enforcement merely
		// because the resolved (unconfined) policy asks for full networking.
		p.Enforcement = "unrestricted"
	}
	if p.LocalPorts == nil {
		p.LocalPorts = []int{}
	}
	for _, m := range resolved.Mounts {
		p.Mounts = append(p.Mounts, explainedMount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	for _, h := range resolved.Drilled {
		p.Drilled = append(p.Drilled, ExplainedHole{Kind: h.Kind, Value: h.Value, Agent: h.Agent, Reason: h.Reason})
	}
	for _, h := range resolved.Derived {
		p.Derived = append(p.Derived, ExplainedHole{Kind: h.Kind, Value: h.Value, Agent: h.Agent, Reason: h.Reason})
	}
	for _, h := range resolved.Suppressed {
		p.Suppressed = append(p.Suppressed, ExplainedHole{Kind: h.Kind, Value: h.Value, Agent: h.Agent, Reason: h.Reason})
	}
	return p, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func WritePolicyJSON(w io.Writer, p ExplainedPolicy) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

func RenderPolicy(w io.Writer, p ExplainedPolicy) error {
	agentName := p.Agent
	if agentName == "" {
		agentName = "(none — shell session)"
	}
	mode := p.Mode
	if mode == "" {
		mode = "default"
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, row := range []struct{ key, value string }{
		{"agent", agentName},
		{"config", p.ConfigSource},
		{"isolation", p.Isolation},
		{"network", string(p.NetworkNeed)},
		{"enforcement", p.Enforcement},
		{"mode", mode},
	} {
		if err := cmdutil.Writef(tw, "%s\t%s\n", row.key, row.value); err != nil {
			return err
		}
	}
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
		if err := cmdutil.Writef(w, "\ndomains (%d)\n", len(p.Domains)); err != nil {
			return err
		}
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, d := range p.Domains {
			if reason, ok := derivedDomains[d]; ok {
				if err := cmdutil.Writef(tw, "  %s\tderived\t%s\n", d, reason); err != nil {
					return err
				}
			} else if reason, ok := drilledDomains[d]; ok {
				if err := cmdutil.Writef(tw, "  %s\tauto\t%s\n", d, reason); err != nil {
					return err
				}
			} else {
				if err := cmdutil.Writef(tw, "  %s\tdeclared\t\n", d); err != nil {
					return err
				}
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(p.Suppressed) > 0 {
		if err := cmdutil.Writeln(w, "\nsuppressed agent domains"); err != nil {
			return err
		}
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, h := range p.Suppressed {
			if err := cmdutil.Writef(tw, "  %s\tsuppressed\t%s\n", h.Value, h.Reason); err != nil {
				return err
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(p.LocalPorts) > 0 {
		if err := cmdutil.Writeln(w, "\nforwarded loopback ports"); err != nil {
			return err
		}
		for _, port := range p.LocalPorts {
			if err := cmdutil.Writef(w, "  %d\n", port); err != nil {
				return err
			}
		}
	}

	if len(p.Mounts) > 0 {
		if err := cmdutil.Writef(w, "\nmounts (%d)\n", len(p.Mounts)); err != nil {
			return err
		}
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, m := range p.Mounts {
			access := "rw"
			if m.ReadOnly {
				access = "ro"
			}
			if err := cmdutil.Writef(tw, "  %s\t%s\n", access, m.Source); err != nil {
				return err
			}
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
		if err := cmdutil.Writef(w, "\n%s (%d)\n", section.title, len(section.paths)); err != nil {
			return err
		}
		for _, path := range section.paths {
			if err := cmdutil.Writef(w, "  %s\n", path); err != nil {
				return err
			}
		}
	}

	if len(p.Drilled) > 0 {
		if err := cmdutil.Writef(w, "\nauto-drilled for %s (%d)\n", p.Agent, len(p.Drilled)); err != nil {
			return err
		}
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, h := range p.Drilled {
			if err := cmdutil.Writef(tw, "  %s\t%s\t%s\n", h.Kind, h.Value, h.Reason); err != nil {
				return err
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func HumanBytes(b int64) string {
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func HumanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// loadSpawnEggConfig is shared by the human CLI and the local MCP server so an
// LLM gets the same trusted-VM behavior. Unsandboxed mode deliberately ignores
// discovered policy; combining it with an explicit config is rejected rather
// than giving a false impression that any of that config is enforced.
func LoadSpawnEggConfig(configPath, cwd string, unsandboxed bool) (*egg.EggConfig, error) {
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

func EffectiveSessionHome(cfg *config.Config, identity EggIdentity) string {
	if config.Channel() == "preview" {
		return cfg.ProviderDataHome()
	}
	home, _ := os.UserHomeDir()
	if identity.UserID != "" && (identity.OrgWing || identity.SharedHost) {
		return filepath.Join(cfg.Dir, "user-homes", UserHash(identity.UserID))
	}
	return home
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

// userHash returns the first 12 hex chars of the SHA256 of a stable identity.
// Org/shared-host callers deliberately pass the authenticated user ID, not an
// email address: two distinct Wingthing accounts must not silently share agent
// credentials merely because an identity provider reports the same email.
func UserHash(stableIdentity string) string {
	h := sha256.Sum256([]byte(stableIdentity))
	return hex.EncodeToString(h[:])[:12]
}

func PrepareIsolatedClaudeConfig(home string, envMap map[string]string) error {
	claudeDir := filepath.Join(home, ".claude")
	envMap["CLAUDE_CONFIG_DIR"] = claudeDir

	// One-time migration: users who already completed onboarding under the old
	// layout have their config at ~/.claude.json. Relocating
	// CLAUDE_CONFIG_DIR would leave that behind and re-prompt them once on
	// release. Seed the new path from the old file if it hasn't been created
	// yet. Only a regular file is migrated — a symlink at the root is the
	// shared empty stub, whose users never had persisted state to preserve.
	newCfg := filepath.Join(claudeDir, ".claude.json")
	oldCfg := filepath.Join(home, ".claude.json")
	if _, err := os.Stat(newCfg); errors.Is(err, os.ErrNotExist) {
		if fi, legacyErr := os.Lstat(oldCfg); legacyErr == nil && fi.Mode().IsRegular() {
			data, readErr := os.ReadFile(oldCfg)
			if readErr != nil {
				return fmt.Errorf("read legacy Claude config: %w", readErr)
			}
			if err := os.MkdirAll(claudeDir, 0700); err != nil {
				return fmt.Errorf("prepare Claude config directory: %w", err)
			}
			if err := daemonctl.WriteAtomicMetadataFile(newCfg, data, 0600); err != nil {
				return fmt.Errorf("migrate Claude config: %w", err)
			}
		} else if legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
			return fmt.Errorf("inspect legacy Claude config: %w", legacyErr)
		}
	} else if err != nil {
		return fmt.Errorf("inspect Claude config: %w", err)
	}
	return nil
}

func WriteEggOwner(dir, userID, email string) error {
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
// validateAgentArgs checks caller-supplied argv before any process is spawned.
// Empty and whitespace arguments are valid literal argv entries: providers use
// them to disable tools and setting sources. Only NUL cannot cross exec argv.
func ValidateAgentArgs(args []string) error {
	return validateExactAgentArgs(args)
}

func validateExactAgentArgs(args []string) error {
	for _, arg := range args {
		if strings.IndexByte(arg, 0) >= 0 {
			return errors.New("agent arguments cannot contain NUL bytes")
		}
	}
	return nil
}

type SpawnEggOpts struct {
	ResumeSessionID        string
	ResumeSourceSessionID  string
	ProviderReserved       bool
	ToolNames              []string
	ToolSocketPath         string
	Label                  string
	Kind                   string
	Command                []string
	AgentArgs              []string
	Principal              string
	PreserveEmptyAgentArgs bool
	// ProtectedWriteTargets are host-owned paths the child's final sandbox
	// policy must keep unwritable. Empty preserves ordinary launches.
	ProtectedWriteTargets []string
	// OmitBrowserBridge launches without the optional browser-open bridge.
	// False preserves ordinary launches.
	OmitBrowserBridge bool
}

const (
	ProtectedWriteTargetArg = "protected-write-target"
	OmitBrowserBridgeArg    = "omit-browser-bridge"
)

// protectedWriteTargetArgs encodes protected targets for the internal egg run
// child. The --flag=value form keeps values beginning with "-" intact, and
// StringArray (not StringSlice) keeps commas literal.
func ProtectedWriteTargetArgs(targets []string) ([]string, error) {
	if err := sandbox.ValidateProtectedWriteTargets(targets); err != nil {
		return nil, err
	}
	args := make([]string, 0, len(targets))
	for _, target := range targets {
		args = append(args, "--"+ProtectedWriteTargetArg+"="+target)
	}
	return args, nil
}

func EffectiveProviderSession(agentName, generatedResumeID string, agentArgs []string) (providerID string, effectiveArgs []string, generatedResume string, err error) {
	profile := egg.Profile(agentName)
	args := append([]string(nil), agentArgs...)
	if profile.SessionIDFlag == "" {
		return "", args, generatedResumeID, nil
	}
	providerID = generatedResumeID
	providerFlagSeen := generatedResumeID != ""
	callerProviderFlag := false
	unverifiableProviderSelection := false
	forkSession := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if agentName == "claude" {
			if strings.HasPrefix(arg, "-r") && arg != "-r" {
				// Preserve attached short-option spellings for Claude to parse,
				// but do not assume how it interprets their provider identity.
				callerProviderFlag = true
				unverifiableProviderSelection = true
				continue
			}
			switch arg {
			case "--continue", "-c":
				callerProviderFlag = true
				unverifiableProviderSelection = true
				continue
			case "--fork-session":
				forkSession = true
				continue
			}
		}
		flags := []string{profile.SessionIDFlag, profile.ResumeFlag}
		if agentName == "claude" {
			flags = append(flags, "-r")
		}
		for _, flag := range flags {
			if flag == "" {
				continue
			}
			if arg == flag {
				providerFlagSeen = true
				callerProviderFlag = true
				if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
					if flag == profile.SessionIDFlag {
						return "", nil, "", fmt.Errorf("%s requires an explicit provider session ID", flag)
					}
					// Claude's native resume picker accepts --resume/-r without
					// an ID. Preserve that interface, but do not invent an ID or
					// capture a possibly concurrent transcript for browser resume.
					unverifiableProviderSelection = true
					break
				}
				providerID = args[index+1]
				index++
				break
			}
			if value, ok := strings.CutPrefix(arg, flag+"="); ok {
				providerFlagSeen = true
				callerProviderFlag = true
				providerID = value
				break
			}
		}
	}
	if callerProviderFlag {
		generatedResumeID = ""
	}
	if unverifiableProviderSelection || forkSession {
		return "", args, generatedResumeID, nil
	}
	if !providerFlagSeen {
		providerID = uuid.NewString()
		args = append([]string{profile.SessionIDFlag, providerID}, args...)
	}
	if !ValidProviderSessionID(providerID) {
		return "", nil, "", errors.New("provider session ID is invalid")
	}
	return providerID, args, generatedResumeID, nil
}

// spawnEgg starts a per-session egg child process and returns a connected client.
func SpawnEgg(cfg *config.Config, sessionID, agentName string, eggCfg *egg.EggConfig, rows, cols uint32, cwd string, debug, vte, trace bool, identity EggIdentity, idleTimeout time.Duration, opts ...SpawnEggOpts) (*egg.Client, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)
	if err := egg.ValidateSocketPath(filepath.Join(dir, "egg.sock")); err != nil {
		return nil, err
	}
	var o SpawnEggOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.ToolSocketPath != "" {
		if err := egg.ValidateSocketPath(o.ToolSocketPath); err != nil {
			return nil, err
		}
	}
	if err := ValidateSessionName(o.Label); err != nil {
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
		lock, err := AcquireSessionNameLock(cfg)
		if err != nil {
			return nil, err
		}
		defer func() { _ = lock.Close() }()
		if err := EnsureSessionNameAvailable(cfg, o.Label, sessionID); err != nil {
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
	outerBoundary := !egg.RequiresSandbox(eggCfg, agentName)
	if err := egg.ValidatePreviewClaudeBoundary(agentName, o.Command, outerBoundary); err != nil {
		return nil, err
	}
	if err := egg.ValidateProtectedWriteTargetBoundary(o.ProtectedWriteTargets, !outerBoundary); err != nil {
		return nil, err
	}
	// Pre-flight: verify the sandbox can work before spawning a child process.
	// Catches AppArmor userns restrictions, missing sysctl, etc. with a clear
	// error instead of a silent 5s timeout.
	if !outerBoundary {
		if ok, help := sandbox.CheckCapability(); !ok {
			return nil, fmt.Errorf("sandbox not available: %s\nrun: wt doctor --fix", help)
		}
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create egg dir: %w", err)
	}
	if err := WriteSessionPrincipal(dir, o.Principal); err != nil {
		return nil, err
	}
	if err := WriteEggOwner(dir, identity.UserID, identity.Email); err != nil {
		return nil, err
	}
	if o.Label != "" {
		if err := WriteSessionName(dir, o.Label); err != nil {
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
	if outerBoundary {
		args = append(args, "--outer-boundary")
	}
	protectedArgs, err := ProtectedWriteTargetArgs(o.ProtectedWriteTargets)
	if err != nil {
		return nil, err
	}
	args = append(args, protectedArgs...)
	if o.OmitBrowserBridge {
		args = append(args, "--"+OmitBrowserBridgeArg)
	}
	for _, arg := range o.Command {
		args = append(args, "--command-arg="+arg)
	}
	validateArgs := ValidateAgentArgs
	if o.PreserveEmptyAgentArgs {
		validateArgs = validateExactAgentArgs
	}
	if err := validateArgs(o.AgentArgs); err != nil {
		return nil, err
	}
	providerSessionID := ""
	effectiveResumeSessionID := o.ResumeSessionID
	effectiveAgentArgs := append([]string(nil), o.AgentArgs...)
	if len(o.Command) == 0 {
		providerSessionID, effectiveAgentArgs, effectiveResumeSessionID, err = EffectiveProviderSession(agentName, o.ResumeSessionID, o.AgentArgs)
		if err != nil {
			return nil, err
		}
	}
	isolatedUser := config.Channel() == "preview" || identity.UserID != "" && (identity.OrgWing || identity.SharedHost)
	policyArgs, err := IsolatedClaudePolicyArgs(agentName, isolatedUser && len(o.Command) == 0)
	if err != nil {
		return nil, err
	}
	for _, arg := range append(policyArgs, effectiveAgentArgs...) {
		args = append(args, "--agent-arg="+arg)
	}
	if eggCfg.Shell != "" {
		args = append(args, "--shell", eggCfg.Shell)
	}
	if eggCfg.DangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	// Compute the effective home before expanding FS policy. Besides ~ rules,
	// this lets the policy mask a live SSH agent socket when ~/.ssh is denied.
	realHome, _ := os.UserHomeDir()
	effectiveHome := EffectiveSessionHome(cfg, identity)
	// Keep the exact execution/provider reference inspectable even when startup
	// fails before the provider process or its endpoint becomes available.
	meta := fmt.Sprintf("agent=%s\nkind=%s\ncwd=%s\nprovider_session_id=%s\nprovider_home=%s\n", agentName, o.Kind, cwd, providerSessionID, effectiveHome)
	if err := daemonctl.WriteAtomicMetadataFile(filepath.Join(dir, "egg.meta"), []byte(meta), 0600); err != nil {
		return nil, fmt.Errorf("persist startup identity: %w", err)
	}
	var releaseProviderSession func(bool)
	providerProcessStarted := false
	if providerSessionID != "" {
		if o.ProviderReserved {
			if err := verifyProviderResumeReservation(cfg, effectiveHome, agentName, providerSessionID, o.ResumeSourceSessionID, sessionID); err != nil {
				return nil, err
			}
		} else {
			releaseProviderSession, err = BrowserProviderResumes.Reserve(cfg, effectiveHome, agentName, providerSessionID, o.ResumeSourceSessionID, sessionID)
			if err != nil {
				return nil, err
			}
			defer func() { releaseProviderSession(providerProcessStarted) }()
		}
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
	for _, path := range eggCfg.SSHAgentSocketDenyPaths(effectiveHome, identity.SharedHost) {
		args = append(args, "--fs", "deny:"+path)
	}
	for _, d := range eggCfg.Network.Domains {
		args = append(args, "--network", d)
	}
	for _, port := range eggCfg.Network.LocalPorts {
		args = append(args, "--local-port", strconv.Itoa(port))
	}
	if eggCfg.Network.Mode != "" {
		args = append(args, "--network-mode", eggCfg.Network.Mode)
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
	envMap := eggCfg.BuildEnvMap(effectiveHome)
	if identity.SharedHost || config.Channel() == "preview" {
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
		if identity.SharedHost || config.Channel() == "preview" {
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
		if identity.SharedHost || config.Channel() == "preview" {
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
			if err := PrepareSharedAgentHome(perUserHome, profileDirs); err != nil {
				return nil, fmt.Errorf("prepare shared-host agent home: %w", err)
			}
		} else if err := os.MkdirAll(perUserHome, 0700); err != nil {
			return nil, fmt.Errorf("prepare agent home: %w", err)
		}
		// Seed shell + agent config symlinks from real HOME
		if realHome != "" && !identity.SharedHost && config.Channel() != "preview" {
			for _, rc := range []string{".bashrc", ".zshrc", ".profile"} {
				src := filepath.Join(realHome, rc)
				dst := filepath.Join(perUserHome, rc)
				if _, err := os.Stat(src); err == nil {
					if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
						if err := os.Symlink(src, dst); err != nil {
							return nil, fmt.Errorf("seed shell config %s: %w", rc, err)
						}
					} else if err != nil {
						return nil, fmt.Errorf("inspect shell config %s: %w", rc, err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("inspect source shell config %s: %w", rc, err)
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
		runtimeCommand := agentRuntimeCommand(agentName)
		if agentBin, err := exec.LookPath(runtimeCommand); err == nil {
			dst := filepath.Join(localBin, runtimeCommand)
			if identity.SealedFS {
				if err := InstallSharedAgentBinary(agentBin, perUserHome, runtimeCommand); err != nil {
					return nil, fmt.Errorf("prepare shared-host %s runtime: %w", agentName, err)
				}
			} else if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
				if err := os.Symlink(agentBin, dst); err != nil {
					return nil, fmt.Errorf("link agent runtime: %w", err)
				}
			} else if err != nil {
				return nil, fmt.Errorf("inspect agent runtime link: %w", err)
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
		if err := PrepareIsolatedClaudeConfig(effectiveHome, envMap); err != nil {
			return nil, err
		}
	}
	// Rebuild agent settings every session for org wing users.
	// Reads existing prefs, layers host settings on top (host always wins
	// for permissions), then injects agent-specific overrides.
	if isolatedUser && !identity.SharedHost && config.Channel() != "preview" && agentName != "claude" {
		agentProfile := egg.Profile(agentName)
		if agentProfile.SettingsFile != "" {
			settingsDst := filepath.Join(effectiveHome, agentProfile.SettingsFile)
			if err := os.MkdirAll(filepath.Dir(settingsDst), 0700); err != nil {
				return nil, fmt.Errorf("prepare agent settings directory: %w", err)
			}
			baseSettings := make(map[string]any)
			// Read existing session settings to preserve user preferences
			if data, err := os.ReadFile(settingsDst); err == nil {
				if err := json.Unmarshal(data, &baseSettings); err != nil {
					return nil, fmt.Errorf("parse agent settings: %w", err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("read agent settings: %w", err)
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
			} else if realHome != "" && !identity.SharedHost && config.Channel() != "preview" {
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
				data, err := json.MarshalIndent(baseSettings, "", "  ")
				if err != nil {
					return nil, fmt.Errorf("encode agent settings: %w", err)
				}
				if err := daemonctl.WriteAtomicMetadataFile(settingsDst, append(data, '\n'), 0644); err != nil {
					return nil, fmt.Errorf("write agent settings: %w", err)
				}
			}
		}
	}
	// Write ANTHROPIC_API_KEY to a stable file and use apiKeyHelper to read
	// it. The key never enters the agent's environment. The file lives at
	// effectiveHome/.anthropic_key (not per-session) so the settings.json
	// path doesn't go stale when sessions end or race with each other.
	if err := SetupAPIKeyHelper(agentName, envMap, effectiveHome); err != nil {
		return nil, err
	}
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
	if effectiveResumeSessionID != "" {
		args = append(args, "--resume-session", effectiveResumeSessionID)
	}
	if providerSessionID != "" {
		args = append(args, "--provider-session-id", providerSessionID)
	}
	if o.ToolSocketPath != "" && len(o.ToolNames) > 0 {
		args = append(args, "--tool-socket", o.ToolSocketPath)
		for _, tn := range o.ToolNames {
			args = append(args, "--tool-name", tn)
		}
	}
	if identity.SharedHost || config.Channel() == "preview" {
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
		cmdutil.CloseWithLog("egg log", logFile)
		return nil, err
	}
	defer cmdutil.RemoveWithLog(envPath)

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
			"USER": true, "SHELL": true, "TMPDIR": true, "WINGTHING_DIR": true, "WINGTHING_PREVIEW_DIR": true,
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
		cmdutil.CloseWithLog("egg log", logFile)
		return nil, fmt.Errorf("start egg: %w", err)
	}
	providerProcessStarted = true
	if err := logFile.Close(); err != nil {
		daemonctl.AbandonStartedDaemon(child)
		providerProcessStarted = false
		return nil, fmt.Errorf("close egg log: %w", err)
	}

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

	daemonctl.AbandonStartedDaemon(child)
	providerProcessStarted = false
	failure := errors.New("egg did not start within 5s")
	diagnostic, diagnosticErr := PreserveEggFailure(dir, failure)
	if diagnosticErr != nil {
		return nil, fmt.Errorf("%w (diagnostic preservation failed: %v; startup log %s)", failure, diagnosticErr, logPath)
	}
	return nil, fmt.Errorf("%w (check %s)", failure, diagnostic)
}

func sealedSharedHostEggConfig(cfg *config.Config, source *egg.EggConfig, cwd string, allowedPaths []string) (*egg.EggConfig, error) {
	if source == nil {
		return nil, errors.New("shared-host egg config is required")
	}
	canonical, err := ValidateSharedHostWorkspacePaths(cfg, allowedPaths)
	if err != nil {
		return nil, err
	}
	resolvedCWD := wingpolicy.CanonicalSessionPath(cwd)
	if !wingpolicy.IsUnderPaths(resolvedCWD, canonical) {
		return nil, fmt.Errorf("working directory %q is outside this user's roost paths", cwd)
	}
	declared := source.ToSandboxConfig("")
	if !ContainsExactPath(declared.Deny, string(filepath.Separator)) {
		return nil, errors.New("shared-host egg config must deny the filesystem root")
	}
	if err := RejectSharedHostRootMount(declared.Mounts); err != nil {
		return nil, err
	}
	// egg.yaml is the administrator-authored security policy. AllowedPaths
	// authorizes the session CWD; it does not replace or synthesize mounts.
	sealed := *source
	sealed.FS = append([]string(nil), source.FS...)
	if source.AgentSettings != nil {
		sealed.AgentSettings = make(map[string]string, len(source.AgentSettings))
		for name, path := range source.AgentSettings {
			sealed.AgentSettings[name] = path
		}
	}
	sealed.Env = append(egg.EnvField(nil), source.Env...)
	sealed.Network.Domains = append([]string(nil), source.Network.Domains...)
	sealed.Network.LocalPorts = append([]int(nil), source.Network.LocalPorts...)
	return &sealed, nil
}

func ContainsExactPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func RejectSharedHostRootMount(mounts []sandbox.Mount) error {
	for _, mount := range mounts {
		if wingpolicy.CanonicalSessionPath(mount.Source) == string(filepath.Separator) {
			return errors.New("shared-host egg config must not mount the filesystem root")
		}
	}
	return nil
}

func ValidateSharedHostWorkspacePaths(cfg *config.Config, allowedPaths []string) ([]string, error) {
	canonical := wingpolicy.CanonicalPaths(allowedPaths)
	if len(canonical) == 0 {
		return nil, errors.New("shared-host sessions require at least one configured workspace path")
	}
	stateDir := wingpolicy.CanonicalSessionPath(cfg.Dir)
	hostHome, _ := os.UserHomeDir()
	hostHome = wingpolicy.CanonicalSessionPath(hostHome)
	for _, path := range canonical {
		if path == string(filepath.Separator) {
			return nil, errors.New("the filesystem root cannot be a shared-roost workspace path")
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			if err == nil {
				err = errors.New("not a directory")
			}
			return nil, fmt.Errorf("shared-roost workspace %q: %w", path, err)
		}
		if wingpolicy.IsUnderPaths(stateDir, []string{path}) || wingpolicy.IsUnderPaths(path, []string{stateDir}) {
			return nil, fmt.Errorf("shared-roost workspace %q overlaps Wingthing state", path)
		}
		if hostHome != "." && wingpolicy.IsUnderPaths(hostHome, []string{path}) {
			return nil, fmt.Errorf("shared-roost workspace %q contains the host account home", path)
		}
	}
	return canonical, nil
}

// parseMemFlag parses a memory string like "2GB" or "512MB" into bytes.
func ParseMemFlag(s string) uint64 {
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

// agentRuntimeCommand is the single translation from Wingthing's public agent
// name to the executable placed in an isolated user's PATH. Most names match;
// Cursor is intentionally exposed as "cursor" while its CLI binary is "agent".
func agentRuntimeCommand(agentName string) string {
	if definition, ok := agent.LookupDefinition(agentName); ok {
		return definition.Command
	}
	return agentName
}

// setupAPIKeyHelper moves ANTHROPIC_API_KEY out of the environment and into a
// stable file + apiKeyHelper setting. This prevents the key from entering the
// agent's env and avoids the v0.128.0 race where per-session paths in
// settings.json went stale.
func SetupAPIKeyHelper(agentName string, envMap map[string]string, effectiveHome string) error {
	if agentName != "claude" {
		return nil
	}
	v, ok := envMap["ANTHROPIC_API_KEY"]
	if ok {
		delete(envMap, "ANTHROPIC_API_KEY")
	} else if config.Channel() != "preview" {
		// Shared-host mode strips provider creds from the agent env and skips
		// injecting them into envMap, so the key never reaches this point via
		// envMap. Source it straight from the roost's own environment: it lands
		// only in the 0400 helper file below and still never enters the agent's
		// environment. Without this, authenticated (shared-host) sessions get no
		// credential and every user is forced to log in manually.
		v = os.Getenv("ANTHROPIC_API_KEY")
	}
	if v == "" {
		return nil
	}
	keyFile := filepath.Join(effectiveHome, ".anthropic_key")
	if err := os.MkdirAll(effectiveHome, 0700); err != nil {
		return fmt.Errorf("prepare API key helper directory: %w", err)
	}
	if err := daemonctl.WriteAtomicMetadataFile(keyFile, []byte(v), 0400); err != nil {
		return fmt.Errorf("write API key helper: %w", err)
	}
	agentProfile := egg.Profile(agentName)
	if agentProfile.SettingsFile == "" {
		return nil
	}
	settingsDst := filepath.Join(effectiveHome, agentProfile.SettingsFile)
	if err := os.MkdirAll(filepath.Dir(settingsDst), 0700); err != nil {
		return fmt.Errorf("prepare API key settings directory: %w", err)
	}
	settings := make(map[string]any)
	if data, err := os.ReadFile(settingsDst); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse API key settings: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read API key settings: %w", err)
	}
	helper := "cat " + keyFile
	if settings["apiKeyHelper"] == helper {
		return nil
	}
	settings["apiKeyHelper"] = helper
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode API key settings: %w", err)
	}
	if err := daemonctl.WriteAtomicMetadataFile(settingsDst, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("write API key settings: %w", err)
	}
	return nil
}
