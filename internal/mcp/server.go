package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

const (
	protocolVersion = "2025-11-25"
	maxRequestBytes = 1 << 20
)

var supportedProtocolVersions = map[string]bool{
	"2025-03-26": true,
	"2025-06-18": true,
	"2025-11-25": true,
}

// RolesFunc resolves all caller roles from the request. Disabled roles contribute no access;
// tools allowed by any enabled role are included in the caller's maximum subset.
type RolesFunc func(*http.Request) []string

// CallObserver receives allowed and denied tool-call attempts for centralized auditing.
type CallObserver func(*http.Request, string, []string, egg.ToolResponse)

// CallEnvFunc supplies trusted per-request identity variables to the privileged tool process.
type CallEnvFunc func(*http.Request) map[string]string

// Principal is the authenticated identity supplied to built-in control tools.
// UserID owns created resources. ClientID identifies the acting MCP client.
type Principal struct {
	UserID   string
	Email    string
	ClientID string
	Roles    []string
}

// PrincipalFunc resolves the authenticated caller from a request.
type PrincipalFunc func(*http.Request) Principal

// NativeTool is a typed in-process MCP operation. Native tools are available to
// authenticated roost users independently of role-scoped executable tools.
type NativeTool struct {
	Name        string
	Title       string
	Description string
	InputSchema map[string]any
	Annotations map[string]any
	Call        func(context.Context, Principal, json.RawMessage) (map[string]any, bool, error)
}

// NativeCallObserver receives the exact typed call envelope for audit logging.
type NativeCallObserver func(*http.Request, string, json.RawMessage, map[string]any, bool)

// Server is an MCP-over-HTTP (Streamable HTTP) endpoint over a shared ToolRunner, gated by
// a per-role Policy.
type Server struct {
	runner        *egg.ToolRunner
	policy        *Policy
	rolesOf       RolesFunc
	observe       CallObserver
	callEnv       CallEnvFunc
	native        []NativeTool
	principalOf   PrincipalFunc
	observeNative NativeCallObserver
}

func NewServer(runner *egg.ToolRunner, policy *Policy, rolesOf RolesFunc) *Server {
	return &Server{runner: runner, policy: policy, rolesOf: rolesOf}
}

func (s *Server) SetCallObserver(observer CallObserver) { s.observe = observer }

func (s *Server) SetCallEnv(callEnv CallEnvFunc) { s.callEnv = callEnv }

func (s *Server) SetNativeTools(tools []NativeTool, principalOf PrincipalFunc) {
	s.native = append([]NativeTool(nil), tools...)
	s.principalOf = principalOf
}

func (s *Server) SetNativeCallObserver(observer NativeCallObserver) { s.observeNative = observer }

func (s *Server) HasNativeTools() bool { return len(s.native) > 0 }

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ServeHTTP handles a single JSON-RPC request per POST (stateless Streamable HTTP).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !OriginAllowed(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var req rpcRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if err := ensureJSONEOF(dec); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "invalid request"}})
		return
	}
	if req.Method != "initialize" {
		version := r.Header.Get("MCP-Protocol-Version")
		if version == "" {
			version = "2025-03-26" // required backwards-compatible assumption
		}
		if !supportedProtocolVersions[version] {
			http.Error(w, "unsupported MCP protocol version", http.StatusBadRequest)
			return
		}
	}
	var roles []string
	if s.rolesOf != nil {
		roles = s.rolesOf(r)
	}

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ProtocolVersion == "" {
			writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid initialize params"}})
			return
		}
		selected := params.ProtocolVersion
		if !supportedProtocolVersions[selected] {
			selected = protocolVersion
		}
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": selected,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "wingthing", "version": "mcp"},
		}})
	case "notifications/initialized", "notifications/cancelled":
		// Notifications carry no id and expect no response.
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "tools/list":
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"tools": s.visibleTools(roles),
		}})
	case "tools/call":
		s.handleCall(w, r, req, roles)
	default:
		if len(req.ID) == 0 {
			// JSON-RPC notifications never receive a response, including unknown ones.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}})
	}
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}

// Native MCP clients omit Origin. Browser-originated requests must be same-host to prevent
// DNS rebinding; reverse proxies preserve the external Host header.
func OriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// mcpTool is one entry in a tools/list result.
type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

// genericSchema accepts positional string args for tools without parameter metadata.
func genericSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"args": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Positional arguments passed to the tool",
			},
		},
	}
}

// parameterSchema exposes optional named parameter metadata as JSON Schema. Tools that omit
// params retain the generic positional args schema above.
func parameterSchema(params []config.ToolParam) map[string]any {
	properties := make(map[string]any, len(params))
	required := make([]string, 0, len(params))
	for _, param := range params {
		paramType := param.Type
		if paramType == "" {
			paramType = "string"
		}
		property := map[string]any{"type": paramType}
		if param.Description != "" {
			property["description"] = param.Description
		}
		if len(param.Enum) > 0 {
			property["enum"] = param.Enum
		}
		if len(param.Examples) > 0 {
			property["examples"] = param.Examples
		}
		properties[param.Name] = property
		if param.Required {
			required = append(required, param.Name)
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func (s *Server) visibleTools(roles []string) []mcpTool {
	tools := make([]mcpTool, 0, len(s.native))
	nativeNames := make(map[string]bool, len(s.native))
	for _, tool := range s.native {
		nativeNames[tool.Name] = true
		tools = append(tools, mcpTool{
			Name: tool.Name, Title: tool.Title, Description: tool.Description,
			InputSchema: tool.InputSchema, Annotations: tool.Annotations,
		})
	}
	if s.runner == nil || s.policy == nil {
		return tools
	}
	for _, t := range s.runner.List() {
		if nativeNames[t.Name] || !s.policy.AllowedAny(roles, t.Name) {
			continue
		}
		schema := genericSchema()
		if len(t.Params) > 0 {
			schema = parameterSchema(t.Params)
		}
		tools = append(tools, mcpTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return tools
}

func (s *Server) handleCall(w http.ResponseWriter, r *http.Request, req rpcRequest, roles []string) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}})
		return
	}
	if len(params.Arguments) == 0 || bytes.Equal(bytes.TrimSpace(params.Arguments), []byte("null")) {
		params.Arguments = json.RawMessage(`{}`)
	}
	for _, tool := range s.native {
		if tool.Name != params.Name {
			continue
		}
		if tool.Call == nil {
			writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32603, Message: "tool handler unavailable"}})
			return
		}
		principal := Principal{}
		if s.principalOf != nil {
			principal = s.principalOf(r)
		}
		data, isError, err := tool.Call(r.Context(), principal, params.Arguments)
		if err != nil {
			data = map[string]any{"error": err.Error()}
			isError = true
		}
		if s.observeNative != nil {
			s.observeNative(r, params.Name, params.Arguments, data, isError)
		}
		writeNativeToolResult(w, req.ID, data, isError)
		return
	}
	// A tool the role may not see is treated as nonexistent — don't reveal it.
	if s.runner == nil || s.policy == nil || !s.runner.Has(params.Name) || !s.policy.AllowedAny(roles, params.Name) {
		s.observeCall(r, params.Name, nil, egg.ToolResponse{Error: "not permitted"})
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "unknown or not permitted tool: " + params.Name}})
		return
	}
	toolParams, _ := s.runner.ParamsFor(params.Name)
	args, err := toolArguments(params.Arguments, toolParams)
	if err != nil {
		resp := egg.ToolResponse{ExitCode: 1, Error: "Invalid arguments: " + err.Error()}
		s.observeCall(r, params.Name, nil, resp)
		writeToolResult(w, req.ID, resp)
		return
	}
	var extraEnv map[string]string
	if s.callEnv != nil {
		extraEnv = s.callEnv(r)
	}
	resp := s.runner.CallWithEnv(params.Name, args, extraEnv)
	s.observeCall(r, params.Name, args, resp)
	writeToolResult(w, req.ID, resp)
}

func writeNativeToolResult(w http.ResponseWriter, id json.RawMessage, data map[string]any, isError bool) {
	encoded, err := json.Marshal(data)
	if err != nil {
		data = map[string]any{"error": "could not encode tool result"}
		encoded = []byte(`{"error":"could not encode tool result"}`)
		isError = true
	}
	writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(encoded)}},
		"structuredContent": data,
		"isError":           isError,
	}})
}

func writeToolResult(w http.ResponseWriter, id json.RawMessage, resp egg.ToolResponse) {
	text := resp.Stdout
	if resp.Stderr != "" {
		text += resp.Stderr
	}
	if resp.Error != "" {
		text += resp.Error
	}
	writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": resp.ExitCode != 0 || resp.Error != "",
	}})
}

func toolArguments(raw json.RawMessage, params []config.ToolParam) ([]string, error) {
	if len(params) == 0 {
		var arguments struct {
			Args []string `json:"args"`
		}
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			raw = json.RawMessage(`{}`)
		}
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return nil, fmt.Errorf("expected an object containing an optional string array named args")
		}
		return arguments.Args, nil
	}

	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = json.RawMessage(`{}`)
	}
	var named map[string]json.RawMessage
	if err := json.Unmarshal(raw, &named); err != nil || named == nil {
		return nil, fmt.Errorf("expected an object with named parameters")
	}
	known := make(map[string]bool, len(params))
	for _, param := range params {
		known[param.Name] = true
	}
	unknown := make([]string, 0)
	for name := range named {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown parameter %q", unknown[0])
	}

	args := make([]string, len(params))
	lastPresent := -1
	for i, param := range params {
		value, ok := named[param.Name]
		if !ok {
			if param.Required {
				return nil, fmt.Errorf("missing required parameter %q", param.Name)
			}
			continue
		}
		arg, err := toolArgumentValue(value, param)
		if err != nil {
			return nil, fmt.Errorf("parameter %q %w", param.Name, err)
		}
		args[i] = arg
		lastPresent = i
	}
	return args[:lastPresent+1], nil
}

func toolArgumentValue(raw json.RawMessage, param config.ToolParam) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return "", fmt.Errorf("contains invalid JSON")
	}
	paramType := param.Type
	if paramType == "" {
		paramType = "string"
	}
	var out string
	switch paramType {
	case "string":
		v, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("must be a string")
		}
		out = v
		if len(param.Enum) > 0 && !stringIn(param.Enum, v) {
			return "", fmt.Errorf("must be one of %s", strings.Join(param.Enum, ", "))
		}
	case "integer":
		v, ok := value.(json.Number)
		if !ok {
			return "", fmt.Errorf("must be an integer")
		}
		if _, err := strconv.ParseInt(v.String(), 10, 64); err != nil {
			return "", fmt.Errorf("must be an integer")
		}
		out = v.String()
	case "number":
		v, ok := value.(json.Number)
		if !ok {
			return "", fmt.Errorf("must be a number")
		}
		if _, err := v.Float64(); err != nil {
			return "", fmt.Errorf("must be a number")
		}
		out = v.String()
	case "boolean":
		v, ok := value.(bool)
		if !ok {
			return "", fmt.Errorf("must be a boolean")
		}
		out = strconv.FormatBool(v)
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return "", fmt.Errorf("must be an object")
		}
		encoded, _ := json.Marshal(value)
		out = string(encoded)
	case "array":
		if _, ok := value.([]any); !ok {
			return "", fmt.Errorf("must be an array")
		}
		encoded, _ := json.Marshal(value)
		out = string(encoded)
	default:
		return "", fmt.Errorf("has unsupported type %q", paramType)
	}
	return out, nil
}

func stringIn(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *Server) observeCall(r *http.Request, name string, args []string, resp egg.ToolResponse) {
	if s.observe != nil {
		s.observe(r, name, args, resp)
	}
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(resp)
}
