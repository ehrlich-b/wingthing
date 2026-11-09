//go:build integration

package phoneweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

// Coordinator gate: a real HTTPS browser session and full-duplex WebSocket,
// including termination of an already upgraded socket after sign-out.
func TestPhoneGatewayLoopbackWebSocket(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			kind, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if c.Write(r.Context(), kind, data) != nil {
				return
			}
		}
	}))
	defer backend.Close()
	var gateway *Gateway
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gateway.ServeHTTP(w, r) }))
	defer front.Close()
	path := filepath.Join(testsocket.Dir(t), "phone.key")
	if err := os.WriteFile(path, []byte(testKey), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	gateway, err = New(front.URL, backend.URL, path)
	if err != nil {
		t.Fatal(err)
	}
	client := front.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	login, _ := http.NewRequest("POST", front.URL+"/phone/login", strings.NewReader(url.Values{"key": {testKey}}.Encode()))
	login.Header.Set("Origin", front.URL)
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 303 || len(response.Cookies()) != 1 {
		t.Fatalf("login: %d", response.StatusCode)
	}
	cookie := response.Cookies()[0]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(front.URL, "https")+"/ws/relay", &websocket.DialOptions{
		HTTPClient: client, HTTPHeader: http.Header{"Origin": {front.URL}, "Cookie": {cookie.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte("existing wing protocol")); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil || string(data) != "existing wing protocol" {
		t.Fatalf("echo %q: %v", data, err)
	}
	logout, _ := http.NewRequest("POST", front.URL+"/phone/logout", nil)
	logout.Header.Set("Origin", front.URL)
	logout.AddCookie(cookie)
	response, err = client.Do(logout)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 303 {
		t.Fatalf("logout %d", response.StatusCode)
	}
	exit, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	_, _, err = conn.Read(exit)
	if err == nil || exit.Err() != nil {
		t.Fatalf("signed-out WebSocket did not close promptly: %v", err)
	}
}
