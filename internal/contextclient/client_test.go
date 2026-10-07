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
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

type handlerTransport struct{ handler http.Handler }

func TestContextSecretReaderRefusesHardLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sensitive-filename")
	if err := os.WriteFile(path, []byte("private-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(t.TempDir(), "ordinary")); err != nil {
		t.Fatal(err)
	}
	_, err := New(&config.ContextConfig{URL: "https://context.example", ClientID: "wing", SecretFile: path})
	if err == nil || !strings.Contains(err.Error(), "hard links") || strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "private-credential") {
		t.Fatalf("unsafe secret read/error: %v", err)
	}
}

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
	if _, err := client.Call(context.Background(), "owner@slide.tech", "jira-search", args); err == nil || err.Error() != "context: MCP error -32001: [redacted][redacted]" {
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

func newErrorTestClient(t *testing.T) *Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("private-context-client-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := New(&config.ContextConfig{URL: "https://context.example", ClientID: "wingthing-stage", SecretFile: path})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClientPreservesUsefulErrorsAndRedactsCredentials(t *testing.T) {
	const token = "private-owner-bearer-token"
	const secret = "private-context-client-secret"
	for _, tc := range []struct {
		name   string
		status int
		body   any
		want   string
	}{
		{
			name: "SQL operation error",
			body: map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
				"isError": true, "content": []map[string]string{
					{"type": "text", "text": `ERROR: relation "agents" does not exist`},
					{"type": "image", "text": "ignored non-text content"},
					{"type": "text", "text": "details: " + secret + " Bearer " + token},
				},
			}},
			want: "context: ERROR: relation \"agents\" does not exist\ndetails: [redacted] Bearer [redacted]",
		},
		{
			name: "JSON-RPC entitlement guidance",
			body: map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{
				"code": -32001, "message": "missing entitlement: ask an admin for prod-db-ro access; " + secret + " " + token,
			}},
			want: "context: MCP error -32001: missing entitlement: ask an admin for prod-db-ro access; [redacted] [redacted]",
		},
		{
			name: "HTTP rate limit", status: 429,
			body: map[string]string{"message": "rate limited: too many calls against prod-db-ro; " + secret + " " + token},
			want: "context: HTTP 429 (Too Many Requests): rate limited: too many calls against prod-db-ro; [redacted] [redacted]",
		},
		{
			name: "HTTP error description", status: 403,
			body: map[string]string{"error_description": "missing entitlement: request jira access; " + token + " " + secret},
			want: "context: HTTP 403 (Forbidden): missing entitlement: request jira access; [redacted] [redacted]",
		},
		{
			name: "HTTP JSON-RPC error body", status: 400,
			body: map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{
				"code": -32602, "message": "unknown or not permitted tool: happyfox-staff; " + secret + " " + token,
			}},
			want: "context: HTTP 400 (Bad Request): unknown or not permitted tool: happyfox-staff; [redacted] [redacted]",
		},
		{
			name: "HTTP message takes precedence", status: 500,
			body: map[string]string{"message": "useful message", "error_description": "ignored description"},
			want: "context: HTTP 500 (Internal Server Error): useful message",
		},
		{
			name: "HTTP Unicode truncation", status: 500,
			body: map[string]string{"message": strings.Repeat("界", 600)},
			want: "context: HTTP 500 (Internal Server Error): " + strings.Repeat("界", 500),
		},
		{
			name: "HTTP secret crosses truncation boundary", status: 500,
			body: map[string]string{"message": strings.Repeat("x", 490) + secret + token},
			want: "context: HTTP 500 (Internal Server Error): " + strings.Repeat("x", 490) + "[redacted]",
		},
		{
			name: "HTTP token crosses truncation boundary", status: 500,
			body: map[string]string{"message": strings.Repeat("x", 490) + token + secret},
			want: "context: HTTP 500 (Internal Server Error): " + strings.Repeat("x", 490) + "[redacted]",
		},
		{
			name: "HTTP non-JSON fallback", status: 503,
			body: secret + token,
			want: "context: HTTP 503 (Service Unavailable)",
		},
		{
			name: "HTTP oversized fallback", status: 500,
			body: map[string]string{"message": strings.Repeat("x", maxResponseBytes) + secret + token},
			want: "context: HTTP 500 (Internal Server Error)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newErrorTestClient(t)
			grants, calls := 0, 0
			client.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					grants++
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "expires_in": 100})
					return
				}
				calls++
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				if raw, ok := tc.body.(string); ok {
					_, _ = io.WriteString(w, raw)
				} else {
					_ = json.NewEncoder(w).Encode(tc.body)
				}
			})}
			output, err := client.Call(context.Background(), "owner@slide.tech", "database-query", nil)
			if output != "" || err == nil || err.Error() != tc.want {
				t.Fatalf("output=%q err=%v, want %q", output, err, tc.want)
			}
			if !utf8.ValidString(err.Error()) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), token) {
				t.Fatal("error contains credentials or invalid UTF-8")
			}
			if grants != 1 || calls != 1 {
				t.Fatalf("unexpected retry: grants=%d calls=%d", grants, calls)
			}
		})
	}
}

func TestClientTokenErrorsRedactAssertionAndRequest(t *testing.T) {
	client := newErrorTestClient(t)
	var assertion, request string
	client.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatal("token failure reached MCP")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		request = string(body)
		r.Body = io.NopCloser(strings.NewReader(request))
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		assertion = r.Form.Get("client_assertion")
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error_description": "missing entitlement: ask an admin; " + string(client.secret) + "; request=" + request + "; assertion=" + assertion})
	})}
	_, err := client.Call(context.Background(), "owner@slide.tech", "jira-search", nil)
	want := "context: HTTP 400 (Bad Request): missing entitlement: ask an admin; [redacted]; request=[redacted]; assertion=[redacted]"
	if err == nil || err.Error() != want || strings.Contains(err.Error(), assertion) || strings.Contains(err.Error(), request) {
		t.Fatalf("token error=%v", err)
	}
}

func TestClientRefreshesUnauthorizedOwnerOnce(t *testing.T) {
	for _, outcome := range []string{"success", "HTTP 401", "HTTP 403", "RPC error", "operation error", "refresh failure"} {
		t.Run(outcome, func(t *testing.T) {
			client := newErrorTestClient(t)
			const owner = "owner@slide.tech"
			const other = "other@slide.tech"
			const rejected = "rejected-owner-token"
			const replacement = "replacement-owner-token"
			client.tokens[owner] = cachedToken{rejected, time.Now().Add(time.Hour)}
			client.tokens[other] = cachedToken{"other-owner-token", time.Now().Add(time.Hour)}
			grants, calls := 0, 0
			var originalBody, originalHeaders string
			client.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					grants++
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if r.Form.Get("subject_token") != owner {
						t.Fatal("refreshed the wrong owner")
					}
					if outcome == "refresh failure" {
						w.WriteHeader(401)
						_ = json.NewEncoder(w).Encode(map[string]string{"error_description": "refresh denied: " + string(client.secret) + " " + rejected + " " + r.Form.Get("client_assertion")})
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": replacement, "expires_in": 100})
					return
				}
				calls++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				headers := r.Header.Clone()
				headers.Del("Authorization")
				if calls == 1 {
					originalBody, originalHeaders = string(body), fmt.Sprint(headers)
					if r.Header.Get("Authorization") != "Bearer "+rejected {
						t.Fatal("did not use cached owner token")
					}
					w.WriteHeader(401)
					_ = json.NewEncoder(w).Encode(map[string]string{"message": "revoked: " + rejected})
					return
				}
				if calls != 2 || string(body) != originalBody || fmt.Sprint(headers) != originalHeaders || r.Header.Get("Authorization") != "Bearer "+replacement {
					t.Fatal("retry changed the call or reused the rejected token")
				}
				text := "owner result: " + string(client.secret) + " " + rejected + " " + replacement
				switch outcome {
				case "HTTP 401", "HTTP 403":
					status := 401
					if outcome == "HTTP 403" {
						status = 403
					}
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(map[string]string{"message": text})
				case "RPC error":
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32001, "message": text}})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": outcome == "operation error", "content": []map[string]string{{"type": "text", "text": text}}}})
				}
			})}
			output, err := client.Call(context.Background(), "Owner@slide.tech", "jira-search", map[string]any{"query": "project = SLIDE"})
			wantCalls := 2
			if outcome == "refresh failure" {
				wantCalls = 1
			}
			if grants != 1 || calls != wantCalls {
				t.Fatalf("retry counts: grants=%d calls=%d", grants, calls)
			}
			if outcome == "success" {
				if err != nil || output != "owner result: [redacted] [redacted] [redacted]" {
					t.Fatalf("output=%q err=%v", output, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), map[string]string{
				"HTTP 401": "HTTP 401", "HTTP 403": "HTTP 403", "RPC error": "MCP error -32001",
				"operation error": "owner result", "refresh failure": "refresh denied",
			}[outcome]) {
				t.Fatalf("wrong retry error: %v", err)
			}
			returned := output + fmt.Sprint(err)
			for _, credential := range []string{string(client.secret), rejected, replacement} {
				if strings.Contains(returned, credential) {
					t.Fatal("retry leaked a credential")
				}
			}
			if client.tokens[other].value != "other-owner-token" {
				t.Fatal("invalidated another owner's cached token")
			}
			if outcome == "refresh failure" {
				if _, ok := client.tokens[owner]; ok {
					t.Fatal("failed refresh retained the rejected token")
				}
			} else if client.tokens[owner].value != replacement {
				t.Fatal("replacement token was not cached")
			}
		})
	}
}

func TestClientRefreshErrorRedactsRejectedTokenBeforeTruncation(t *testing.T) {
	client := newErrorTestClient(t)
	const rejected = "rejected-owner-token-crossing-the-boundary"
	client.tokens["owner@slide.tech"] = cachedToken{rejected, time.Now().Add(time.Hour)}
	client.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		if r.URL.Path == "/oauth/token" {
			_ = json.NewEncoder(w).Encode(map[string]string{"message": strings.Repeat("x", 490) + rejected})
		}
	})}
	_, err := client.Call(context.Background(), "owner@slide.tech", "jira-search", nil)
	want := "context: HTTP 401 (Unauthorized): " + strings.Repeat("x", 490) + "[redacted]"
	if err == nil || err.Error() != want {
		t.Fatalf("refresh error=%v, want %q", err, want)
	}
}

func TestClientConcurrent401PreservesRefreshedToken(t *testing.T) {
	client := newErrorTestClient(t)
	const rejected, replacement = "rejected-owner-token", "replacement-owner-token"
	client.tokens["owner@slide.tech"] = cachedToken{rejected, time.Now().Add(time.Hour)}
	var grants, oldCalls, newCalls atomic.Int32
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	refreshed := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			grants.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": replacement, "expires_in": 100})
			return
		}
		if r.Header.Get("Authorization") == "Bearer "+rejected {
			index := oldCalls.Add(1)
			arrived <- struct{}{}
			gate := release
			if index == 2 {
				// Delay this stale 401 until the first call has already refreshed.
				gate = refreshed
			}
			select {
			case <-gate:
			case <-ctx.Done():
			}
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+replacement {
			t.Error("retry did not use the replacement token")
		}
		if newCalls.Add(1) == 1 {
			close(refreshed)
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`)
	})}
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := client.Call(ctx, "owner@slide.tech", "jira-search", nil)
			errors <- err
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("concurrent calls did not arrive")
		}
	}
	close(release)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if grants.Load() != 1 || oldCalls.Load() != 2 || newCalls.Load() != 2 || client.tokens["owner@slide.tech"].value != replacement {
		t.Fatalf("concurrent refresh: grants=%d rejected=%d replacement=%d", grants.Load(), oldCalls.Load(), newCalls.Load())
	}
}
