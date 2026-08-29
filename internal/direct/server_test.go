package direct

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func signedHandoffToken(t *testing.T, key *ecdsa.PrivateKey, claims HandoffClaims) string {
	t.Helper()
	if claims.IssuedAt == nil {
		claims.IssuedAt = jwt.NewNumericDate(time.Now())
	}
	if claims.ExpiresAt == nil {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
	}
	if claims.TokenUse == "" {
		claims.TokenUse = "handoff"
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestDirectServerRejectsCrossOriginWebSocket(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	claims := HandoffClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "alice", IssuedAt: jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
		TokenUse: "handoff",
	}
	token := signedHandoffToken(t, key, claims)
	server := &Server{}
	server.SetRelayPublicKey(&key.PublicKey)
	if err := server.StartAsync("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	wsURL := "ws://" + server.Addr().String() + "/ws/pty"
	_, response, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + token},
			"Origin":        []string{"https://attacker.example"},
		},
	})
	if err == nil {
		t.Fatal("direct server accepted a cross-origin WebSocket")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin response = %#v, err = %v; want 403", response, err)
	}
}

func TestDirectServerInjectsAuthenticatedIdentityIntoStart(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan ws.PTYStart, 1)
	server := &Server{OnPTY: func(_ context.Context, start ws.PTYStart, _ ws.PTYWriteFunc, _ <-chan []byte) {
		started <- start
	}}
	server.SetRelayPublicKey(&key.PublicKey)
	if err := server.StartAsync("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	claims := HandoffClaims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"},
		Email:            "alice@example.com",
		OrgRole:          "member",
	}
	conn, _, err := websocket.Dial(context.Background(), "ws://"+server.Addr().String()+"/ws/pty", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + signedHandoffToken(t, key, claims)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The server may have already closed the upgraded connection while the test
	// is asserting protocol behavior. Cleanup is therefore intentionally
	// best-effort; close semantics are covered independently below.
	t.Cleanup(func() { _ = conn.CloseNow() })
	payload, err := json.Marshal(ws.PTYStart{Type: ws.TypePTYStart, SessionID: "session-1", Agent: "claude", UserID: "forged", Email: "forged@example.com", OrgRole: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(context.Background(), websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-started:
		if got.UserID != "user-1" || got.Email != "alice@example.com" || got.OrgRole != "member" {
			t.Fatalf("injected identity = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("PTY handler was not called")
	}
}

func TestDirectServerRejectsAttachInsteadOfStartingDuplicate(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan ws.PTYStart, 1)
	server := &Server{OnPTY: func(_ context.Context, start ws.PTYStart, _ ws.PTYWriteFunc, _ <-chan []byte) {
		started <- start
	}}
	server.SetRelayPublicKey(&key.PublicKey)
	if err := server.StartAsync("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	claims := HandoffClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"}}
	conn, _, err := websocket.Dial(context.Background(), "ws://"+server.Addr().String()+"/ws/pty", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + signedHandoffToken(t, key, claims)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	payload, err := json.Marshal(ws.PTYAttach{Type: ws.TypePTYAttach, SessionID: "session-1", Spectate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(context.Background(), websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
	_, _, readErr := conn.Read(context.Background())
	if websocket.CloseStatus(readErr) != websocket.StatusUnsupportedData {
		t.Fatalf("attach close status = %v, err = %v; want %v", websocket.CloseStatus(readErr), readErr, websocket.StatusUnsupportedData)
	}
	select {
	case got := <-started:
		t.Fatalf("attach launched duplicate PTY: %#v", got)
	default:
	}
}

func TestValidateHandoffJWTRequiresDedicatedTokenUse(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issue := func(tokenUse string) string {
		t.Helper()
		claims := HandoffClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject: "alice", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			},
			TokenUse: tokenUse,
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}

	if _, err := validateHandoffJWT(&key.PublicKey, issue("handoff")); err != nil {
		t.Fatalf("valid handoff token rejected: %v", err)
	}
	if _, err := validateHandoffJWT(&key.PublicKey, issue("mcp")); err == nil {
		t.Fatal("MCP token use accepted as a direct-mode handoff token")
	}
}

func TestStartAsyncBindsBeforeReturningAndClosesCleanly(t *testing.T) {
	server := &Server{}
	if err := server.StartAsync("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	if server.Addr() == nil {
		t.Fatal("server returned before publishing its listener")
	}
	if err := server.StartAsync("127.0.0.1:0"); err == nil {
		t.Fatal("server accepted a second start")
	}

	response, err := http.Get("http://" + server.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("health response read=%v close=%v", readErr, closeErr)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"mode":"direct"`) {
		t.Fatalf("health response = %d %q", response.StatusCode, body)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
