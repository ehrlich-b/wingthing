package ws

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/auth"
)

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
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Logf("ws accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		ctx := r.Context()

		// Read tunnel.req from client
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Logf("ws read: %v", err)
			return
		}

		var req TunnelRequest
		json.Unmarshal(data, &req)

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
			conn.Write(ctx, websocket.MessageText, respJSON)
		}
	}))
	defer srv.Close()

	tc := &TunnelClient{
		RelayURL:    srv.URL,
		DeviceToken: "test-token",
		PrivKey:     clientPriv,
	}

	var received []string
	err := tc.Stream(context.Background(), "test-wing", wingPubB64,
		map[string]string{"type": "test"},
		func(chunk []byte) error {
			var c map[string]string
			json.Unmarshal(chunk, &c)
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
