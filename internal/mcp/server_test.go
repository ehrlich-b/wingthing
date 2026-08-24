package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

func TestServerPublishesAndCallsNativeToolsWithoutRolePolicy(t *testing.T) {
	srv := NewServer(nil, nil, nil)
	srv.SetNativeTools([]NativeTool{{
		Name: "terminal_list", Title: "List owned terminals", Description: "List the caller's terminals.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false},
		Annotations: map[string]any{"readOnlyHint": true},
		Call: func(_ context.Context, principal Principal, arguments json.RawMessage) (map[string]any, bool, error) {
			if principal.UserID != "user-1" || principal.ClientID != "client-1" {
				t.Fatalf("principal = %+v", principal)
			}
			if string(arguments) != `{}` {
				t.Fatalf("arguments = %s", arguments)
			}
			return map[string]any{"owner": principal.UserID}, false, nil
		},
	}}, func(*http.Request) Principal { return Principal{UserID: "user-1", ClientID: "client-1"} })

	call := func(body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "https://wing.example/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	listed := call(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	tools := listed["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["title"] != "List owned terminals" {
		t.Fatalf("tools = %#v", tools)
	}
	result := call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"terminal_list"}}`)["result"].(map[string]any)
	if result["isError"] != false || result["structuredContent"].(map[string]any)["owner"] != "user-1" {
		t.Fatalf("result = %#v", result)
	}
}

func testMCPServer() *Server {
	p := &Policy{Roles: map[string]*RolePolicy{
		"eng": {Enabled: true},
	}, DefaultAllowAll: true}
	return NewServer(egg.NewToolRunner(nil), p, func(*http.Request) []string { return []string{"eng"} })
}

func TestServerNegotiatesSupportedProtocolVersion(t *testing.T) {
	srv := testMCPServer()
	req := httptest.NewRequest(http.MethodPost, "https://wing.example/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
	))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("protocol version = %q", out.Result.ProtocolVersion)
	}
}

func TestServerRejectsCrossOriginAndUnsupportedVersion(t *testing.T) {
	srv := testMCPServer()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

	t.Run("cross-origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://wing.example/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", w.Code)
		}
	})

	t.Run("protocol-version", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://wing.example/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "not-a-version")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
}

func TestServerRejectsInvalidContentAndMultipleJSONValues(t *testing.T) {
	srv := testMCPServer()

	req := httptest.NewRequest(http.MethodPost, "https://wing.example/mcp", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type status = %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "https://wing.example/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"} {"jsonrpc":"2.0"}`,
	))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK { // JSON-RPC parse errors use an HTTP 200 response.
		t.Fatalf("multiple JSON status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"code":-32700`) {
		t.Fatalf("expected parse error: %s", w.Body.String())
	}
}

func TestServerSupportsPingAndIgnoresUnknownNotifications(t *testing.T) {
	srv := testMCPServer()
	for name, body := range map[string]string{
		"ping":                 `{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		"unknown-notification": `{"jsonrpc":"2.0","method":"notifications/example"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://wing.example/mcp", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("MCP-Protocol-Version", "2025-11-25")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if name == "ping" {
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"result":{}`) {
					t.Fatalf("ping response = %d %s", w.Code, w.Body.String())
				}
			} else if w.Code != http.StatusAccepted || w.Body.Len() != 0 {
				t.Fatalf("notification response = %d %q", w.Code, w.Body.String())
			}
		})
	}
}

func TestServerPublishesOptionalNamedParameterSchema(t *testing.T) {
	p := &Policy{Roles: map[string]*RolePolicy{"eng": {Enabled: true}}, DefaultAllowAll: true}
	runner := egg.NewToolRunner([]*config.ToolConfig{
		{Name: "generic", Run: "true"},
		{
			Name: "search", Run: "true", Params: []config.ToolParam{
				{Name: "method", Description: "Read-only request method", Type: "string", Required: true, Enum: []string{"GET", "POST"}},
				{Name: "body", Description: "JSON request body", Type: "object", Examples: []any{map[string]any{"size": 1}}},
			},
		},
	})
	srv := NewServer(runner, p, func(*http.Request) []string { return []string{"eng"} })
	req := httptest.NewRequest(http.MethodPost, "https://wing.example/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var out struct {
		Result struct {
			Tools []mcpTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Result.Tools) != 2 {
		t.Fatalf("tools = %#v", out.Result.Tools)
	}
	if _, ok := out.Result.Tools[0].InputSchema["properties"].(map[string]any)["args"]; !ok {
		t.Fatalf("generic schema = %#v", out.Result.Tools[0].InputSchema)
	}
	if out.Result.Tools[0].InputSchema["additionalProperties"] != false {
		t.Fatalf("generic schema allows extra properties: %#v", out.Result.Tools[0].InputSchema)
	}
	named := out.Result.Tools[1].InputSchema
	if named["additionalProperties"] != false {
		t.Fatalf("named schema allows extra properties: %#v", named)
	}
	required, _ := named["required"].([]any)
	if len(required) != 1 || required[0] != "method" {
		t.Fatalf("required = %#v", named["required"])
	}
	properties := named["properties"].(map[string]any)
	method := properties["method"].(map[string]any)
	if method["description"] != "Read-only request method" {
		t.Fatalf("method schema = %#v", method)
	}
}

func TestGenericToolArgumentsRejectUnknownProperties(t *testing.T) {
	if _, err := toolArguments(json.RawMessage(`{"args":["ok"],"credential":"must-not-be-ignored"}`), nil); err == nil {
		t.Fatal("generic tool arguments silently accepted an unknown property")
	}
	args, err := toolArguments(json.RawMessage(`{"args":["ok"]}`), nil)
	if err != nil || len(args) != 1 || args[0] != "ok" {
		t.Fatalf("valid generic tool arguments = %v, %v", args, err)
	}
}

func TestServerMapsNamedParametersToPositionalToolArgs(t *testing.T) {
	p := &Policy{Roles: map[string]*RolePolicy{"eng": {Enabled: true}}, DefaultAllowAll: true}
	runner := egg.NewToolRunner([]*config.ToolConfig{{
		Name: "search",
		Run:  `printf '%s|%s|%s' "$1" "$2" "$3"`,
		Params: []config.ToolParam{
			{Name: "method", Type: "string", Required: true, Enum: []string{"GET", "POST"}},
			{Name: "path", Type: "string", Required: true},
			{Name: "body", Type: "object"},
		},
	}})
	srv := NewServer(runner, p, func(*http.Request) []string { return []string{"eng"} })

	call := func(arguments string) map[string]any {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":` + arguments + `}}`
		req := httptest.NewRequest(http.MethodPost, "https://wing.example/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out["result"].(map[string]any)
	}

	result := call(`{"method":"GET","path":"/_cluster/health","body":{"level":"cluster"}}`)
	if result["isError"] != false {
		t.Fatalf("call failed: %#v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"]
	if content != `GET|/_cluster/health|{"level":"cluster"}` {
		t.Fatalf("mapped args output = %q", content)
	}

	for _, arguments := range []string{
		`{"path":"/_cluster/health"}`,
		`{"method":"DELETE","path":"/_cluster/health"}`,
		`{"method":"GET","path":"/_cluster/health","body":"{}"}`,
		`{"method":"GET","path":"/_cluster/health","extra":true}`,
	} {
		result := call(arguments)
		if result["isError"] != true {
			t.Errorf("arguments %s should fail: %#v", arguments, result)
		}
	}

	result = call(`{"method":"GET","path":"/_cluster/health"}`)
	content = result["content"].([]any)[0].(map[string]any)["text"]
	if content != "GET|/_cluster/health|" {
		t.Fatalf("omitted trailing optional arg output = %q", content)
	}
}
