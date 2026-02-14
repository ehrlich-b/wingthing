package ws

// Message types for the relay WebSocket protocol.
const (
	// Wing → Relay
	TypeWingRegister  = "wing.register"
	TypeWingHeartbeat = "wing.heartbeat"

	// PTY (bidirectional, already E2E encrypted)
	TypePTYStart        = "pty.start"         // browser → relay → wing
	TypePTYStarted      = "pty.started"       // wing → relay → browser
	TypePTYOutput       = "pty.output"        // wing → relay → browser
	TypePTYInput        = "pty.input"         // browser → relay → wing
	TypePTYResize       = "pty.resize"        // browser → relay → wing
	TypePTYExited       = "pty.exited"        // wing → relay → browser
	TypePTYAttach       = "pty.attach"        // browser → relay → wing (reattach)
	TypePTYKill         = "pty.kill"          // browser → relay → wing (terminate session)
	TypePTYDetach       = "pty.detach"        // browser → relay (explicit detach before disconnect)
	TypePTYAttentionAck = "pty.attention_ack" // browser → relay → wing (notification seen)

	// Encrypted tunnel (browser ↔ wing, relay is opaque forwarder)
	TypeTunnelRequest  = "tunnel.req"    // browser → relay → wing
	TypeTunnelResponse = "tunnel.res"    // wing → relay → browser
	TypeTunnelStream   = "tunnel.stream" // wing → relay → browser (streaming)

	// Wing → Relay (session reclaim after wing restart)
	TypePTYReclaim = "pty.reclaim"

	// Session sync (relay requests, wing responds; also sent on heartbeat)
	TypeSessionsList = "sessions.list" // relay → wing
	TypeSessionsSync = "sessions.sync" // wing → relay

	// Relay → Browser/Wing (bandwidth)
	TypeBandwidthExceeded = "bandwidth.exceeded"

	// Passkey challenge-response (wing ↔ browser, relay is passthrough)
	TypePasskeyChallenge = "passkey.challenge"
	TypePasskeyResponse  = "passkey.response"

	// Wing → Relay (config change)
	TypeWingConfig = "wing.config"

	// Relay → Wing (control)
	TypeRegistered   = "registered"
	TypeRelayRestart = "relay.restart" // relay → all: server shutting down, reconnect
	TypeError        = "error"
)

// WingConfig is sent by the wing when lock state changes (e.g. lock/unlock, allow/revoke).
type WingConfig struct {
	Type         string `json:"type"`
	WingID       string `json:"wing_id"`
	Locked       bool   `json:"locked"`
	AllowedCount int    `json:"allowed_count"`
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
	Type        string        `json:"type"`
	WingID      string        `json:"wing_id"`
	Hostname    string        `json:"hostname,omitempty"`
	Platform    string        `json:"platform,omitempty"` // runtime.GOOS (e.g. "darwin", "linux")
	Version     string        `json:"version,omitempty"`  // build version (e.g. "v0.7.35")
	Agents      []string      `json:"agents"`
	Skills      []string      `json:"skills"`
	Labels      []string      `json:"labels"`
	Identities  []string      `json:"identities"`
	Projects    []WingProject `json:"projects,omitempty"`
	OrgSlug     string        `json:"org_slug,omitempty"`
	RootDir     string        `json:"root_dir,omitempty"`
	Locked       bool          `json:"locked"`                 // explicit locked flag from wing.yaml
	AllowedCount int           `json:"allowed_count,omitempty"` // number of allowed keys
}

// WingHeartbeat is sent by the wing every 30s.
type WingHeartbeat struct {
	Type   string `json:"type"`
	WingID string `json:"wing_id"`
}

// RegisteredMsg is the relay's acknowledgment of a successful wing registration.
type RegisteredMsg struct {
	Type   string `json:"type"`
	WingID string `json:"wing_id"`
}

// ErrorMsg is sent by the relay for protocol errors.
type ErrorMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// PTYStart requests a new interactive terminal session on the wing.
type PTYStart struct {
	Type                string `json:"type"`
	SessionID           string `json:"session_id"`
	Agent               string `json:"agent"` // "claude", "codex", "ollama"
	Cols                int    `json:"cols"`
	Rows                int    `json:"rows"`
	PublicKey           string `json:"public_key,omitempty"`            // browser's ephemeral X25519 (base64)
	CWD                 string `json:"cwd,omitempty"`                   // working directory for the agent
	WingID              string `json:"wing_id,omitempty"`               // target wing (picks first if empty)
	PasskeyCredentialID string `json:"passkey_credential_id,omitempty"` // base64url credential ID
	AuthToken           string `json:"auth_token,omitempty"`            // cached passkey auth token
	UserID              string `json:"user_id,omitempty"`               // relay-injected creator user ID
}

// PTYStarted confirms the PTY session is running.
type PTYStarted struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	PublicKey string `json:"public_key,omitempty"` // wing's X25519 (base64)
	CWD       string `json:"cwd,omitempty"`        // resolved working directory
	AuthToken string `json:"auth_token,omitempty"` // passkey auth token
}

// PasskeyChallenge is sent from wing to browser requesting passkey verification.
type PasskeyChallenge struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Challenge string `json:"challenge"` // base64url random 32 bytes
}

// PasskeyResponse is sent from browser to wing with the passkey assertion.
type PasskeyResponse struct {
	Type              string `json:"type"`
	SessionID         string `json:"session_id"`
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
	Error     string `json:"error,omitempty"` // crash/error info for display
}

// PTYAttach requests reattachment to an existing PTY session.
type PTYAttach struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	PublicKey string `json:"public_key,omitempty"` // new browser ephemeral key
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
	Type          string `json:"type"`
	WingID        string `json:"wing_id"`
	RequestID     string `json:"request_id"`
	SenderPub     string `json:"sender_pub,omitempty"`      // browser X25519 identity pubkey
	Payload       string `json:"payload"`                   // base64(AES-GCM encrypted)
	SenderUserID  string `json:"sender_user_id,omitempty"`  // relay-injected user ID
	SenderOrgRole string `json:"sender_org_role,omitempty"` // relay-injected: "owner", "admin", "member", ""
	SenderEmail   string `json:"sender_email,omitempty"`    // relay-injected user email
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

// PTYReclaim is sent by the wing after reconnect to reclaim a surviving egg session.
type PTYReclaim struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent,omitempty"`
	CWD       string `json:"cwd,omitempty"`
}

// SessionsList requests the wing's current session list.
type SessionsList struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

// SessionsSync carries the wing's current session list.
type SessionsSync struct {
	Type      string        `json:"type"`
	RequestID string        `json:"request_id,omitempty"` // set when responding to sessions.list
	Sessions  []SessionInfo `json:"sessions"`
}

// SessionInfo describes one active session on a wing.
type SessionInfo struct {
	SessionID      string `json:"session_id"`
	Agent          string `json:"agent"`
	CWD            string `json:"cwd,omitempty"`
	EggConfig      string `json:"egg_config,omitempty"` // YAML config snapshot
	NeedsAttention bool   `json:"needs_attention,omitempty"`
	Audit          bool   `json:"audit,omitempty"` // true if session has audit recording
	UserID         string `json:"user_id,omitempty"`
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

// RelayRestart is sent to all connected WebSockets when the server is shutting down.
type RelayRestart struct {
	Type string `json:"type"`
}
