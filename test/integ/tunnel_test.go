//go:build e2e

package integ

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestTunnelEncryptionRoundTrip(t *testing.T) {
	_, ts, store := testRelayAndWS(t)
	token, _ := createTestUser(t, store, "tunnel1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Connect wing WebSocket and register
	wingConn, _, err := websocket.Dial(ctx, wsURL(ts)+"/ws/wing?token="+token, nil)
	if err != nil {
		t.Fatalf("dial wing ws: %v", err)
	}
	defer wingConn.CloseNow()

	// Generate wing X25519 key pair
	wingPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate wing key: %v", err)
	}
	wingPubB64 := base64.StdEncoding.EncodeToString(wingPriv.PublicKey().Bytes())

	reg := ws.WingRegister{
		Type:      ws.TypeWingRegister,
		WingID:    "tunnel-wing-1",
		Hostname:  "testhost",
		Agents:    []string{"claude"},
		PublicKey: wingPubB64,
	}
	if err := wsjson.Write(ctx, wingConn, reg); err != nil {
		t.Fatalf("write wing.register: %v", err)
	}

	// Read registered ack
	var ack ws.RegisteredMsg
	if err := wsjson.Read(ctx, wingConn, &ack); err != nil {
		t.Fatalf("read registered ack: %v", err)
	}

	// Connect browser WebSocket (PTY path handles tunnel.req)
	browserConn, _, err := websocket.Dial(ctx, wsURL(ts)+"/ws/pty?token="+token+"&wing_id=tunnel-wing-1", nil)
	if err != nil {
		t.Fatalf("dial browser ws: %v", err)
	}
	defer browserConn.CloseNow()

	// Generate browser X25519 key pair
	browserPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate browser key: %v", err)
	}
	browserPubB64 := base64.StdEncoding.EncodeToString(browserPriv.PublicKey().Bytes())

	// Browser encrypts a tunnel request using wing's public key
	plaintext := []byte(`{"type":"wing.info"}`)
	browserAEAD, err := auth.DeriveSharedKey(browserPriv, wingPubB64, "wt-tunnel")
	if err != nil {
		t.Fatalf("browser derive shared key: %v", err)
	}
	encPayload, err := auth.Encrypt(browserAEAD, plaintext)
	if err != nil {
		t.Fatalf("browser encrypt: %v", err)
	}

	requestID := uuid.New().String()[:8]
	tunnelReq := ws.TunnelRequest{
		Type:      ws.TypeTunnelRequest,
		WingID:    "tunnel-wing-1",
		RequestID: requestID,
		SenderPub: browserPubB64,
		Payload:   encPayload,
	}
	if err := wsjson.Write(ctx, browserConn, tunnelReq); err != nil {
		t.Fatalf("write tunnel.req: %v", err)
	}

	// Wing reads the tunnel request from its WebSocket
	var wingReceived ws.TunnelRequest
	if err := wsjson.Read(ctx, wingConn, &wingReceived); err != nil {
		t.Fatalf("wing read tunnel.req: %v", err)
	}
	if wingReceived.Type != ws.TypeTunnelRequest {
		t.Fatalf("expected tunnel.req, got %s", wingReceived.Type)
	}
	if wingReceived.RequestID != requestID {
		t.Fatalf("request ID mismatch: want %s, got %s", requestID, wingReceived.RequestID)
	}

	// Wing decrypts the payload
	wingAEAD, err := auth.DeriveSharedKey(wingPriv, wingReceived.SenderPub, "wt-tunnel")
	if err != nil {
		t.Fatalf("wing derive shared key: %v", err)
	}
	decrypted, err := auth.Decrypt(wingAEAD, wingReceived.Payload)
	if err != nil {
		t.Fatalf("wing decrypt: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("decrypted payload mismatch: want %q, got %q", plaintext, decrypted)
	}

	// Wing encrypts a response using browser's public key
	responsePlain := []byte(`{"hostname":"testhost","platform":"darwin"}`)
	// Wing derives key from browser's public key
	wingRespAEAD, err := auth.DeriveSharedKey(wingPriv, browserPubB64, "wt-tunnel")
	if err != nil {
		t.Fatalf("wing derive response key: %v", err)
	}
	encResponse, err := auth.Encrypt(wingRespAEAD, responsePlain)
	if err != nil {
		t.Fatalf("wing encrypt response: %v", err)
	}

	tunnelResp := ws.TunnelResponse{
		Type:      ws.TypeTunnelResponse,
		RequestID: requestID,
		Payload:   encResponse,
	}
	if err := wsjson.Write(ctx, wingConn, tunnelResp); err != nil {
		t.Fatalf("wing write tunnel.res: %v", err)
	}

	// Browser reads the tunnel response
	var browserReceived ws.TunnelResponse
	if err := wsjson.Read(ctx, browserConn, &browserReceived); err != nil {
		t.Fatalf("browser read tunnel.res: %v", err)
	}
	if browserReceived.Type != ws.TypeTunnelResponse {
		t.Fatalf("expected tunnel.res, got %s", browserReceived.Type)
	}

	// Browser decrypts the response
	browserRespAEAD, err := auth.DeriveSharedKey(browserPriv, wingPubB64, "wt-tunnel")
	if err != nil {
		t.Fatalf("browser derive response key: %v", err)
	}
	decResponse, err := auth.Decrypt(browserRespAEAD, browserReceived.Payload)
	if err != nil {
		t.Fatalf("browser decrypt response: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(decResponse, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result["hostname"] != "testhost" {
		t.Errorf("expected hostname testhost, got %s", result["hostname"])
	}
	if result["platform"] != "darwin" {
		t.Errorf("expected platform darwin, got %s", result["platform"])
	}
}
