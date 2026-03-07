package ws

import (
	"context"
	"crypto/ecdh"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/auth"
)

// TunnelClient sends encrypted tunnel requests to a wing via the relay.
type TunnelClient struct {
	RelayURL    string           // HTTP base URL (e.g. "https://wingthing.ai")
	DeviceToken string           // Bearer token for relay auth
	PrivKey     *ecdh.PrivateKey // wing's own identity key
}

// WingInfo holds the minimal info needed to connect to a wing.
type WingInfo struct {
	WingID    string `json:"wing_id"`
	PublicKey string `json:"public_key"`
}

// DiscoverWing finds a wing's public key from the relay API.
func (tc *TunnelClient) DiscoverWing(ctx context.Context, wingID string) (*WingInfo, error) {
	url := strings.TrimRight(tc.RelayURL, "/") + "/api/app/wings"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tc.DeviceToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wings API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("wings API: status %d", resp.StatusCode)
	}

	var wings []WingInfo
	if err := json.NewDecoder(resp.Body).Decode(&wings); err != nil {
		return nil, fmt.Errorf("decode wings: %w", err)
	}

	for _, w := range wings {
		if w.WingID == wingID {
			return &w, nil
		}
	}
	return nil, fmt.Errorf("wing %s not found", wingID)
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

	// Build relay WebSocket URL
	wsURL := tc.relayWSURL() + "/ws/relay?wing_id=" + wingID
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+tc.DeviceToken)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Encrypt inner message
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return fmt.Errorf("marshal inner: %w", err)
	}
	payload, err := auth.Encrypt(gcm, innerJSON)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	// Send tunnel.req
	senderPub := base64.StdEncoding.EncodeToString(tc.PrivKey.PublicKey().Bytes())
	requestID := generateRequestID()
	tunnelReq := TunnelRequest{
		Type:      TypeTunnelRequest,
		WingID:    wingID,
		RequestID: requestID,
		SenderPub: senderPub,
		Payload:   payload,
	}
	reqJSON, _ := json.Marshal(tunnelReq)
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
			Payload   string `json:"payload"`
			Done      bool   `json:"done"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.RequestID != requestID {
			continue
		}

		decrypted, err := auth.Decrypt(gcm, msg.Payload)
		if err != nil {
			return fmt.Errorf("decrypt response: %w", err)
		}

		switch msg.Type {
		case TypeTunnelResponse:
			// Single response (might be an error)
			var result map[string]any
			json.Unmarshal(decrypted, &result)
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
	b := make([]byte, 16)
	crand.Read(b)
	return fmt.Sprintf("%x", b)
}
