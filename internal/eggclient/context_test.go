package eggclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/contextclient"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/golang-jwt/jwt/v5"
)

func contextSocketCall(t *testing.T, path string, request any) egg.ToolResponse {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var response egg.ToolResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestContextThroughEggTools(t *testing.T) {
	const secret = "context-secret-that-must-never-reach-an-egg"
	var mu sync.Mutex
	grants, calls, status := 0, 0, 0
	jtis := map[string]bool{}
	tokens := map[string]string{}
	var endpoint string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			claims := &jwt.RegisteredClaims{}
			token, err := jwt.ParseWithClaims(r.Form.Get("client_assertion"), claims, func(token *jwt.Token) (any, error) {
				if token.Method.Alg() != "HS256" {
					return nil, fmt.Errorf("wrong algorithm")
				}
				return []byte(secret), nil
			}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience(endpoint+"/oauth/token"), jwt.WithIssuer("wingthing-stage"), jwt.WithIssuedAt())
			if err != nil || !token.Valid {
				t.Errorf("assertion rejected: %v", err)
				w.WriteHeader(400)
				return
			}
			if claims.Subject != "wingthing-stage" || claims.IssuedAt == nil || claims.ExpiresAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > 60*time.Second || !claims.ExpiresAt.After(claims.IssuedAt.Time) || claims.ID == "" || jtis[claims.ID] {
				t.Errorf("invalid/replayed assertion claims: %+v", claims)
				w.WriteHeader(400)
				return
			}
			jtis[claims.ID] = true
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" || r.Form.Get("client_id") != "wingthing-stage" || r.Form.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" || r.Form.Get("subject_token_type") != "https://context.pants.taxi/oauth/token-type/user" || r.Form.Get("scope") != "happyfox" {
				t.Errorf("invalid token exchange envelope")
			}
			owner := r.Form.Get("subject_token")
			if owner != "owner@slide.tech" && owner != "second@slide.tech" {
				t.Errorf("spoofed subject: %q", owner)
			}
			grants++
			access := fmt.Sprintf("user-token-%d", grants)
			tokens[access] = owner
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "expires_in": 1, "scope": "happyfox"})
		case "/mcp":
			owner := tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
			if owner == "" {
				t.Error("missing user token")
				w.WriteHeader(401)
				return
			}
			if r.Header.Get("Mcp-Protocol-Version") != "2026-07-28" || r.Header.Get("Mcp-Method") != "tools/call" || r.Header.Get("Mcp-Name") != "happyfox-tickets" || r.Header.Get("Accept") != "application/json, text/event-stream" {
				t.Error("incorrect MCP headers")
			}
			var request struct {
				JSONRPC string `json:"jsonrpc"`
				ID      int    `json:"id"`
				Method  string `json:"method"`
				Params  struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
					Meta      map[string]any `json:"_meta"`
				} `json:"params"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.JSONRPC != "2.0" || request.ID != 1 || request.Method != "tools/call" || request.Params.Name != "happyfox-tickets" || request.Params.Arguments["subject"] != "spoof@slide.tech" || request.Params.Arguments["page"] != float64(2) || request.Params.Meta["io.modelcontextprotocol/protocolVersion"] != "2026-07-28" {
				t.Errorf("bad tools/call: %+v", request)
			}
			calls++
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, secret+" upstream token "+r.Header.Get("Authorization"))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]string{{"type": "text", "text": "tickets for " + owner + " " + secret + " " + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")}}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	endpoint = server.URL
	root, err := os.MkdirTemp("/tmp", "wt-ctx-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	secretPath := filepath.Join(root, "context.secret")
	if err := os.WriteFile(secretPath, []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wc := &config.WingConfig{Context: &config.ContextConfig{URL: server.URL, ClientID: "wingthing-stage", SecretFile: secretPath, Scopes: []string{"happyfox"}}}
	if err := config.SaveWingConfig(root, wc); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Dir: root}
	tools := []*config.ToolConfig{
		{Name: "tickets", Context: "happyfox-tickets", Params: []config.ToolParam{{Name: "subject", Required: true}, {Name: "page", Type: "integer"}}, Env: map[string]string{"WT_USER_EMAIL": "spoof@slide.tech"}},
		{Name: "environment", Run: "env"},
	}
	opts := SpawnEggOpts{}
	identity := EggIdentity{UserID: "verified-owner", Email: "Owner@slide.tech"}
	listener, err := PrepareBrowserTools(cfg, "owner", tools, &opts, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("WT_USER_EMAIL", "spoof@slide.tech")
	request := map[string]any{"tool": "tickets", "args": []string{"spoof@slide.tech", "2"}, "owner": "spoof@slide.tech", "env": map[string]string{"WT_USER_EMAIL": "spoof@slide.tech"}}
	call := func(path, wantOwner string) {
		t.Helper()
		response := contextSocketCall(t, path, request)
		if response.Error != "" || response.ExitCode != 0 || !strings.Contains(response.Stdout, "tickets for "+wantOwner) {
			t.Fatalf("call: %+v", response)
		}
		if strings.Contains(fmt.Sprintf("%+v", response), secret) || strings.Contains(response.Stdout, "user-token-") {
			t.Fatalf("credential in tool output")
		}
	}
	call(opts.ToolSocketPath, "owner@slide.tech")
	call(opts.ToolSocketPath, "owner@slide.tech")
	mu.Lock()
	count := grants
	mu.Unlock()
	if count != 1 {
		t.Fatalf("token was not cached: %d grants", count)
	}
	time.Sleep(1100 * time.Millisecond)
	call(opts.ToolSocketPath, "owner@slide.tech")
	mu.Lock()
	count = grants
	mu.Unlock()
	if count != 2 {
		t.Fatalf("token was not re-minted: %d grants", count)
	}
	// A separate verified owner must never reuse another owner's token.
	secondOpts := SpawnEggOpts{}
	second, err := PrepareBrowserTools(cfg, "second", tools, &secondOpts, EggIdentity{Email: "second@slide.tech"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	call(secondOpts.ToolSocketPath, "second@slide.tech")
	noOwnerOpts := SpawnEggOpts{}
	noOwner, err := PrepareBrowserTools(cfg, "anonymous", tools, &noOwnerOpts)
	if err != nil {
		t.Fatal(err)
	}
	defer noOwner.Close()
	response := contextSocketCall(t, noOwnerOpts.ToolSocketPath, request)
	if !strings.Contains(response.Error, "verified owner email") {
		t.Fatalf("anonymous call: %+v", response)
	}
	mu.Lock()
	count, beforeCalls := grants, calls
	status = 403
	mu.Unlock()
	if count != 3 || beforeCalls != 4 {
		t.Fatalf("anonymous call reached Context: grants=%d calls=%d", count, beforeCalls)
	}
	response = contextSocketCall(t, opts.ToolSocketPath, request)
	if !strings.Contains(response.Error, "403 (Forbidden)") || strings.Contains(fmt.Sprintf("%+v", response), secret) {
		t.Fatalf("403 response: %+v", response)
	}
	environment := contextSocketCall(t, opts.ToolSocketPath, egg.ToolRequest{Tool: "environment"})
	if environment.Error != "" || environment.ExitCode != 0 || strings.Contains(environment.Stdout, secret) || strings.Contains(environment.Stdout, "user-token-") {
		t.Fatalf("credential in tool env: %+v", environment)
	}
	// SpawnEgg builds its environment from this policy; credentials are never added to it.
	protected, targets, err := protectContextSecret(egg.DefaultEggConfig(), wc.Context, root, root)
	if err != nil {
		t.Fatal(err)
	}
	envPath, err := writeEggEnvironment(root, protected.BuildEnvMap(root))
	if err != nil {
		t.Fatal(err)
	}
	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envData), secret) || strings.Contains(string(envData), "user-token-") {
		t.Fatal("credential in egg env")
	}
	if len(targets) != 1 || targets[0] != secretPath || !ContainsExactPath(protected.FS, "deny:"+secretPath) {
		t.Fatalf("secret unprotected: %v %v", targets, protected.FS)
	}
	if err := egg.ValidateProtectedWriteTargetBoundary(targets, false); err == nil {
		t.Fatal("secret accepted without sandbox")
	}
}

func TestContextSecretProtectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	path, alias := filepath.Join(root, "secret"), filepath.Join(root, "alias")
	if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	original := egg.DefaultEggConfig()
	before := append([]string(nil), original.FS...)
	protected, targets, err := protectContextSecret(original, &config.ContextConfig{URL: "https://context.pants.taxi", ClientID: "wingthing-stage", SecretFile: alias}, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || !ContainsExactPath(protected.FS, "deny:"+path) || !ContainsExactPath(protected.FS, "deny:"+alias) {
		t.Fatalf("incomplete protection: %v %v", protected.FS, targets)
	}
	if fmt.Sprint(before) != fmt.Sprint(original.FS) {
		t.Fatal("mutated caller config")
	}
}

func TestContextSecretNeverInEggOrToolEnvironment(t *testing.T) {
	const secret = "unique-secret-stays-in-wing-memory"
	root := t.TempDir()
	path := filepath.Join(root, "context.secret")
	if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	contextConfig := &config.ContextConfig{URL: "https://context.example", ClientID: "wingthing-stage", SecretFile: path}
	client, err := contextclient.New(contextConfig)
	if err != nil {
		t.Fatal(err)
	}
	policy, paths, err := protectContextSecret(egg.DefaultEggConfig(), contextConfig, root, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !ContainsExactPath(policy.FS, "deny:"+path) || !egg.RequiresSandbox(policy, "claude") {
		t.Fatal("secret lacks enforced deny policy")
	}
	envFile, err := writeEggEnvironment(root, policy.BuildEnvMap(root))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("secret in transported egg env")
	}
	runner := egg.NewToolRunner([]*config.ToolConfig{{Name: "env", Run: "env"}}, client)
	response := runner.Call("env", nil)
	if response.Error != "" || response.ExitCode != 0 || strings.Contains(fmt.Sprintf("%+v", response), secret) {
		t.Fatalf("secret in tool environment/output: %+v", response)
	}
}

func TestContextSecretRejectsGrantAliases(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(private, "secret")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "leak")
	if err := os.Symlink(secret, alias); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Symlink(alias, nested); err != nil {
		t.Fatal(err)
	}
	c := &config.ContextConfig{URL: "https://context.example", ClientID: "wing", SecretFile: secret}
	for _, grant := range []string{"ro:" + alias, "rw:" + alias, "ro:" + nested, "ro:leak", "rw:~/nested", "ro:" + secret} {
		t.Run(grant, func(t *testing.T) {
			if _, _, err := protectContextSecret(&egg.EggConfig{FS: []string{grant}}, c, root, root); err == nil || !strings.Contains(err.Error(), "exposes protected secret path") {
				t.Fatalf("grant accepted: %v", err)
			}
		})
	}
	// Protected paths can also be directories; no descendant may be granted.
	c.SecretFile = private
	if _, _, err := protectContextSecret(&egg.EggConfig{FS: []string{"ro:" + secret}}, c, root, root); err == nil {
		t.Fatal("grant inside protected directory accepted")
	}
}

func TestContextSecretMasksAncestorGrantAliases(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.MkdirAll(filepath.Join(private, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(private, "sub", "secret")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(private, alias); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Symlink(alias, nested); err != nil {
		t.Fatal(err)
	}
	c := &config.ContextConfig{URL: "https://context.example", ClientID: "wing", SecretFile: secret}
	for _, mode := range []string{"ro", "rw"} {
		original := &egg.EggConfig{FS: []string{mode + ":" + alias, mode + ":" + nested}}
		policy, targets, err := protectContextSecret(original, c, root, root)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{secret, filepath.Join(alias, "sub", "secret"), filepath.Join(nested, "sub", "secret")} {
			if !ContainsExactPath(policy.FS, "deny:"+path) || !ContainsExactPath(targets, path) {
				t.Fatalf("unprotected alias %s: %v, %v", path, policy.FS, targets)
			}
		}
		// Both Linux deny mounts and macOS Seatbelt receive the projected paths.
		sb := policy.ToSandboxConfig(root)
		for _, path := range targets {
			if !ContainsExactPath(sb.Deny, path) {
				t.Fatalf("sandbox lost deny %s", path)
			}
		}
		if len(original.FS) != 2 {
			t.Fatal("mutated final caller policy")
		}
	}
}
