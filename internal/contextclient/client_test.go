package contextclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

type handlerTransport struct{ handler http.Handler }

func (rt handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	rt.handler.ServeHTTP(recorder, r)
	return recorder.Result(), nil
}

func TestClientExchangeCacheAndErrors(t *testing.T) {
	const secret = "memory-only-context-client-secret"
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := New(&config.ContextConfig{URL: "https://context.example", ClientID: "wingthing-stage", SecretFile: path, Scopes: []string{"jira", "happyfox"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	client.now = func() time.Time { return now }
	grants, calls, httpStatus := 0, 0, 0
	rpcError, operationError := false, false
	jtis := map[string]bool{}
	owners := map[string]string{}
	client.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			claims := &jwt.RegisteredClaims{}
			_, err := jwt.ParseWithClaims(r.Form.Get("client_assertion"), claims, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience("https://context.example/oauth/token"), jwt.WithIssuer("wingthing-stage"), jwt.WithTimeFunc(func() time.Time { return now }))
			if err != nil {
				t.Fatal(err)
			}
			if claims.Subject != "wingthing-stage" || claims.IssuedAt == nil || claims.ExpiresAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) != time.Minute || claims.ID == "" || jtis[claims.ID] {
				t.Fatalf("bad claims: %+v", claims)
			}
			jtis[claims.ID] = true
			if r.Form.Get("client_id") != "wingthing-stage" || r.Form.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" || r.Form.Get("subject_token_type") != "https://context.pants.taxi/oauth/token-type/user" || r.Form.Get("scope") != "jira happyfox" {
				t.Fatal("wrong form contract")
			}
			owner := r.Form.Get("subject_token")
			if owner != "owner@slide.tech" && owner != "second@slide.tech" {
				t.Fatalf("bad subject %q", owner)
			}
			grants++
			token := fmt.Sprintf("token-%d", grants)
			owners[token] = owner
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "expires_in": 100})
			return
		}
		calls++
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if owners[token] == "" {
			t.Fatal("missing owner token")
		}
		if httpStatus != 0 {
			w.WriteHeader(httpStatus)
			_, _ = io.WriteString(w, secret+token)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		params := request["params"].(map[string]any)
		if params["name"] != "jira-search" || params["arguments"].(map[string]any)["subject"] != "spoof@slide.tech" {
			t.Fatal("wrong arguments")
		}
		if rpcError {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"`+secret+token+`"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": operationError, "content": []map[string]string{{"type": "text", "text": owners[token]}, {"type": "text", "text": secret + " " + token}}}})
	})}
	args := map[string]any{"subject": "spoof@slide.tech"}
	call := func(owner string) {
		t.Helper()
		output, err := client.Call(context.Background(), owner, "jira-search", args)
		if err != nil || !strings.Contains(output, strings.ToLower(owner)) || strings.Contains(output, secret) || strings.Contains(output, "token-") {
			t.Fatalf("output=%q err=%v", output, err)
		}
	}
	call("Owner@slide.tech")
	call("Owner@slide.tech")
	if grants != 1 {
		t.Fatalf("cache missed: %d", grants)
	}
	now = now.Add(91 * time.Second)
	call("Owner@slide.tech")
	if grants != 2 {
		t.Fatal("did not refresh before expiry")
	}
	call("second@slide.tech")
	if grants != 3 {
		t.Fatal("cross-user cache reuse")
	}
	beforeCalls := calls
	for _, owner := range []string{"", "Owner <owner@slide.tech>", "invalid"} {
		if _, err := client.Call(context.Background(), owner, "jira-search", args); err == nil || !strings.Contains(err.Error(), "verified owner email") {
			t.Fatalf("unverified owner: %v", err)
		}
	}
	if grants != 3 || calls != beforeCalls {
		t.Fatal("anonymous call contacted Context")
	}
	for _, status := range []int{401, 403, 429, 500, 503} {
		httpStatus = status
		_, err := client.Call(context.Background(), "owner@slide.tech", "jira-search", args)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "token-") {
			t.Fatalf("HTTP error=%v", err)
		}
	}
	httpStatus = 0
	rpcError = true
	if _, err := client.Call(context.Background(), "owner@slide.tech", "jira-search", args); err == nil || err.Error() != "context: MCP error -32001" {
		t.Fatalf("RPC error=%v", err)
	}
	rpcError = false
	operationError = true
	if _, err := client.Call(context.Background(), "owner@slide.tech", "jira-search", args); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("operation error=%v", err)
	}
}

func TestClientRefusesRedirectAndSanitizesReadErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-secret")
	if _, err := New(&config.ContextConfig{URL: "https://context.example", ClientID: "wingthing-stage", SecretFile: path}); err == nil || strings.Contains(err.Error(), path) {
		t.Fatalf("file error: %v", err)
	}
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := New(&config.ContextConfig{URL: "https://context.example", ClientID: "wingthing-stage", SecretFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if client.http.CheckRedirect(&http.Request{}, nil) != http.ErrUseLastResponse {
		t.Fatal("credential redirects enabled")
	}
	client.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://other.example")
		w.WriteHeader(307)
	})}
	if _, err := client.Call(context.Background(), "owner@slide.tech", "jira-search", nil); err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("redirect result: %v", err)
	}
	if _, err := (*Client)(nil).Call(context.Background(), "owner@slide.tech", "jira-search", nil); err == nil {
		t.Fatal("missing block accepted")
	}
}

func TestTokenCacheWaitHonorsCancellation(t *testing.T) {
	client := &Client{tokenLock: make(chan struct{}, 1)}
	client.tokenLock <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Call(ctx, "owner@slide.tech", "jira-search", nil); err == nil || err.Error() != "context: request timed out or cancelled" {
		t.Fatalf("cancelled cache wait: %v", err)
	}
}
