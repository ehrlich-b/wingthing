package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"gopkg.in/yaml.v3"
)

const (
	HostedRelayAllow = "allow"
	HostedRelayDeny  = "deny"
)

// WingConfig holds wing-specific settings persisted in ~/.wingthing/wing.yaml.
type WingConfig struct {
	WingID         string     `yaml:"wing_id"`
	Label          string     `yaml:"label,omitempty"` // display name shown in the web UI
	Roost          string     `yaml:"roost,omitempty"`
	Org            string     `yaml:"org,omitempty"`
	Paths          PathList   `yaml:"paths,omitempty"`
	Root           string     `yaml:"root,omitempty"` // compat: folded into Paths on load
	Labels         []string   `yaml:"labels,omitempty"`
	EggConfig      string     `yaml:"egg_config,omitempty"`
	Conv           string     `yaml:"conv,omitempty"`
	Audit          bool       `yaml:"audit,omitempty"`
	Debug          bool       `yaml:"debug,omitempty"`
	Locked         bool       `yaml:"locked,omitempty"`   // explicit lock mode toggle
	Spectate       bool       `yaml:"spectate,omitempty"` // allow spectator (read-only) session viewing
	AuthTTL        string     `yaml:"auth_ttl,omitempty"` // passkey auth token duration (default "1h")
	AllowKeys      []AllowKey `yaml:"allow_keys,omitempty"`
	Admins         []string   `yaml:"admins,omitempty"`          // emails with admin role (see all sessions, all paths)
	IdleTimeout    string     `yaml:"idle_timeout,omitempty"`    // kill sessions idle for this long (e.g. "4h")
	ConnectionMode string     `yaml:"connection_mode,omitempty"` // "relay" (default), "p2p", "p2p_only", "direct"
	HostedRelay    string     `yaml:"hosted_relay,omitempty"`    // "allow" (default) or "deny"

	// P2P / Direct mode settings
	ICEServers []ICEServer `yaml:"ice_servers,omitempty"` // STUN/TURN servers for WebRTC
	DirectPort int         `yaml:"direct_port,omitempty"` // port for direct WebSocket connections
	DirectTLS  bool        `yaml:"direct_tls,omitempty"`  // enable TLS for direct mode

	// JWT signing key for roost mode (base64-DER P-256 private key).
	// Auto-generated on first roost start. Server mode uses WT_JWT_KEY env instead.
	JWTKey string `yaml:"jwt_key,omitempty"`

	// ToolsDir is the directory containing privileged tool YAML configs.
	// Defaults to ~/.wingthing/tools/ if empty.
	ToolsDir string `yaml:"tools_dir,omitempty"`

	// MCP is the optional OAuth-gated remote surface over privileged tools.
	MCP       *MCPConfig       `yaml:"mcp,omitempty"`
	DirectMCP *DirectMCPConfig `yaml:"direct_mcp,omitempty"`
}

// Clone returns an independent runtime snapshot. Wing callbacks outlive config
// reloads, so sharing slice/map storage would let a SIGHUP or ACL mutation race
// an already admitted session.
func (c *WingConfig) Clone() *WingConfig {
	if c == nil {
		return nil
	}
	clone := *c
	clone.Labels = append([]string(nil), c.Labels...)
	clone.AllowKeys = append([]AllowKey(nil), c.AllowKeys...)
	clone.Admins = append([]string(nil), c.Admins...)
	clone.Paths = make(PathList, len(c.Paths))
	for index, path := range c.Paths {
		clone.Paths[index] = path
		clone.Paths[index].Members = append([]string(nil), path.Members...)
	}
	clone.ICEServers = make([]ICEServer, len(c.ICEServers))
	for index, server := range c.ICEServers {
		clone.ICEServers[index] = server
		clone.ICEServers[index].URLs = append([]string(nil), server.URLs...)
	}
	if c.DirectMCP != nil {
		direct := *c.DirectMCP
		direct.AllowGrants = append([]string(nil), c.DirectMCP.AllowGrants...)
		direct.DenyGrants = append([]string(nil), c.DirectMCP.DenyGrants...)
		clone.DirectMCP = &direct
	}
	if c.MCP != nil {
		mcp := *c.MCP
		mcp.Roles = make(map[string]*MCPRoleConfig, len(c.MCP.Roles))
		for name, role := range c.MCP.Roles {
			if role == nil {
				mcp.Roles[name] = nil
				continue
			}
			roleClone := *role
			roleClone.Allow = append([]string(nil), role.Allow...)
			roleClone.Deny = append([]string(nil), role.Deny...)
			roleClone.Members = append([]string(nil), role.Members...)
			mcp.Roles[name] = &roleClone
		}
		clone.MCP = &mcp
	}
	return &clone
}

// IsAdmin returns true if email is in the Admins list (case-insensitive).
func (c *WingConfig) IsAdmin(email string) bool {
	emailLower := strings.ToLower(email)
	for _, a := range c.Admins {
		if strings.ToLower(a) == emailLower {
			return true
		}
	}
	return false
}

// EffectiveHostedRelay returns the additive hosted relay policy. Empty means
// allow so existing wing.yaml files retain their current behavior.
func (c *WingConfig) EffectiveHostedRelay() string {
	if c.HostedRelay == "" {
		return HostedRelayAllow
	}
	return c.HostedRelay
}

func (c *WingConfig) HostedRelayAllowed() bool {
	return c.EffectiveHostedRelay() == HostedRelayAllow
}

// ICEServer is a STUN/TURN server configuration for WebRTC P2P connections.
type ICEServer struct {
	URLs       []string `yaml:"urls" json:"urls"`
	Username   string   `yaml:"username,omitempty" json:"username,omitempty"`
	Credential string   `yaml:"credential,omitempty" json:"credential,omitempty"`
}

// AllowKey is an allowed user for wing access control.
type AllowKey struct {
	Key    string `yaml:"key,omitempty"`     // base64 raw P-256 public key (optional)
	UserID string `yaml:"user_id,omitempty"` // relay user ID
	Email  string `yaml:"email,omitempty"`   // auto-set from relay, for display
}

// PathEntry is a directory path with optional per-folder member ACLs.
// When Members is nil/empty, the path is visible to all authenticated users (legacy behavior).
// When Members is set, only those emails + owner/admin can access the path.
type PathEntry struct {
	Path    string   `yaml:"path" json:"path"`
	Members []string `yaml:"members,omitempty" json:"members,omitempty"`
}

// PathList is a list of PathEntry values that supports mixed YAML formats:
// plain strings ("~/repos") and mappings ({path: ~/repos, members: [...]}).
type PathList []PathEntry

// UnmarshalYAML handles both scalar strings and mapping nodes in a YAML sequence.
func (pl *PathList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return &yaml.TypeError{Errors: []string{"expected sequence"}}
	}
	var result PathList
	for _, item := range value.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			result = append(result, PathEntry{Path: item.Value})
		case yaml.MappingNode:
			var entry PathEntry
			if err := item.Decode(&entry); err != nil {
				return err
			}
			result = append(result, entry)
		}
	}
	*pl = result
	return nil
}

// MarshalYAML serializes PathList: entries without members become plain strings (backwards compat).
func (pl PathList) MarshalYAML() (any, error) {
	var nodes []*yaml.Node
	for _, e := range pl {
		if len(e.Members) == 0 {
			nodes = append(nodes, &yaml.Node{Kind: yaml.ScalarNode, Value: e.Path})
		} else {
			var n yaml.Node
			if err := n.Encode(e); err != nil {
				return nil, err
			}
			nodes = append(nodes, &n)
		}
	}
	return &yaml.Node{Kind: yaml.SequenceNode, Content: nodes}, nil
}

// Strings returns just the path strings (drop-in for existing callers that need []string).
func (pl PathList) Strings() []string {
	out := make([]string, len(pl))
	for i, e := range pl {
		out[i] = e.Path
	}
	return out
}

// PathsForUser returns paths accessible to the given email/orgRole.
// Owner/admin get all paths. Members get only entries where their email is in Members
// (case-insensitive) or where Members is empty/nil (legacy open entries).
func (pl PathList) PathsForUser(email, orgRole string) []string {
	if orgRole == "owner" || orgRole == "admin" {
		return pl.Strings()
	}
	emailLower := strings.ToLower(email)
	var out []string
	for _, e := range pl {
		if len(e.Members) == 0 {
			// Legacy entry: visible to all
			out = append(out, e.Path)
			continue
		}
		for _, m := range e.Members {
			if strings.ToLower(m) == emailLower {
				out = append(out, e.Path)
				break
			}
		}
	}
	return out
}

// LoadWingConfig reads wing.yaml from dir. If the file doesn't exist,
// it returns a zero-value config (no error). If a legacy wing-id file
// exists, the wing_id is seeded from it.
func LoadWingConfig(dir string) (*WingConfig, error) {
	cfg := &WingConfig{}
	path := filepath.Join(dir, "wing.yaml")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Migrate from legacy wing-id file
			if idData, idErr := os.ReadFile(filepath.Join(dir, "wing-id")); idErr == nil {
				cfg.WingID = strings.TrimSpace(string(idData))
			}
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.MCP != nil {
		if err := cfg.MCP.Validate(); err != nil {
			return nil, fmt.Errorf("validate %s mcp config: %w", path, err)
		}
	}
	if cfg.DirectMCP != nil {
		if err := cfg.DirectMCP.Validate(); err != nil {
			return nil, fmt.Errorf("validate %s direct_mcp config: %w", path, err)
		}
	}
	if cfg.HostedRelay != "" && cfg.HostedRelay != HostedRelayAllow && cfg.HostedRelay != HostedRelayDeny {
		return nil, fmt.Errorf("validate %s hosted_relay: expected %q or %q, got %q", path, HostedRelayAllow, HostedRelayDeny, cfg.HostedRelay)
	}
	// Migrate legacy root -> paths
	if cfg.Root != "" && len(cfg.Paths) == 0 {
		cfg.Paths = PathList{{Path: cfg.Root}}
	}
	return cfg, nil
}

// SaveWingConfig writes wing.yaml to dir. The file may contain the roost's JWT signing
// key, so it must never be readable by other local users.
func SaveWingConfig(dir string, cfg *WingConfig) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create wing config directory: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "wing.yaml")
	tmp, err := os.CreateTemp(dir, ".wing.yaml-*")
	if err != nil {
		return fmt.Errorf("create temporary wing config: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return fmt.Errorf("restrict temporary wing config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary wing config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary wing config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary wing config: %w", err)
	}
	// Rename replaces a legacy permissive file or symlink atomically. Readers
	// therefore see either the complete old config or the complete new config;
	// a crash cannot leave a truncated JWT signing key or access-control policy.
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace wing config: %w", err)
	}
	committed = true
	// Persist the directory entry as well as the file contents. Some filesystems
	// otherwise permit a successful rename to disappear after sudden power loss.
	if err := fsutil.SyncDirectory(dir); err != nil {
		return fmt.Errorf("persist wing config replacement: %w", err)
	}
	return nil
}
