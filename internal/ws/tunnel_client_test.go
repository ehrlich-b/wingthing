package ws

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/auth"
)

func TestTunnelClientPinsWingIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_wings.json")
	tc := &TunnelClient{KnownWingsPath: path}
	keyA := base64.StdEncoding.EncodeToString(make([]byte, 32))
	keyBBytes := make([]byte, 32)
	keyBBytes[0] = 1
	keyB := base64.StdEncoding.EncodeToString(keyBBytes)

	if err := tc.VerifyWingIdentity(WingInfo{WingID: "wing-1", PublicKey: keyA}); err != nil {
		t.Fatalf("first use should pin: %v", err)
	}
	if err := tc.VerifyWingIdentity(WingInfo{WingID: "wing-1", PublicKey: keyA}); err != nil {
		t.Fatalf("same key should remain trusted: %v", err)
	}
	if err := tc.VerifyWingIdentity(WingInfo{WingID: "wing-1", PublicKey: keyB}); err == nil {
		t.Fatal("changed identity must be rejected")
	}
}

func TestGenerateRequestIDFromFailsClosedOnShortRandomness(t *testing.T) {
	if id, err := generateRequestIDFrom(strings.NewReader("short")); err == nil || id != "" {
		t.Fatalf("id = %q, err = %v; want empty ID and error", id, err)
	}
	id, err := generateRequestIDFrom(strings.NewReader(strings.Repeat("x", 16)))
	if err != nil || len(id) != 32 {
		t.Fatalf("id = %q, err = %v", id, err)
	}
}

func TestTunnelClientPinsConcurrentWingIdentities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_wings.json")
	var wg sync.WaitGroup
	for index := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each connector process owns a distinct TunnelClient, so this test
			// must not rely on the in-memory mutex of one instance.
			tc := &TunnelClient{KnownWingsPath: path}
			key := make([]byte, 32)
			key[0] = byte(index)
			if err := tc.VerifyWingIdentity(WingInfo{
				WingID: fmt.Sprintf("wing-%d", index), PublicKey: base64.StdEncoding.EncodeToString(key),
			}); err != nil {
				t.Errorf("pin wing %d: %v", index, err)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]string
	if err := json.Unmarshal(data, &pins); err != nil {
		t.Fatal(err)
	}
	if len(pins) != 32 {
		t.Fatalf("concurrent pins = %d, want 32", len(pins))
	}
}

func TestTunnelClientListWings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewEncoder(w).Encode([]WingInfo{{
			WingID: "wing-1", PublicKey: "public-key", Owner: "owner@example.com",
			OrgID: "org-1", RemoteNode: "machine-2", LatestVersion: "v1.2.3", Locked: true, AllowedCount: 2, PurposeBinding: true, DirectMCP: true,
		}}); err != nil {
			t.Errorf("encode wings: %v", err)
		}
	}))
	defer server.Close()

	client := &TunnelClient{RelayURL: server.URL, DeviceToken: "test-token"}
	wings, err := client.ListWings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(wings) != 1 || wings[0].WingID != "wing-1" || wings[0].Owner != "owner@example.com" || wings[0].RemoteNode != "machine-2" || !wings[0].Locked || wings[0].AllowedCount != 2 || !wings[0].PurposeBinding || !wings[0].DirectMCP {
		t.Fatalf("wings = %#v", wings)
	}
}

func TestTunnelClientListWingsUsesConfiguredHTTPClient(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	tc := &TunnelClient{RelayURL: "https://relay.example", HTTPClient: client}
	if _, err := tc.ListWings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("configured HTTP client was not used")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestTunnelClientRejectsOversizedWingRoster(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxWingRosterBytes+1)))
	}))
	defer server.Close()

	tc := &TunnelClient{RelayURL: server.URL, DeviceToken: "test-token"}
	if _, err := tc.ListWings(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized roster error = %v", err)
	}
}

func TestTunnelClientRejectsOversizedInnerRequestBeforeWrite(t *testing.T) {
	wingPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tc := &TunnelClient{PrivKey: clientPriv, RelayURL: "http://127.0.0.1:1"}
	inner := map[string]any{"type": "sessions.list", "padding": strings.Repeat("x", maxTunnelInnerBytes)}
	if err := tc.Stream(context.Background(), "wing", base64.StdEncoding.EncodeToString(wingPriv.PublicKey().Bytes()), inner, func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "request exceeds") {
		t.Fatalf("oversized request error = %v", err)
	}
}

func TestTunnelClientRejectsOversizedCoordinatorFrame(t *testing.T) {
	wingPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, acceptErr := websocket.Accept(w, r, nil)
		if acceptErr != nil {
			t.Errorf("accept: %v", acceptErr)
			return
		}
		defer func() {
			if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil && websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
				t.Errorf("close websocket: %v", err)
			}
		}()
		if _, _, readErr := conn.Read(r.Context()); readErr != nil {
			t.Errorf("read request: %v", readErr)
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(strings.Repeat("x", maxTunnelWireMessageBytes+1)))
	}))
	defer server.Close()

	tc := &TunnelClient{
		RelayURL: strings.Replace(server.URL, "http://", "ws://", 1),
		PrivKey:  clientPriv,
	}
	err = tc.Stream(context.Background(), "wing", base64.StdEncoding.EncodeToString(wingPriv.PublicKey().Bytes()), map[string]any{"type": "wing.info"}, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("oversized coordinator frame was accepted")
	}
}

func TestTunnelClient_DeriveKey(t *testing.T) {
	// Generate two keypairs (client and wing)
	clientPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wingPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	clientPubB64 := base64.StdEncoding.EncodeToString(clientPriv.PublicKey().Bytes())
	wingPubB64 := base64.StdEncoding.EncodeToString(wingPriv.PublicKey().Bytes())

	// Client derives key using wing's public key
	clientGCM, err := auth.DeriveSharedKey(clientPriv, wingPubB64, "wt-tunnel")
	if err != nil {
		t.Fatal(err)
	}

	// Wing derives key using client's public key
	wingGCM, err := auth.DeriveSharedKey(wingPriv, clientPubB64, "wt-tunnel")
	if err != nil {
		t.Fatal(err)
	}

	// Encrypt with client, decrypt with wing
	plaintext := []byte("hello from client")
	encrypted, err := auth.Encrypt(clientGCM, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := auth.Decrypt(wingGCM, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if string(decrypted) != string(plaintext) {
		t.Errorf("round-trip failed: got %q, want %q", decrypted, plaintext)
	}
}

func TestTunnelClient_Stream(t *testing.T) {
	// Generate keypairs
	clientPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	wingPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	wingPubB64 := base64.StdEncoding.EncodeToString(wingPriv.PublicKey().Bytes())

	// Create mock relay server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("wing_id"); got != "wing & one/#" {
			t.Errorf("wing_id query = %q", got)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Logf("ws accept: %v", err)
			return
		}
		defer func() {
			if err := conn.Close(websocket.StatusNormalClosure, "done"); err != nil {
				t.Errorf("close websocket: %v", err)
			}
		}()

		ctx := r.Context()

		// Read tunnel.req from client
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Logf("ws read: %v", err)
			return
		}

		var req TunnelRequest
		if err := json.Unmarshal(data, &req); err != nil {
			t.Logf("decode request: %v", err)
			return
		}
		if req.Purpose != TunnelPurposeControl {
			t.Errorf("tunnel purpose = %q, want %q", req.Purpose, TunnelPurposeControl)
		}

		// Derive shared key as the wing would
		gcm, err := auth.DeriveSharedKey(wingPriv, req.SenderPub, "wt-tunnel")
		if err != nil {
			t.Logf("derive key: %v", err)
			return
		}

		// Decrypt to verify
		_, err = auth.Decrypt(gcm, req.Payload)
		if err != nil {
			t.Logf("decrypt req: %v", err)
			return
		}

		// Send streaming response chunks
		chunks := []string{"chunk1", "chunk2", "chunk3"}
		for i, c := range chunks {
			payload, _ := json.Marshal(map[string]string{"data": c})
			encrypted, _ := auth.Encrypt(gcm, payload)
			done := i == len(chunks)-1
			resp := TunnelStream{
				Type:      TypeTunnelStream,
				RequestID: req.RequestID,
				Payload:   encrypted,
				Done:      done,
			}
			respJSON, _ := json.Marshal(resp)
			if err := conn.Write(ctx, websocket.MessageText, respJSON); err != nil {
				t.Logf("write response: %v", err)
				return
			}
		}
	}))
	defer srv.Close()

	tc := &TunnelClient{
		RelayURL:    srv.URL,
		DeviceToken: "test-token",
		PrivKey:     clientPriv,
	}

	var received []string
	err := tc.Stream(context.Background(), "wing & one/#", wingPubB64,
		map[string]string{"type": "test"},
		func(chunk []byte) error {
			var c map[string]string
			if err := json.Unmarshal(chunk, &c); err != nil {
				return err
			}
			received = append(received, c["data"])
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if len(received) != 3 {
		t.Fatalf("got %d chunks, want 3", len(received))
	}
	for i, want := range []string{"chunk1", "chunk2", "chunk3"} {
		if received[i] != want {
			t.Errorf("chunk %d = %q, want %q", i, received[i], want)
		}
	}
}
