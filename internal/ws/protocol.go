package ws

// Message types for the relay WebSocket protocol.
const (
	// Wing → Relay
	TypeWingRegister  = "wing.register"
	TypeWingHeartbeat = "wing.heartbeat"

	// PTY routing. Input/output payloads are application-encrypted; lifecycle
	// envelopes remain visible. Resize/kill use the encrypted tunnel in current
	// clients, and current wings reject their legacy plaintext relay forms.
	TypePTYStart        = "pty.start"         // browser → relay → wing
	TypePTYStarted      = "pty.started"       // wing → relay → browser
	TypePTYOutput       = "pty.output"        // wing → relay → browser
	TypePTYInput        = "pty.input"         // browser → relay → wing
	TypePTYResize       = "pty.resize"        // legacy relay form; current wings reject it
	TypePTYExited       = "pty.exited"        // wing → relay → browser
	TypePTYAttach       = "pty.attach"        // browser → relay → wing (reattach)
	TypePTYKill         = "pty.kill"          // legacy relay form; current wings reject it
	TypePTYDetach       = "pty.detach"        // browser → relay (explicit detach before disconnect)
	TypePTYAttentionAck = "pty.attention_ack" // browser → relay → wing (notification seen)
	TypePTYPreview      = "pty.preview"       // wing → relay → browser (ephemeral)
	TypePTYBrowserOpen  = "pty.browser_open"  // wing → relay → browser (URL open request)
	TypePTYMigrate      = "pty.migrate"       // browser → relay → wing (request P2P migration)
	TypePTYMigrated     = "pty.migrated"      // wing → relay → browser (P2P migration complete)
	TypePTYFallback     = "pty.fallback"      // wing → relay → browser (P2P failed, back to relay)

	// Encrypted tunnel (browser ↔ wing, relay is opaque forwarder)
	TypeTunnelRequest  = "tunnel.req"    // browser → relay → wing
	TypeTunnelResponse = "tunnel.res"    // wing → relay → browser
	TypeTunnelStream   = "tunnel.stream" // wing → relay → browser (streaming)

	// Wing → Relay (session attention broadcast)
	TypeSessionAttention = "session.attention"

	// Relay → Browser/Wing (bandwidth)
	TypeBandwidthExceeded = "bandwidth.exceeded"

	// Passkey challenge-response (wing ↔ browser, relay is passthrough)
	TypePasskeyChallenge = "passkey.challenge"
	TypePasskeyResponse  = "passkey.response"

	// Relay → Wing (passkey lifecycle event)
	TypePasskeyRegistered = "passkey.registered"

	// Wing → Relay (config change)
	TypeWingConfig = "wing.config"

	// Relay → Wing (control)
	TypeRegistered   = "registered"
	TypeRelayRestart = "relay.restart" // relay → all: server shutting down, reconnect
	TypeWingOffline  = "wing.offline"  // relay → PTY browsers: wing disconnected
	TypeError        = "error"

	TunnelPurposeSignal    = "webrtc-signal"
	TunnelPurposeDiscovery = "wing-discovery"
	TunnelPurposePasskey   = "passkey-auth"
	TunnelPurposeControl   = "wing-control"

	HostedRelayAllow = "allow"
	HostedRelayDeny  = "deny"

	MaxCoordinationTunnelPayload = 256 * 1024
)

// ValidSessionID reports whether an ID is safe to use as one filesystem path
// component and as a bounded in-memory routing key. Relay-created IDs are
// compact UUID-derived values, while the broader character set preserves
// compatibility with older short local sessions and human-readable
// test/automation IDs.
func ValidSessionID(sessionID string) bool {
	if sessionID == "" || sessionID == "." || sessionID == ".." || len(sessionID) > 128 {
		return false
	}
	for _, r := range sessionID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// TunnelPurposeForInnerType declares the coordinator-visible class of an
// otherwise encrypted tunnel request. Direct-only accounts may use only the
// bounded coordination classes; the wing verifies the declaration after it
// decrypts the inner type.
func TunnelPurposeForInnerType(innerType string) string {
	switch innerType {
	case "webrtc.offer":
		return TunnelPurposeSignal
	case "wing.info":
		return TunnelPurposeDiscovery
	case "passkey.auth.begin", "passkey.auth.finish":
		return TunnelPurposePasskey
	default:
		return TunnelPurposeControl
	}
}

func IsCoordinationTunnelPurpose(purpose string) bool {
	return purpose == TunnelPurposeSignal || purpose == TunnelPurposeDiscovery || purpose == TunnelPurposePasskey
}

func TunnelPurposeMatches(purpose, innerType string) bool {
	return purpose == TunnelPurposeForInnerType(innerType)
}

// HostedRelayAllowed preserves compatibility with older wings that omit the
// additive policy. Unknown explicit values fail closed.
func HostedRelayAllowed(policy string) bool {
	return policy == "" || policy == HostedRelayAllow
}

// WingConfig is sent by the wing when lock state changes (e.g. lock/unlock, allow/revoke).
type WingConfig struct {
	Type         string `json:"type"`
	WingID       string `json:"wing_id"`
	Locked       bool   `json:"locked"`
	AllowedCount int    `json:"allowed_count"`
	DirectMCP    bool   `json:"direct_mcp,omitempty"`
	HostedRelay  string `json:"hosted_relay,omitempty"`
}

// Envelope wraps every WebSocket message with a type field for routing.
type Envelope struct {
	Type string `json:"type"`
}

// WingProject is a project directory discovered on the wing.
type WingProject struct {
	Name    string `json:"name"`               // directory name (e.g. "wingthing")
	Path    string `json:"path"`               // absolute path (e.g. "/Users/ehrlich/repos/wingthing")
	ModTime int64  `json:"mod_time,omitempty"` // unix timestamp of last modification
}

// WingRegister is sent by the wing on connect.
type WingRegister struct {
	Type         string        `json:"type"`
	WingID       string        `json:"wing_id"`
	Hostname     string        `json:"hostname,omitempty"`
	Platform     string        `json:"platform,omitempty"` // runtime.GOOS (e.g. "darwin", "linux")
	Version      string        `json:"version,omitempty"`  // build version (e.g. "v0.7.35")
	Agents       []string      `json:"agents"`
	Skills       []string      `json:"skills"`
	Labels       []string      `json:"labels"`
	Identities   []string      `json:"identities"`
	Projects     []WingProject `json:"projects,omitempty"`
	OrgSlug      string        `json:"org_slug,omitempty"`
	RootDir      string        `json:"root_dir,omitempty"`
	PublicKey    string        `json:"public_key,omitempty"`    // wing's X25519 identity key (base64)
	Locked       bool          `json:"locked"`                  // explicit locked flag from wing.yaml
	AllowedCount int           `json:"allowed_count,omitempty"` // number of allowed keys
	// PurposeBinding means this wing verifies the coordinator-visible tunnel
	// purpose against the decrypted inner message before dispatch.
	PurposeBinding bool   `json:"purpose_binding,omitempty"`
	DirectMCP      bool   `json:"direct_mcp,omitempty"`
	HostedRelay    string `json:"hosted_relay,omitempty"`
}

// WingHeartbeat is sent by the wing every 30s.
type WingHeartbeat struct {
	Type   string `json:"type"`
	WingID string `json:"wing_id"`
}

// RegisteredMsg is the relay's acknowledgment of a successful wing registration.
type RegisteredMsg struct {
	Type           string   `json:"type"`
	WingID         string   `json:"wing_id"`
	RelayPubKey    string   `json:"relay_pub_key,omitempty"` // base64 DER EC P-256 public key for JWT verification
	PasskeyRPID    string   `json:"passkey_rp_id,omitempty"`
	PasskeyOrigins []string `json:"passkey_origins,omitempty"`
}

// ErrorMsg is sent for protocol/session errors. Session and viewer IDs let a
// relay route an authorization failure back to the pending attach attempt.
type ErrorMsg struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ViewerID  string `json:"viewer_id,omitempty"`
}

// PTYStart requests a new interactive terminal session on the wing.
type PTYStart struct {
	Type                string   `json:"type"`
	SessionID           string   `json:"session_id"`
	Agent               string   `json:"agent"` // "claude", "codex", "ollama"
	Cols                int      `json:"cols"`
	Rows                int      `json:"rows"`
	PublicKey           string   `json:"public_key,omitempty"`            // browser's ephemeral X25519 (base64)
	CWD                 string   `json:"cwd,omitempty"`                   // working directory for the agent
	WingID              string   `json:"wing_id,omitempty"`               // target wing (picks first if empty)
	PasskeyCredentialID string   `json:"passkey_credential_id,omitempty"` // base64url credential ID
	AuthToken           string   `json:"auth_token,omitempty"`            // cached passkey auth token
	UserID              string   `json:"user_id,omitempty"`               // relay-injected creator user ID
	Email               string   `json:"email,omitempty"`                 // relay-injected user email
	DisplayName         string   `json:"display_name,omitempty"`          // relay-injected display name (Google full name, GitHub login)
	OrgRole             string   `json:"org_role,omitempty"`              // relay-injected: "owner", "admin", "member", ""
	Passkeys            []string `json:"passkeys,omitempty"`              // relay-injected: base64 raw P-256 public keys
}

// PTYStarted confirms the PTY session is running.
type PTYStarted struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	PublicKey string `json:"public_key,omitempty"` // wing's X25519 (base64)
	CWD       string `json:"cwd,omitempty"`        // resolved working directory
	AuthToken string `json:"auth_token,omitempty"` // passkey auth token
	ViewerID  string `json:"viewer_id,omitempty"`  // spectator viewer ID (relay-assigned)
}

// PasskeyChallenge is sent from wing to browser requesting passkey verification.
type PasskeyChallenge struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Challenge string `json:"challenge"` // base64url random 32 bytes
	RPID      string `json:"rp_id"`     // wing-configured WebAuthn relying-party ID
	ViewerID  string `json:"viewer_id,omitempty"`
}

// PasskeyResponse is sent from browser to wing with the passkey assertion.
type PasskeyResponse struct {
	Type              string `json:"type"`
	SessionID         string `json:"session_id"`
	ViewerID          string `json:"viewer_id,omitempty"`
	CredentialID      string `json:"credential_id"`      // base64url
	AuthenticatorData string `json:"authenticator_data"` // base64
	ClientDataJSON    string `json:"client_data_json"`   // base64
	Signature         string `json:"signature"`          // base64 (ASN.1 DER)
}

// PTYOutput carries raw terminal bytes from wing to browser.
type PTYOutput struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id"`
	Data       string `json:"data"`                 // base64-encoded
	Compressed bool   `json:"compressed,omitempty"` // gzip before encrypt
	ViewerID   string `json:"viewer_id,omitempty"`  // spectator viewer ID (for relay routing)
}

// PTYPreview carries preview panel data (URL or markdown) from wing to browser.
type PTYPreview struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Data      string `json:"data"`                // base64(AES-GCM encrypted JSON)
	ViewerID  string `json:"viewer_id,omitempty"` // spectator viewer ID (for relay routing)
}

// PTYInput carries keystrokes from browser to wing.
type PTYInput struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Data      string `json:"data"` // base64-encoded
}

// PTYResize tells the wing to resize the terminal.
type PTYResize struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// PTYExited tells the browser the process exited.
type PTYExited struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	ExitCode  int    `json:"exit_code"`
	Error     string `json:"error,omitempty"`     // crash/error info for display
	ViewerID  string `json:"viewer_id,omitempty"` // spectator viewer ID (for relay routing)
}

// PTYAttach requests reattachment to an existing PTY session.
type PTYAttach struct {
	Type      string   `json:"type"`
	SessionID string   `json:"session_id"`
	PublicKey string   `json:"public_key,omitempty"` // new browser ephemeral key
	WingID    string   `json:"wing_id,omitempty"`    // target wing (for relay routing)
	AuthToken string   `json:"auth_token,omitempty"` // cached passkey auth token
	UserID    string   `json:"user_id,omitempty"`    // relay-injected
	OrgRole   string   `json:"org_role,omitempty"`   // relay-injected: "owner", "admin", "member", ""
	Cols      uint32   `json:"cols,omitempty"`       // browser terminal cols (for resize-before-snapshot)
	Rows      uint32   `json:"rows,omitempty"`       // browser terminal rows (for resize-before-snapshot)
	Spectate  bool     `json:"spectate,omitempty"`   // read-only spectator mode
	ViewerID  string   `json:"viewer_id,omitempty"`  // relay-assigned attach attempt ID
	Email     string   `json:"email,omitempty"`      // relay-injected user email
	Passkeys  []string `json:"passkeys,omitempty"`   // relay-injected passkey attestation
}

// PTYKill requests termination of a PTY session.
type PTYKill struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// PTYDetach explicitly detaches the browser from a PTY session.
type PTYDetach struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// PTYAttentionAck acknowledges a notification was seen by the browser.
type PTYAttentionAck struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// TunnelRequest is an encrypted request from browser to wing via relay.
type TunnelRequest struct {
	Type           string   `json:"type"`
	WingID         string   `json:"wing_id"`
	RequestID      string   `json:"request_id"`
	Purpose        string   `json:"purpose,omitempty"`
	SenderPub      string   `json:"sender_pub,omitempty"`      // browser X25519 identity pubkey
	Payload        string   `json:"payload"`                   // base64(AES-GCM encrypted)
	SenderUserID   string   `json:"sender_user_id,omitempty"`  // relay-injected user ID
	SenderOrgRole  string   `json:"sender_org_role,omitempty"` // relay-injected: "owner", "admin", "member", ""
	SenderEmail    string   `json:"sender_email,omitempty"`    // relay-injected user email
	SenderPasskeys []string `json:"sender_passkeys,omitempty"` // relay-injected: base64 raw P-256 public keys
}

// TunnelResponse is an encrypted response from wing to browser via relay.
type TunnelResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Payload   string `json:"payload"` // base64(AES-GCM encrypted)
}

// TunnelStream is an encrypted streaming chunk from wing to browser via relay.
type TunnelStream struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Payload   string `json:"payload"` // base64(AES-GCM encrypted)
	Done      bool   `json:"done"`
}

// SessionAttention is sent by the wing when a session needs user attention (bell detected).
type SessionAttention struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	Nonce     string `json:"nonce,omitempty"` // dedup key: same nonce = same attention episode
}

// SessionInfo describes one active session on a wing (used in tunnel sessions.list responses).
type SessionInfo struct {
	SessionID      string `json:"session_id"`
	Agent          string `json:"agent"`
	CWD            string `json:"cwd,omitempty"`
	EggConfig      string `json:"egg_config,omitempty"` // YAML config snapshot
	NeedsAttention bool   `json:"needs_attention,omitempty"`
	Audit          bool   `json:"audit,omitempty"` // true if session has audit recording
	Chat           bool   `json:"chat,omitempty"`  // true if session has chat history
	UserID         string `json:"user_id,omitempty"`
	Email          string `json:"email,omitempty"`
}

// DirEntry is a single entry in a directory listing.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Path  string `json:"path"`
}

// PTYWriteFunc sends a message back to the relay over the wing's WebSocket.
type PTYWriteFunc func(v any) error

// BandwidthExceeded is sent to browser/wing when monthly cap is hit.
type BandwidthExceeded struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// PasskeyRegistered is sent from relay to wings when a user registers a new passkey.
// Lightweight event — actual key data flows through relay-enriched messages at auth time.
type PasskeyRegistered struct {
	Type   string `json:"type"`
	UserID string `json:"user_id"`
	Email  string `json:"email,omitempty"`
}

// PTYBrowserOpen notifies the browser that an agent requested a URL open.
type PTYBrowserOpen struct {
	Type      string `json:"type"` // "pty.browser_open"
	SessionID string `json:"session_id"`
	URL       string `json:"url"`
}

// PTYMigrate requests migration of a PTY session to a P2P DataChannel.
type PTYMigrate struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	AuthToken string `json:"auth_token,omitempty"` // passkey auth token for re-validation
}

// PTYMigrated confirms a PTY session has been migrated to a P2P DataChannel.
type PTYMigrated struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// PTYFallback notifies the browser that a P2P DataChannel died and I/O is back on the relay.
type PTYFallback struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// RelayRestart is sent to all connected WebSockets when the server is shutting down.
type RelayRestart struct {
	Type string `json:"type"`
}
