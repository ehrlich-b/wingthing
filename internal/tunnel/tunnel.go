package tunnel

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/localmcp"
	"github.com/ehrlich-b/wingthing/internal/procinfo"
	"github.com/ehrlich-b/wingthing/internal/sessionfiles"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/wingpolicy"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

const maxTunnelKeyCacheEntries = 1024

// tunnelKeyCache bounds derived AES-GCM keys per sender public key. Direct-free
// users may send coordination tunnels, so a process-lifetime sync.Map would let
// an authenticated caller grow the wing indefinitely by rotating X25519 keys.
type tunnelKeyCache struct {
	mu      sync.Mutex
	max     int
	entries map[string]cipher.AEAD
	order   []string
}

func newTunnelKeyCache(max int) *tunnelKeyCache {
	return &tunnelKeyCache{max: max, entries: make(map[string]cipher.AEAD)}
}

func (c *tunnelKeyCache) Get(senderPub string) (cipher.AEAD, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key, ok := c.entries[senderPub]
	return key, ok
}

func (c *tunnelKeyCache) Put(senderPub string, key cipher.AEAD) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[senderPub]; exists {
		c.entries[senderPub] = key
		return
	}
	if c.max <= 0 {
		return
	}
	for len(c.entries) >= c.max && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.entries[senderPub] = key
	c.order = append(c.order, senderPub)
}

func (c *tunnelKeyCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

var tunnelKeys = newTunnelKeyCache(maxTunnelKeyCacheEntries)

// tunnelInner is the decrypted JSON payload inside a tunnel request.
type tunnelInner struct {
	ControllerID string          `json:"controller_id"`
	Type         string          `json:"type"`
	Operation    string          `json:"operation,omitempty"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
	Path         string          `json:"path,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	UploadID     string          `json:"upload_id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Size         int64           `json:"size,omitempty"`
	Data         string          `json:"data,omitempty"`
	Target       string          `json:"target,omitempty"`
	Kind         string          `json:"kind,omitempty"`
	YAML         string          `json:"yaml,omitempty"`
	Offset       int             `json:"offset,omitempty"`
	Limit        int             `json:"limit,omitempty"`
	Cols         int             `json:"cols,omitempty"`
	Rows         int             `json:"rows,omitempty"`
	AuthToken    string          `json:"auth_token,omitempty"`
	Key          string          `json:"key,omitempty"`           // passkey public key for allow.add
	AllowUserID  string          `json:"allow_user_id,omitempty"` // target user_id for allow.remove
	SDP          string          `json:"sdp,omitempty"`           // WebRTC SDP for webrtc.offer

	// Path ACL fields (for paths.set / paths.add_member / paths.remove_member)
	Paths   []config.PathEntry `json:"paths,omitempty"`   // for paths.set (bulk replace)
	Members []string           `json:"members,omitempty"` // for paths.set on a single path
	Email   string             `json:"email,omitempty"`   // for paths.add_member / paths.remove_member

	// Passkey assertion fields (for type "passkey.auth.finish")
	ChallengeID       string `json:"challenge_id,omitempty"`
	CredentialID      string `json:"credential_id,omitempty"`
	AuthenticatorData string `json:"authenticator_data,omitempty"`
	ClientDataJSON    string `json:"client_data_json,omitempty"`
	Signature         string `json:"signature,omitempty"`
}

// pastSessionInfo is the local version of PastSessionInfo for tunnel responses.
type pastSessionInfo struct {
	eggclient.ConversationLink
	SessionID               string `json:"session_id"`
	Name                    string `json:"name,omitempty"`
	Agent                   string `json:"agent"`
	CWD                     string `json:"cwd,omitempty"`
	StartedAt               int64  `json:"started_at,omitempty"`
	Audit                   bool   `json:"audit,omitempty"`
	Chat                    bool   `json:"chat,omitempty"`
	UserID                  string `json:"user_id,omitempty"`
	Resumable               bool   `json:"resumable,omitempty"`
	Recoverable             bool   `json:"recoverable,omitempty"`
	Status                  string `json:"status,omitempty"`
	ResumeUnavailableReason string `json:"resume_unavailable_reason,omitempty"`
}

// handleTunnelRequest decrypts and dispatches an encrypted tunnel request from the browser.
func HandleTunnelRequest(refs References, ctx context.Context, cfg *config.Config, req ws.TunnelRequest, write ws.PTYWriteFunc,
	passkeyCache *auth.AuthCache, passkeyChallenges *auth.ChallengeCache,
	passkeyPolicy auth.PasskeyPolicy, privKey *ecdh.PrivateKey, home string,
	audit, debug bool, client *ws.Client,
	peerMgr *webrtcpkg.PeerManager, dcSessions *sync.Map, sharedHostArg ...bool) {
	wingCfg := refs.WingCfg
	wingCfgMu := refs.WingCfgMu
	allowedKeysPtr := refs.AllowedKeys
	wingEggMu := refs.WingEggMu
	wingEggCfg := refs.WingEggCfg
	version := refs.Version
	listAliveEggSessions := refs.ListAliveEggSessions
	resizeBrowserInput := refs.ResizeBrowserInput
	killSessionsViolatingACLs := refs.KillSessionsViolatingACLs
	sharedHost := len(sharedHostArg) > 0 && sharedHostArg[0]

	// Tunnel callbacks run concurrently and some operations stream or perform
	// network/process work for an arbitrary amount of time. Admit each request
	// against an immutable policy snapshot; hold wingCfgMu only while taking that
	// snapshot or committing a serialized wing.yaml mutation. This keeps SIGHUP
	// revocation and unrelated requests responsive to a slow peer.
	liveWingCfg := wingCfg
	authenticatedOrgRole := req.SenderOrgRole
	wingCfgMu.Lock()
	wingCfg = liveWingCfg.Clone()
	allowedKeys := append([]config.AllowKey(nil), (*allowedKeysPtr)...)
	wingCfgMu.Unlock()

	// Wing-level admin override
	if wingCfg.IsAdmin(req.SenderEmail) && wingpolicy.IsMemberRole(req.SenderOrgRole) {
		req.SenderOrgRole = "admin"
	}

	// Derive or retrieve cached AES-GCM key for this sender
	var gcm cipher.AEAD
	if cached, ok := tunnelKeys.Get(req.SenderPub); ok {
		gcm = cached
	}
	if gcm == nil {
		derived, err := auth.DeriveSharedKey(privKey, req.SenderPub, "wt-tunnel")
		if err != nil {
			log.Printf("tunnel: derive key failed: %v", err)
			return
		}
		gcm = derived
		tunnelKeys.Put(req.SenderPub, gcm)
	}

	// Decrypt the payload
	plaintext, err := auth.Decrypt(gcm, req.Payload)
	if err != nil {
		log.Printf("tunnel %s: decrypt failed: %v", req.RequestID, err)
		return
	}

	// Parse inner message
	var inner tunnelInner
	if err := json.Unmarshal(plaintext, &inner); err != nil {
		log.Printf("tunnel %s: bad inner JSON: %v", req.RequestID, err)
		return
	}
	if inner.SessionID != "" && !ws.ValidSessionID(inner.SessionID) {
		log.Printf("tunnel %s: rejected invalid session ID %q", req.RequestID, inner.SessionID)
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid session ID"}, write)
		return
	}
	if req.Purpose != "" && !ws.TunnelPurposeMatches(req.Purpose, inner.Type) {
		log.Printf("tunnel %s: declared purpose %q does not match inner type %q", req.RequestID, req.Purpose, inner.Type)
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "tunnel purpose mismatch"}, write)
		return
	}
	// Every supported coordinator, including the N-1 org deployment, injects
	// authenticated sender identity. Treat its absence as a protocol failure,
	// not as legacy administrator authority.
	if req.SenderUserID == "" {
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "authenticated user identity required"}, write)
		return
	}

	isPasskeyCeremony := inner.Type == "passkey.auth.begin" || inner.Type == "passkey.auth.finish"
	subject := wingpolicy.PasskeySubject(req.SenderUserID, req.SenderPub)

	// Two-state auth check for locked wings. Relay-provided roles and passkey
	// keys are deliberately insufficient: the sender needs a key pinned in the
	// wing's local allowlist and a token bound to its encryption identity.
	if wingCfg.Locked && !isPasskeyCeremony {
		inList := subject != "" && len(wingpolicy.PasskeysForSubject(allowedKeys, req.SenderUserID)) > 0

		if !inList {
			// Not in allow list at all — locked
			ws.TunnelRespond(gcm, req.RequestID, map[string]any{
				"error": "not_allowed",
			}, write)
			return
		}

		// Step 2: In the list — check auth token
		var authTTL time.Duration // default 0 = boot-scoped, no expiry
		if wingCfg.AuthTTL != "" {
			if d, err := time.ParseDuration(wingCfg.AuthTTL); err == nil {
				authTTL = d
			}
		}
		authorized := false
		if inner.AuthToken != "" {
			if _, ok := passkeyCache.Check(inner.AuthToken, authTTL, subject); ok {
				authorized = true
			}
		}

		if !authorized {
			// In list but not yet authenticated — passkey challenge
			ws.TunnelRespond(gcm, req.RequestID, map[string]any{
				"error":    "passkey_required",
				"hostname": client.Hostname,
				"platform": client.Platform,
				"version":  version,
				"locked":   true,
			}, write)
			return
		}
	}

	// Per-user passkey enforcement on unlocked wings
	if !wingCfg.Locked && !isPasskeyCeremony {
		if req.SenderUserID != "" {
			userNeedsPasskey := len(wingpolicy.PasskeysForSubject(allowedKeys, req.SenderUserID)) > 0
			if userNeedsPasskey {
				var authTTL time.Duration
				if wingCfg.AuthTTL != "" {
					if d, err := time.ParseDuration(wingCfg.AuthTTL); err == nil {
						authTTL = d
					}
				}
				authorized := false
				if inner.AuthToken != "" {
					if _, ok := passkeyCache.Check(inner.AuthToken, authTTL, subject); ok {
						authorized = true
					}
				}
				if !authorized {
					ws.TunnelRespond(gcm, req.RequestID, map[string]any{
						"error":    "passkey_required",
						"hostname": client.Hostname,
						"platform": client.Platform,
						"version":  version,
						"locked":   false,
					}, write)
					return
				}
			}
		}
	}

	log.Printf("tunnel %s: %s (user=%s role=%s)", req.RequestID, inner.Type, req.SenderUserID, req.SenderOrgRole)

	switch inner.Type {
	case "dir.list":
		userPaths := wingpolicy.PathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home)
		entries := wingpolicy.RequestDirEntries(req, inner.Path, userPaths)
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{"entries": entries}, write)

	case "wing.info":
		userPaths := wingpolicy.PathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home)
		projects := wingpolicy.RequestProjects(req, client.Projects, userPaths)
		resp := map[string]any{
			"hostname":      client.Hostname,
			"platform":      client.Platform,
			"version":       version,
			"agents":        client.Agents,
			"projects":      projects,
			"locked":        wingCfg.Locked,
			"spectate":      wingCfg.Spectate,
			"allowed_count": len(wingCfg.AllowKeys),
			"hosted_relay":  wingCfg.EffectiveHostedRelay(),
			"file_limits": map[string]int64{
				"upload_bytes":   sessionfiles.MaxSessionUploadSize,
				"download_bytes": sessionfiles.MaxSessionDownloadSize,
				"export_bytes":   sessionfiles.MaxSessionDownloadSize,
			},
		}
		visibleExports := wingCfg.ExportsForUser(req.SenderEmail, req.SenderOrgRole)
		resp["capabilities"] = append(sessionfiles.BrowserSessionCapabilities(len(visibleExports) > 0), "session.lifecycle.v1")
		if wingCfg.Org == "" && !sharedHost {
			resp["capabilities"] = append(resp["capabilities"].([]string), "conversation.personal.v1")
		}
		if len(visibleExports) > 0 {
			exports := make([]map[string]string, 0, len(visibleExports))
			for _, target := range visibleExports {
				exports = append(exports, map[string]string{"name": target.Name, "type": "folder"})
			}
			resp["exports"] = exports
		}
		if wingCfg.Label != "" {
			resp["wing_label"] = wingCfg.Label
		}
		// Browser PTY migration remains opt-in even though the same PeerManager is
		// available in relay mode for native direct MCP control.
		if peerMgr != nil && (wingCfg.ConnectionMode == "p2p" || wingCfg.ConnectionMode == "p2p_only") {
			resp["p2p"] = true
			resp["connection_mode"] = wingCfg.ConnectionMode
		}
		if peerMgr != nil && len(wingCfg.ICEServers) > 0 {
			resp["ice_servers"] = wingCfg.ICEServers
		}
		// Report which well-known API keys are set in the wing's environment
		var globalKeys []string
		for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "CURSOR_API_KEY"} {
			if os.Getenv(k) != "" {
				globalKeys = append(globalKeys, k)
			}
		}
		if len(globalKeys) > 0 && !wingpolicy.IsMemberFiltered(req) {
			resp["global_keys"] = globalKeys
		}
		if req.SenderUserID != "" {
			enrolled := false
			for _, ak := range allowedKeys {
				if ak.UserID == req.SenderUserID && ak.Key != "" {
					enrolled = true
					break
				}
			}
			if enrolled {
				resp["passkey_enrolled"] = true
			}
		}
		ws.TunnelRespond(gcm, req.RequestID, resp, write)

	case "webrtc.offer":
		if peerMgr == nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]any{"error": "p2p not enabled"}, write)
			return
		}
		if inner.SDP == "" {
			ws.TunnelRespond(gcm, req.RequestID, map[string]any{"error": "missing sdp"}, write)
			return
		}
		answerSDP, err := peerMgr.HandleOffer(req.SenderPub, req.SenderUserID, req.SenderEmail, req.SenderOrgRole, req.SenderPasskeys, inner.SDP)
		if err != nil {
			log.Printf("[P2P] webrtc.offer failed: %v", err)
			ws.TunnelRespond(gcm, req.RequestID, map[string]any{"error": fmt.Sprintf("webrtc offer: %v", err)}, write)
			return
		}
		log.Printf("[P2P] webrtc.offer accepted from %s, answer SDP %d bytes", cmdutil.ShortLogValue(req.SenderPub), len(answerSDP))
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{"sdp": answerSDP}, write)

	case "sessions.list":
		sessions := listAliveEggSessions(cfg)
		if wingpolicy.IsMemberFiltered(req) {
			userPaths := wingpolicy.PathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home)
			var filtered []ws.SessionInfo
			for _, s := range sessions {
				if wingpolicy.CanSeeSession(req, s.UserID) && wingpolicy.CanAccessSessionPath(req, s.CWD, userPaths) {
					filtered = append(filtered, s)
				}
			}
			sessions = filtered
		}
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{"sessions": sessions}, write)

	case "session.control":
		result, err := localmcp.BrowserSessionControl(version, ctx, cfg, wingCfg, req, inner.Operation, inner.Arguments, home, sharedHost)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]any{"error": err.Error()}, write)
			return
		}
		ws.TunnelRespond(gcm, req.RequestID, result, write)

	case "sessions.history":
		sessions := collectSessionsHistory(cfg)
		if wingpolicy.IsMemberFiltered(req) {
			userPaths := wingpolicy.PathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home)
			sessions = filterSessionsHistoryForRequest(req, sessions, userPaths)
		}
		sessions, total := paginateSessionsHistory(sessions, inner.Offset, inner.Limit)
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{"sessions": sessions, "total": total}, write)

	case "sessions.rename":
		userPaths := wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home))
		if err := renameTunnelSession(cfg, req, inner.SessionID, inner.Name, listAliveEggSessions(cfg), userPaths); err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{"ok": true, "name": inner.Name}, write)

	case "file.upload.begin":
		userPaths := wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home))
		effectiveHome := eggclient.EffectiveSessionHome(cfg, eggclient.EggIdentity{UserID: req.SenderUserID, OrgWing: wingCfg.Org != "", SharedHost: sharedHost})
		session, policy, err := sessionfiles.ResolveOwnedSessionFileTarget(req, inner.SessionID, listAliveEggSessions(cfg), userPaths, effectiveHome)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		upload, err := sessionfiles.SessionUploads.Begin(session, policy, userPaths, req, inner.Name, inner.Size)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{"upload_id": upload.ID, "chunk_size": sessionfiles.MaxSessionUploadChunk, "path": filepath.Join(upload.Destination, upload.Name)}, write)

	case "file.upload.chunk":
		chunk, err := base64.StdEncoding.DecodeString(inner.Data)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid upload chunk"}, write)
			return
		}
		received, err := sessionfiles.SessionUploads.Append(inner.UploadID, req.SenderUserID, req.SenderPub, int64(inner.Offset), chunk)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		ws.TunnelRespond(gcm, req.RequestID, map[string]int64{"received": received}, write)

	case "file.upload.finish":
		upload, err := sessionfiles.SessionUploads.Finish(inner.UploadID, req.SenderUserID, req.SenderPub)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		defer func() { _ = upload.Root.Close() }()
		userPaths := wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home))
		effectiveHome := eggclient.EffectiveSessionHome(cfg, eggclient.EggIdentity{UserID: req.SenderUserID, OrgWing: wingCfg.Org != "", SharedHost: sharedHost})
		session, policy, err := sessionfiles.ResolveOwnedSessionFileTarget(req, upload.SessionID, listAliveEggSessions(cfg), userPaths, effectiveHome)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "session upload policy changed"}, write)
			return
		}
		destination, err := policy.UploadDirectory(userPaths)
		if err != nil || session.SessionID != upload.SessionID || destination != upload.Destination {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "session upload destination changed"}, write)
			return
		}
		sha, size, err := upload.Commit(destination)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		path := filepath.Join(upload.Destination, upload.Name)
		log.Printf("session upload complete (user=%s session=%s path=%q size=%d)", req.SenderUserID, upload.SessionID, path, size)
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{"name": upload.Name, "path": path, "size": size, "sha256": sha}, write)

	case "file.upload.cancel":
		if err := sessionfiles.SessionUploads.Cancel(inner.UploadID, req.SenderUserID, req.SenderPub); err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{"ok": true}, write)

	case "file.download":
		userPaths := wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home))
		effectiveHome := eggclient.EffectiveSessionHome(cfg, eggclient.EggIdentity{UserID: req.SenderUserID, OrgWing: wingCfg.Org != "", SharedHost: sharedHost})
		session, policy, err := sessionfiles.ResolveOwnedSessionFileTarget(req, inner.SessionID, listAliveEggSessions(cfg), userPaths, effectiveHome)
		if err != nil {
			_ = sessionfiles.StreamSessionFileError(gcm, req.RequestID, err.Error(), write)
			return
		}
		file, path, info, err := sessionfiles.OpenSessionFile(session, policy, userPaths, inner.Path)
		if err != nil {
			_ = sessionfiles.StreamSessionFileError(gcm, req.RequestID, err.Error(), write)
			return
		}
		defer cmdutil.CloseWithLog("session download", file)
		if err := sessionfiles.StreamSessionFile(ctx, file, path, info, gcm, req.RequestID, write); err != nil {
			log.Printf("session download failed (user=%s session=%s): %v", req.SenderUserID, inner.SessionID, err)
			_ = sessionfiles.StreamSessionFileError(gcm, req.RequestID, err.Error(), write)
		}

	case "file.export":
		userPaths := wingpolicy.CanonicalPaths(wingpolicy.PathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home))
		effectiveHome := eggclient.EffectiveSessionHome(cfg, eggclient.EggIdentity{UserID: req.SenderUserID, OrgWing: wingCfg.Org != "", SharedHost: sharedHost})
		session, policy, err := sessionfiles.ResolveOwnedSessionFileTarget(req, inner.SessionID, listAliveEggSessions(cfg), userPaths, effectiveHome)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		wingCfgMu.Lock()
		exportCfg := liveWingCfg.Clone()
		wingCfgMu.Unlock()
		if err := config.ValidateExports(cfg.Dir, exportCfg); err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "export destinations are invalid: " + err.Error()}, write)
			return
		}
		var exportTarget *config.ExportTarget
		for _, candidate := range exportCfg.ExportsForUser(req.SenderEmail, req.SenderOrgRole) {
			if candidate.Name == inner.Target {
				copy := candidate
				exportTarget = &copy
				break
			}
		}
		if exportTarget == nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "export destination is unavailable for this user"}, write)
			return
		}
		file, _, info, err := sessionfiles.OpenSessionFile(session, policy, userPaths, inner.Path)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		defer cmdutil.CloseWithLog("session export source", file)
		sha, size, err := sessionfiles.ExportSessionFile(file, info, *exportTarget, req.SenderUserID)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		log.Printf("session export complete (user=%s session=%s target=%s name=%q size=%d)", req.SenderUserID, inner.SessionID, exportTarget.Name, info.Name(), size)
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{"ok": true, "target": exportTarget.Name, "name": info.Name(), "size": size, "sha256": sha}, write)

	case "audit.request":
		if inner.SessionID != "" && wingpolicy.IsMemberFiltered(req) {
			sessionDir := filepath.Join(cfg.Dir, "eggs", inner.SessionID)
			userPaths := wingpolicy.PathsForRequest(wingCfg.Paths, req.SenderEmail, req.SenderOrgRole, home)
			if !eggclient.CanAccessSessionArtifact(req, sessionDir, userPaths) {
				log.Printf("tunnel %s: denied audit outside current owner/path policy (user=%s session=%s)", req.RequestID, req.SenderUserID, inner.SessionID)
				ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
				return
			}
		}
		streamAuditData(cfg, inner.SessionID, inner.Kind, gcm, req.RequestID, write)

	case "egg.config_update":
		// Rewrites the egg policy every session on this host runs under —
		// wing-wide administration, not a per-path member capability.
		if wingpolicy.IsMemberFiltered(req) {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "admin required"}, write)
			return
		}
		if inner.YAML == "" {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing yaml"}, write)
			return
		}
		newCfg, err := egg.LoadEggConfigFromYAML(inner.YAML)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		wingEggMu.Lock()
		*wingEggCfg = newCfg
		wingEggMu.Unlock()
		log.Printf("egg: config updated from tunnel (network=%s)", newCfg.NetworkSummary())
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "pty.kill":
		if inner.SessionID == "" {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing session_id"}, write)
			return
		}
		if wingpolicy.IsMemberFiltered(req) {
			owner := eggclient.ReadEggOwner(filepath.Join(cfg.Dir, "eggs", inner.SessionID))
			if !wingpolicy.CanSeeSession(req, owner) {
				log.Printf("tunnel %s: denied kill (user=%s session_owner=%s)", req.RequestID, req.SenderUserID, owner)
				ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
				return
			}
		}
		eggclient.KillOrphanEgg(cfg, inner.SessionID)
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "pty.resize":
		if inner.SessionID == "" || inner.Rows <= 0 || inner.Cols <= 0 {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing session_id or dimensions"}, write)
			return
		}
		if wingpolicy.IsMemberFiltered(req) {
			owner := eggclient.ReadEggOwner(filepath.Join(cfg.Dir, "eggs", inner.SessionID))
			if !wingpolicy.CanSeeSession(req, owner) {
				ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
				return
			}
		}
		var resizeErr error
		if config.Channel() == "preview" {
			resizeErr = resizeBrowserInput(ctx, inner.SessionID, inner.ControllerID, req.SenderPub, req.SenderUserID, uint32(inner.Rows), uint32(inner.Cols))
		} else {
			resizeErr = eggclient.ResizeEgg(cfg, inner.SessionID, uint32(inner.Rows), uint32(inner.Cols))
		}
		if err := resizeErr; err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "wing.update":
		// Replaces the host executable — wing-wide administration.
		if wingpolicy.IsMemberFiltered(req) {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "admin required"}, write)
			return
		}
		log.Println("tunnel: remote update requested")
		exe, exeErr := os.Executable()
		if exeErr != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": exeErr.Error()}, write)
			return
		}
		c := exec.Command(exe, "update")
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": err.Error()}, write)
			return
		}
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "passkey.auth.begin":
		if subject == "" {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "authenticated client identity required"}, write)
			return
		}
		if len(wingpolicy.PasskeysForSubject(allowedKeys, req.SenderUserID)) == 0 {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "not_allowed"}, write)
			return
		}
		challengeID, challenge, err := passkeyChallenges.Put(subject, time.Minute)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "challenge generation failed"}, write)
			return
		}
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{
			"challenge_id": challengeID,
			"challenge":    base64.RawURLEncoding.EncodeToString(challenge),
			"rp_id":        passkeyPolicy.RPID,
		}, write)

	case "passkey.auth.finish":
		if subject == "" || inner.ChallengeID == "" {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing passkey challenge"}, write)
			return
		}
		challenge, ok := passkeyChallenges.Consume(inner.ChallengeID, subject)
		if !ok {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid or expired passkey challenge"}, write)
			return
		}
		if _, err := base64.RawURLEncoding.DecodeString(inner.CredentialID); err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid credential ID"}, write)
			return
		}
		authData, err := base64.StdEncoding.DecodeString(inner.AuthenticatorData)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid authenticator data"}, write)
			return
		}
		cdJSON, err := base64.StdEncoding.DecodeString(inner.ClientDataJSON)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid client data"}, write)
			return
		}
		sig, err := base64.StdEncoding.DecodeString(inner.Signature)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid signature encoding"}, write)
			return
		}

		matchedKey, err := wingpolicy.VerifySubjectPasskey(allowedKeys, req.SenderUserID, challenge, authData, cdJSON, sig, passkeyPolicy)
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "passkey verification failed"}, write)
			return
		}
		token, err := auth.GenerateAuthToken()
		if err != nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "auth token generation failed"}, write)
			return
		}
		passkeyCache.Put(token, matchedKey, subject)
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"auth_token": token}, write)

	case "allow.list":
		type allowInfo struct {
			Key    string `json:"key"`
			UserID string `json:"user_id,omitempty"`
			Email  string `json:"email,omitempty"`
		}
		var allowed []allowInfo
		for _, ak := range wingpolicy.VisibleAllowKeys(req, allowedKeys) {
			allowed = append(allowed, allowInfo{Key: ak.Key, UserID: ak.UserID, Email: ak.Email})
		}
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{"allowed": allowed}, write)

	case "allow.add":
		if req.SenderUserID == "" {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "no user identity"}, write)
			return
		}
		// Validate key if provided
		if inner.Key != "" {
			keyBytes, decErr := base64.StdEncoding.DecodeString(inner.Key)
			if decErr != nil || !auth.IsValidP256Point(keyBytes) {
				ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "invalid key"}, write)
				return
			}
		}
		newEntry := config.AllowKey{
			Key:    inner.Key,
			UserID: req.SenderUserID,
			Email:  req.SenderEmail,
		}
		// In-memory only — don't persist to wing.yaml. Persisting here
		// clobbers shared wings (sets locked: true + allow_keys with only
		// the enrolling user, locking everyone else out).
		// Admins manage allow_keys explicitly via `wt wing allow`.
		wingCfgMu.Lock()
		if liveWingCfg.Locked {
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "locked wings require local approval via wt wing allow"}, write)
			return
		}
		for _, ak := range *allowedKeysPtr {
			if ak.UserID == req.SenderUserID {
				wingCfgMu.Unlock()
				ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "already allowed"}, write)
				return
			}
		}
		*allowedKeysPtr = append(*allowedKeysPtr, newEntry)
		wingCfgMu.Unlock()
		log.Printf("allowed: user=%s email=%s has_passkey=%v (session-scoped)", req.SenderUserID, req.SenderEmail, inner.Key != "")
		ws.TunnelRespond(gcm, req.RequestID, map[string]any{
			"ok": "true", "email": req.SenderEmail, "user_id": req.SenderUserID,
			"has_passkey": inner.Key != "",
		}, write)

	case "allow.remove":
		if req.SenderUserID == "" {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "no user identity"}, write)
			return
		}
		wingCfgMu.Lock()
		liveReq := wingpolicy.RequestAgainstWingConfig(req, authenticatedOrgRole, liveWingCfg)
		allowedKeys = append([]config.AllowKey(nil), (*allowedKeysPtr)...)
		// Find entry to remove: by key or user_id against the live ACL. A
		// SIGHUP between admission and this mutation must not resurrect an old
		// snapshot or authorize a removed local admin.
		target := inner.AllowUserID
		if target == "" && inner.Key != "" {
			for _, ak := range allowedKeys {
				if ak.Key == inner.Key {
					target = ak.UserID
					break
				}
			}
		}
		if target == "" {
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing allow_user_id or key"}, write)
			return
		}
		// Only wing owner or the entry's own user can remove
		isOwner := liveReq.SenderOrgRole == "owner" || liveReq.SenderOrgRole == "admin"
		if !isOwner && req.SenderUserID != target {
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "access denied"}, write)
			return
		}
		// Remove from persisted config (if present). Revocation is an ACL
		// mutation: serialize with the other wing.yaml writers, and roll the
		// in-memory change back if persistence fails so a request reported as
		// failed cannot leave a live-but-unsaved authorization change.
		persistedRemoved := false
		oldAllowKeys := append([]config.AllowKey(nil), liveWingCfg.AllowKeys...)
		for i, ak := range liveWingCfg.AllowKeys {
			if ak.UserID == target || (inner.Key != "" && ak.Key == inner.Key) {
				liveWingCfg.AllowKeys = append(liveWingCfg.AllowKeys[:i], liveWingCfg.AllowKeys[i+1:]...)
				persistedRemoved = true
				break
			}
		}
		if persistedRemoved {
			if saveErr := config.SaveWingConfig(cfg.Dir, liveWingCfg); saveErr != nil {
				liveWingCfg.AllowKeys = oldAllowKeys
				wingCfgMu.Unlock()
				ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "persist wing.yaml: " + saveErr.Error()}, write)
				return
			}
		}
		// Also remove from in-memory list (covers session-scoped entries)
		memRemoved := false
		for i, ak := range allowedKeys {
			if ak.UserID == target || (inner.Key != "" && ak.Key == inner.Key) {
				allowedKeys = append(allowedKeys[:i], allowedKeys[i+1:]...)
				memRemoved = true
				break
			}
		}
		if !persistedRemoved && !memRemoved {
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "entry not found"}, write)
			return
		}
		*allowedKeysPtr = allowedKeys
		locked, allowedCount := liveWingCfg.Locked, len(liveWingCfg.AllowKeys)
		wingCfgMu.Unlock()
		client.UpdateAccessConfig(locked, allowedCount)
		if err := client.SendConfig(ctx); err != nil {
			// The local policy mutation is already active and persisted. A later
			// reconnect will advertise it again, so report the transient sync
			// failure without rolling back the security decision.
			log.Printf("advertise access config after revoke: %v", err)
		}
		log.Printf("revoked: target=%s by=%s persisted=%v", target, req.SenderUserID, persistedRemoved)
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "paths.list":
		if !wingpolicy.IsMemberFiltered(req) {
			// Admin/owner: return full PathList with members
			ws.TunnelRespond(gcm, req.RequestID, map[string]any{"paths": wingCfg.Paths}, write)
		} else {
			// Member: return only their accessible paths, no member lists
			userPaths := wingCfg.Paths.PathsForUser(req.SenderEmail, req.SenderOrgRole)
			ws.TunnelRespond(gcm, req.RequestID, map[string]any{"paths": userPaths}, write)
		}

	case "paths.set":
		if inner.Paths == nil {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing paths"}, write)
			return
		}
		wingCfgMu.Lock()
		if wingpolicy.IsMemberFiltered(wingpolicy.RequestAgainstWingConfig(req, authenticatedOrgRole, liveWingCfg)) {
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "admin required"}, write)
			return
		}
		oldPaths, oldRoot := liveWingCfg.Paths, liveWingCfg.Root
		liveWingCfg.Paths = wingpolicy.ClonePathList(config.PathList(inner.Paths))
		liveWingCfg.Root = ""
		if saveErr := config.SaveWingConfig(cfg.Dir, liveWingCfg); saveErr != nil {
			// Roll back so a request reported as failed does not keep steering
			// live authorization until restart.
			liveWingCfg.Paths, liveWingCfg.Root = oldPaths, oldRoot
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "persist wing.yaml: " + saveErr.Error()}, write)
			return
		}
		paths := wingpolicy.ClonePathList(liveWingCfg.Paths)
		wingCfgMu.Unlock()
		log.Printf("paths.set: %d entries by %s", len(paths), req.SenderUserID)
		go killSessionsViolatingACLs(cfg, paths, home)
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "paths.add_member":
		if inner.Path == "" || inner.Email == "" {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing path or email"}, write)
			return
		}
		wingCfgMu.Lock()
		if wingpolicy.IsMemberFiltered(wingpolicy.RequestAgainstWingConfig(req, authenticatedOrgRole, liveWingCfg)) {
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "admin required"}, write)
			return
		}
		found := false
		emailLower := strings.ToLower(inner.Email)
		oldPaths := wingpolicy.ClonePathList(liveWingCfg.Paths)
		for i, e := range liveWingCfg.Paths {
			if e.Path == inner.Path {
				// Check duplicate
				dup := false
				for _, m := range e.Members {
					if strings.ToLower(m) == emailLower {
						dup = true
						break
					}
				}
				if !dup {
					liveWingCfg.Paths[i].Members = append(liveWingCfg.Paths[i].Members, inner.Email)
				}
				found = true
				break
			}
		}
		if !found {
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "path not found"}, write)
			return
		}
		if saveErr := config.SaveWingConfig(cfg.Dir, liveWingCfg); saveErr != nil {
			liveWingCfg.Paths = oldPaths
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "persist wing.yaml: " + saveErr.Error()}, write)
			return
		}
		wingCfgMu.Unlock()
		log.Printf("paths.add_member: %s to %s by %s", inner.Email, inner.Path, req.SenderUserID)
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	case "paths.remove_member":
		if inner.Path == "" || inner.Email == "" {
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "missing path or email"}, write)
			return
		}
		wingCfgMu.Lock()
		if wingpolicy.IsMemberFiltered(wingpolicy.RequestAgainstWingConfig(req, authenticatedOrgRole, liveWingCfg)) {
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "admin required"}, write)
			return
		}
		found := false
		emailLower := strings.ToLower(inner.Email)
		oldPaths := wingpolicy.ClonePathList(liveWingCfg.Paths)
		for i, e := range liveWingCfg.Paths {
			if e.Path == inner.Path {
				// An empty member list means a legacy open entry visible to every
				// member, so removing the last member would silently make the
				// path public instead of revoking access. Fail closed.
				if len(e.Members) == 1 && strings.ToLower(e.Members[0]) == emailLower {
					wingCfgMu.Unlock()
					ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "cannot remove the last member — an empty list opens the path to everyone; remove the path entry instead"}, write)
					return
				}
				for j, m := range e.Members {
					if strings.ToLower(m) == emailLower {
						liveWingCfg.Paths[i].Members = append(e.Members[:j], e.Members[j+1:]...)
						found = true
						break
					}
				}
				break
			}
		}
		if !found {
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "path or member not found"}, write)
			return
		}
		if saveErr := config.SaveWingConfig(cfg.Dir, liveWingCfg); saveErr != nil {
			liveWingCfg.Paths = oldPaths
			wingCfgMu.Unlock()
			ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "persist wing.yaml: " + saveErr.Error()}, write)
			return
		}
		paths := wingpolicy.ClonePathList(liveWingCfg.Paths)
		wingCfgMu.Unlock()
		log.Printf("paths.remove_member: %s from %s by %s", inner.Email, inner.Path, req.SenderUserID)
		go killSessionsViolatingACLs(cfg, paths, home)
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"ok": "true"}, write)

	default:
		ws.TunnelRespond(gcm, req.RequestID, map[string]string{"error": "unknown type: " + inner.Type}, write)
	}
}

func renameTunnelSession(cfg *config.Config, req ws.TunnelRequest, sessionID, name string, sessions []ws.SessionInfo, userPaths []string) error {
	if err := eggclient.ValidateSessionName(name); err != nil {
		return err
	}
	if _, err := sessionfiles.ResolveOwnedActiveSession(req, sessionID, sessions, userPaths); err != nil {
		return err
	}
	lock, err := eggclient.AcquireSessionNameLock(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := eggclient.EnsureSessionNameAvailable(cfg, name, sessionID); err != nil {
		return err
	}
	return eggclient.WriteSessionName(filepath.Join(cfg.Dir, "eggs", sessionID), name)
}

// collectSessionsHistory returns all dead egg sessions from disk newest first.
// Authorization must be applied before pagination so member pages and totals do
// not depend on the positions of sessions they cannot see.
func collectSessionsHistory(cfg *config.Config) []pastSessionInfo {
	eggsDir := filepath.Join(cfg.Dir, "eggs")
	entries, err := os.ReadDir(eggsDir)
	if err != nil {
		return nil
	}

	var dead []pastSessionInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		dir := filepath.Join(eggsDir, sessionID)

		// Check if process is alive -- skip alive sessions
		pidData, err := os.ReadFile(filepath.Join(dir, "egg.pid"))
		if err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if procinfo.OwnedProcessIsAlive(pid) {
				continue
			}
		}

		agentName, cwd := eggclient.ReadEggMeta(dir)
		hasAudit := false
		if _, err := os.Stat(filepath.Join(dir, "audit.pty.gz")); err == nil {
			hasAudit = true
		}
		hasChat := false
		if _, err := os.Stat(filepath.Join(dir, "chat.jsonl.gz")); err == nil {
			hasChat = true
		}
		if agentName == "" && !hasAudit && !hasChat {
			continue
		}
		if agentName == "" {
			agentName = "unknown"
		}

		info := pastSessionInfo{
			ConversationLink: eggclient.SessionConversationLink(cfg, sessionID),
			SessionID:        sessionID,
			Name:             eggclient.ReadSessionName(dir),
			Agent:            agentName,
			CWD:              cwd,
			Audit:            hasAudit,
			Chat:             hasChat,
			UserID:           eggclient.ReadEggOwner(dir),
		}
		meta := eggclient.ReadEggMetaValues(dir)
		if startedAt, parseErr := strconv.ParseInt(meta["started_at"], 10, 64); parseErr == nil && startedAt > 0 {
			info.StartedAt = startedAt
		} else if stat, statErr := os.Stat(dir); statErr == nil {
			info.StartedAt = stat.ModTime().Unix()
		}
		info.Resumable, info.ResumeUnavailableReason = eggclient.SessionResumeStatus(dir, agentName, cwd)
		if classified := eggclient.ClassifyEgg(cfg, sessionID); classified.Class == eggclient.RecoveryEligible {
			info.Recoverable, info.Status = true, "exited"
		}
		dead = append(dead, info)
	}

	sort.Slice(dead, func(i, j int) bool {
		return dead[i].StartedAt > dead[j].StartedAt
	})
	return dead
}

func filterSessionsHistoryForRequest(req ws.TunnelRequest, sessions []pastSessionInfo, userPaths []string) []pastSessionInfo {
	if !wingpolicy.IsMemberFiltered(req) {
		return sessions
	}
	filtered := make([]pastSessionInfo, 0, len(sessions))
	for _, session := range sessions {
		if wingpolicy.CanSeeSession(req, session.UserID) && wingpolicy.CanAccessSessionPath(req, session.CWD, userPaths) {
			filtered = append(filtered, session)
		}
	}
	return filtered
}

const (
	defaultSessionsHistoryLimit = 20
	maxSessionsHistoryLimit     = 200
)

func paginateSessionsHistory(sessions []pastSessionInfo, offset, limit int) ([]pastSessionInfo, int) {
	total := len(sessions)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	if limit <= 0 {
		limit = defaultSessionsHistoryLimit
	}
	if limit > maxSessionsHistoryLimit {
		limit = maxSessionsHistoryLimit
	}
	remaining := total - offset
	if limit > remaining {
		limit = remaining
	}
	return sessions[offset : offset+limit], total
}

const (
	auditStreamChunkBytes = 32 << 10
	maxAuditFrameBytes    = 64 << 10
	maxAuditMetadataBytes = 64 << 10
)

func streamAuditFile(file io.Reader, base64Encode bool, emit func([]byte) error) error {
	buffer := make([]byte, auditStreamChunkBytes)
	for {
		count, readErr := file.Read(buffer)
		if count > 0 {
			value := string(buffer[:count])
			if base64Encode {
				value = base64.StdEncoding.EncodeToString(buffer[:count])
			}
			chunk, marshalErr := json.Marshal(map[string]string{"data": value})
			if marshalErr != nil {
				return marshalErr
			}
			if err := emit(chunk); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func auditDimensions(dir string) (int, int) {
	cols, rows := 120, 40
	file, err := os.Open(filepath.Join(dir, "egg.meta"))
	if err != nil {
		return cols, rows
	}
	defer cmdutil.CloseWithLog("audit metadata", file)
	meta, err := io.ReadAll(io.LimitReader(file, maxAuditMetadataBytes+1))
	if err != nil || len(meta) > maxAuditMetadataBytes {
		return cols, rows
	}
	for _, line := range strings.Split(string(meta), "\n") {
		if strings.HasPrefix(line, "cols=") {
			if value, parseErr := strconv.Atoi(strings.TrimPrefix(line, "cols=")); parseErr == nil && value > 0 && value <= 65535 {
				cols = value
			}
		}
		if strings.HasPrefix(line, "rows=") {
			if value, parseErr := strconv.Atoi(strings.TrimPrefix(line, "rows=")); parseErr == nil && value > 0 && value <= 65535 {
				rows = value
			}
		}
	}
	return cols, rows
}

func incompleteAuditRead(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func auditStreamErrorPayload(message string) []byte {
	payload, err := json.Marshal(map[string]string{"error": message})
	if err != nil {
		return []byte(`{"error":"audit stream failed"}`)
	}
	return payload
}

// streamPTYAudit converts a decompressed V1/V2 recording incrementally. It
// never retains the whole recording or trusts a frame length before enforcing
// the per-frame cap.
func streamPTYAudit(reader io.Reader, fallbackCols, fallbackRows int, emit func([]byte) error) error {
	buffered := bufio.NewReaderSize(reader, auditStreamChunkBytes)
	isV2 := false
	if header, err := buffered.Peek(4); err == nil && bytes.Equal(header, []byte("WTA2")) {
		if _, err := buffered.Discard(4); err != nil {
			return err
		}
		isV2 = true
		cols, err := binary.ReadUvarint(buffered)
		if err != nil {
			return fmt.Errorf("read audit columns: %w", err)
		}
		rows, err := binary.ReadUvarint(buffered)
		if err != nil {
			return fmt.Errorf("read audit rows: %w", err)
		}
		if cols == 0 || cols > 65535 || rows == 0 || rows > 65535 {
			return fmt.Errorf("invalid audit dimensions %dx%d", cols, rows)
		}
		fallbackCols, fallbackRows = int(cols), int(rows)
	}
	header := []byte(fmt.Sprintf(`{"version":2,"width":%d,"height":%d}`, fallbackCols, fallbackRows))
	if err := emit(header); err != nil {
		return err
	}

	var cumulativeMS uint64
	for {
		deltaMS, err := binary.ReadUvarint(buffered)
		if incompleteAuditRead(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read audit timestamp: %w", err)
		}
		frameType := uint64(0)
		if isV2 {
			frameType, err = binary.ReadUvarint(buffered)
			if incompleteAuditRead(err) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("read audit frame type: %w", err)
			}
		}
		dataLen, err := binary.ReadUvarint(buffered)
		if incompleteAuditRead(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read audit frame length: %w", err)
		}
		if dataLen > maxAuditFrameBytes {
			return fmt.Errorf("audit frame is %d bytes; maximum is %d", dataLen, maxAuditFrameBytes)
		}
		frame := make([]byte, int(dataLen))
		if _, err := io.ReadFull(buffered, frame); incompleteAuditRead(err) {
			return nil
		} else if err != nil {
			return fmt.Errorf("read audit frame: %w", err)
		}
		if ^uint64(0)-cumulativeMS < deltaMS {
			return errors.New("audit timestamp overflow")
		}
		cumulativeMS += deltaMS

		var line []byte
		if frameType == 1 {
			resize := bytes.NewReader(frame)
			cols, colErr := binary.ReadUvarint(resize)
			rows, rowErr := binary.ReadUvarint(resize)
			if colErr != nil || rowErr != nil || cols == 0 || cols > 65535 || rows == 0 || rows > 65535 {
				continue
			}
			line = []byte(fmt.Sprintf("[%.3f,\"r\",\"%dx%d\"]", float64(cumulativeMS)/1000, cols, rows))
		} else {
			encoded := base64.StdEncoding.EncodeToString(frame)
			line = []byte(fmt.Sprintf("[%.3f,\"o\",\"%s\"]", float64(cumulativeMS)/1000, encoded))
		}
		if err := emit(line); err != nil {
			return err
		}
	}
}

// streamAuditData reads audit data from disk and streams encrypted chunks via tunnel.stream.
func streamAuditData(cfg *config.Config, sessionID, kind string, gcm cipher.AEAD, requestID string, write ws.PTYWriteFunc) {
	if !ws.ValidSessionID(sessionID) {
		ws.TunnelRespond(gcm, requestID, map[string]string{"error": "invalid session ID"}, write)
		return
	}
	dir := filepath.Join(cfg.Dir, "eggs", sessionID)

	var filePath string
	switch kind {
	case "keylog":
		filePath = filepath.Join(dir, "audit.log")
	case "chat":
		filePath = filepath.Join(dir, "chat.jsonl.gz")
	default:
		filePath = filepath.Join(dir, "audit.pty.gz")
	}

	file, err := os.Open(filePath)
	if err != nil {
		ws.TunnelRespond(gcm, requestID, map[string]string{"error": "file not found: " + kind}, write)
		return
	}
	defer cmdutil.CloseWithLog("audit stream", file)
	emit := func(chunk []byte) error {
		return ws.TunnelStreamChunk(gcm, requestID, chunk, false, write)
	}

	if kind == "chat" {
		if err := streamAuditFile(file, true, emit); err != nil {
			_ = ws.TunnelStreamChunk(gcm, requestID, auditStreamErrorPayload("read chat audit: "+err.Error()), true, write)
			return
		}
		_ = ws.TunnelStreamChunk(gcm, requestID, []byte(`{"done":true}`), true, write)
		return
	}

	if kind != "pty" {
		if err := streamAuditFile(file, false, emit); err != nil {
			_ = ws.TunnelStreamChunk(gcm, requestID, auditStreamErrorPayload("read keylog audit: "+err.Error()), true, write)
			return
		}
		_ = ws.TunnelStreamChunk(gcm, requestID, []byte(`{"done":true}`), true, write)
		return
	}

	// Decompress gzip and stream as asciinema v2 NDJSON. The parser tolerates
	// an incomplete trailing frame from a live writer.
	gr, gzErr := gzip.NewReader(file)
	if gzErr != nil {
		ws.TunnelRespond(gcm, requestID, map[string]string{"error": "decompress: " + gzErr.Error()}, write)
		return
	}
	cols, rows := auditDimensions(dir)
	streamErr := streamPTYAudit(gr, cols, rows, emit)
	if closeErr := gr.Close(); closeErr != nil && streamErr == nil {
		streamErr = closeErr
	}
	if streamErr != nil {
		_ = ws.TunnelStreamChunk(gcm, requestID, auditStreamErrorPayload("read PTY audit: "+streamErr.Error()), true, write)
		return
	}
	_ = ws.TunnelStreamChunk(gcm, requestID, []byte(`{"done":true}`), true, write)
}
