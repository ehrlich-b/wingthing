package ws

import (
	"context"
	"crypto/ecdh"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/fsutil"
)

const (
	maxWingRosterBytes        = 1 << 20
	maxTunnelInnerBytes       = 1 << 20
	maxTunnelWireMessageBytes = 2 << 20
)

// TunnelClient sends encrypted tunnel requests to a wing via the relay.
type TunnelClient struct {
	RelayURL       string           // HTTP base URL (e.g. "https://wingthing.ai")
	DeviceToken    string           // Bearer token for relay auth
	PrivKey        *ecdh.PrivateKey // native client's identity key
	KnownWingsPath string           // optional TOFU identity pin store
	HTTPClient     *http.Client     // optional roster client; defaults to a bounded client
	pinMu          sync.Mutex
}

// WingInfo holds the minimal info needed to connect to a wing.
type WingInfo struct {
	WingID         string `json:"wing_id"`
	PublicKey      string `json:"public_key"`
	LatestVersion  string `json:"latest_version,omitempty"`
	OrgID          string `json:"org_id,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	Owner          string `json:"owner,omitempty"`
	RemoteNode     string `json:"remote_node,omitempty"`
	Locked         bool   `json:"locked,omitempty"`
	AllowedCount   int    `json:"allowed_count,omitempty"`
	PurposeBinding bool   `json:"purpose_binding,omitempty"`
	DirectMCP      bool   `json:"direct_mcp,omitempty"`
	HostedRelay    string `json:"hosted_relay,omitempty"`
}

// ListWings returns the online wings the authenticated relay account may use.
// The relay roster intentionally contains routing identity only; callers use an
// encrypted wing.info request when they need host or capability details.
func (tc *TunnelClient) ListWings(ctx context.Context) ([]WingInfo, error) {
	url := strings.TrimRight(tc.RelayURL, "/") + "/api/app/wings"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tc.DeviceToken)

	client := tc.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wings API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("wings API: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWingRosterBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read wings: %w", err)
	}
	if len(body) > maxWingRosterBytes {
		return nil, fmt.Errorf("decode wings: response exceeds %d bytes", maxWingRosterBytes)
	}
	var wings []WingInfo
	if err := json.Unmarshal(body, &wings); err != nil {
		return nil, fmt.Errorf("decode wings: %w", err)
	}
	return wings, nil
}

// DiscoverWing finds and pins a wing's public key from the relay API.
func (tc *TunnelClient) DiscoverWing(ctx context.Context, wingID string) (*WingInfo, error) {
	wings, err := tc.ListWings(ctx)
	if err != nil {
		return nil, err
	}

	for _, w := range wings {
		if w.WingID == wingID {
			if err := tc.VerifyWingIdentity(w); err != nil {
				return nil, err
			}
			return &w, nil
		}
	}
	return nil, fmt.Errorf("wing %s not found", wingID)
}

// VerifyWingIdentity applies the native client's TOFU policy to a relay roster
// entry. First-use pin updates are serialized so concurrent wing connections do
// not lose one another's entries.
func (tc *TunnelClient) VerifyWingIdentity(wing WingInfo) error {
	if tc.KnownWingsPath == "" {
		return nil
	}
	tc.pinMu.Lock()
	defer tc.pinMu.Unlock()
	publicKey, err := base64.StdEncoding.DecodeString(wing.PublicKey)
	if err != nil || len(publicKey) != 32 {
		return fmt.Errorf("wing %s returned an invalid X25519 identity key", wing.WingID)
	}
	directory := filepath.Dir(tc.KnownWingsPath)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create known wing identity directory: %w", err)
	}
	// pinMu covers goroutines sharing this client. The advisory file lock also
	// serializes independent Claude/Codex connector processes using the same WT
	// profile, so each update rereads the latest complete pin set before commit.
	lock, err := os.OpenFile(tc.KnownWingsPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open known wing identity lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := lock.Chmod(0600); err != nil {
		return fmt.Errorf("protect known wing identity lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock known wing identities: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	pins := map[string]string{}
	data, err := os.ReadFile(tc.KnownWingsPath)
	if err == nil {
		if err := json.Unmarshal(data, &pins); err != nil {
			return fmt.Errorf("parse known wing identities: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read known wing identities: %w", err)
	}
	if pinned := pins[wing.WingID]; pinned != "" {
		if pinned != wing.PublicKey {
			return fmt.Errorf("wing %s identity changed; verify the wing and remove its entry from %s before trusting the new key", wing.WingID, tc.KnownWingsPath)
		}
		return nil
	}
	pins[wing.WingID] = wing.PublicKey
	encoded, err := json.MarshalIndent(pins, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".known-wings-*")
	if err != nil {
		return fmt.Errorf("create temporary known wing identities: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect temporary known wing identities: %w", err)
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write known wing identities: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync known wing identities: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close known wing identities: %w", err)
	}
	if err := os.Rename(temporaryPath, tc.KnownWingsPath); err != nil {
		return fmt.Errorf("commit known wing identities: %w", err)
	}
	if err := fsutil.SyncDirectory(directory); err != nil {
		return fmt.Errorf("persist known wing identities: %w", err)
	}
	return nil
}

// Stream opens a WebSocket to the relay, sends an encrypted tunnel request,
// and collects streaming response chunks. The onChunk callback receives decrypted
// JSON payloads. The stream ends when a chunk with done:true is received.
func (tc *TunnelClient) Stream(ctx context.Context, wingID, wingPubKey string, inner any, onChunk func([]byte) error) error {
	// Derive shared tunnel key
	gcm, err := auth.DeriveSharedKey(tc.PrivKey, wingPubKey, "wt-tunnel")
	if err != nil {
		return fmt.Errorf("derive key: %w", err)
	}
	// Validate and bound caller-controlled input before opening a coordinator
	// connection. The encrypted/base64 wire envelope is larger and has its own
	// read-side cap below.
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return fmt.Errorf("marshal inner: %w", err)
	}
	if len(innerJSON) > maxTunnelInnerBytes {
		return fmt.Errorf("marshal inner: request exceeds %d bytes", maxTunnelInnerBytes)
	}

	// Build relay WebSocket URL
	wsURL := tc.relayWSURL() + "/ws/relay?" + url.Values{"wing_id": []string{wingID}}.Encode()
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tc.DeviceToken)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "done") }()
	conn.SetReadLimit(maxTunnelWireMessageBytes)

	// Encrypt inner message
	payload, err := auth.Encrypt(gcm, innerJSON)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	// Send tunnel.req
	senderPub := base64.StdEncoding.EncodeToString(tc.PrivKey.PublicKey().Bytes())
	requestID := generateRequestID()
	var innerEnvelope Envelope
	if err := json.Unmarshal(innerJSON, &innerEnvelope); err != nil {
		return fmt.Errorf("decode inner envelope: %w", err)
	}
	tunnelReq := TunnelRequest{
		Type:      TypeTunnelRequest,
		WingID:    wingID,
		RequestID: requestID,
		Purpose:   TunnelPurposeForInnerType(innerEnvelope.Type),
		SenderPub: senderPub,
		Payload:   payload,
	}
	reqJSON, err := json.Marshal(tunnelReq)
	if err != nil {
		return fmt.Errorf("marshal tunnel request: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, reqJSON); err != nil {
		return fmt.Errorf("send tunnel.req: %w", err)
	}

	// Read responses
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var msg struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Message   string `json:"message"`
			Payload   string `json:"payload"`
			Done      bool   `json:"done"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.RequestID != requestID {
			continue
		}
		if msg.Type == TypeError {
			return fmt.Errorf("relay: %s", msg.Message)
		}

		decrypted, err := auth.Decrypt(gcm, msg.Payload)
		if err != nil {
			return fmt.Errorf("decrypt response: %w", err)
		}

		switch msg.Type {
		case TypeTunnelResponse:
			// Single response (might be an error)
			var result map[string]any
			if err := json.Unmarshal(decrypted, &result); err != nil {
				return fmt.Errorf("decode tunnel response: %w", err)
			}
			if errMsg, ok := result["error"].(string); ok {
				return fmt.Errorf("wing error: %s", errMsg)
			}
			if err := onChunk(decrypted); err != nil {
				return err
			}
			return nil

		case TypeTunnelStream:
			if err := onChunk(decrypted); err != nil {
				return err
			}
			if msg.Done {
				return nil
			}
		}
	}
}

func (tc *TunnelClient) relayWSURL() string {
	url := strings.TrimRight(tc.RelayURL, "/")
	url = strings.Replace(url, "https://", "wss://", 1)
	url = strings.Replace(url, "http://", "ws://", 1)
	if !strings.HasPrefix(url, "ws://") && !strings.HasPrefix(url, "wss://") {
		url = "wss://" + url
	}
	return url
}

func generateRequestID() string {
	id, err := generateRequestIDFrom(crand.Reader)
	if err != nil {
		panic(fmt.Sprintf("crypto/rand failed while generating a tunnel request ID: %v", err))
	}
	return id
}

func generateRequestIDFrom(reader io.Reader) (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(reader, b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}
