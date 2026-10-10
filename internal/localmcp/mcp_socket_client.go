package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
)

// ServeLocalWingClient owns only the MCP transport. All authority and execution
// belong to the independently running wing selected by its state directory.
func ServeLocalWingClient(ctx context.Context, version, dir, clientName, conversation string, unsandboxed bool, in io.Reader, out io.Writer) error {
	client, err := controlsocket.Dial(ctx, dir, controlsocket.Hello{Client: clientName, Conversation: conversation, Unsandboxed: unsandboxed})
	if err != nil {
		return err
	}
	defer client.Close()
	proxy := &localWingProxy{version: version, client: client}
	return serveStdio(ctx, in, out, proxy.handle)
}

type localWingProxy struct {
	version        string
	client         *controlsocket.Client
	onForwardError func(error)
}

func (s *localWingProxy) handle(ctx context.Context, request localMCPRequest) (localMCPResponse, bool) {
	response := localMCPResponse{JSONRPC: "2.0", ID: request.ID}
	if request.JSONRPC != "2.0" || request.Method == "" {
		response.Error = &localMCPError{Code: -32600, Message: "invalid request"}
		return response, len(request.ID) > 0
	}
	switch request.Method {
	case "initialize":
		instructions := "Wingthing manages persistent sessions and bounded agent runs in your local wing. Use terminal tools for PTYs and agent_run for supervised semantic work. Accepted work survives this MCP client exiting."
		if s.client.Welcome.Isolation == "outer-boundary" {
			instructions += " This wing trusts the outer VM/container boundary; spawned processes have the full authority of its OS user."
		}
		response.Result = map[string]any{
			"protocolVersion": localMCPProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "wingthing-local", "version": s.version, "principal": s.client.Welcome.Principal, "actor": s.client.Welcome.Actor},
			"instructions":    instructions,
		}
	case "notifications/initialized", "notifications/cancelled":
		return localMCPResponse{}, false
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		tools := control.Tools(control.SurfaceLocalMCP)
		if grants := s.client.Welcome.Grants; grants != nil || s.client.Welcome.Tools != nil {
			filtered := tools[:0]
			for _, tool := range tools {
				if (grants == nil || grants[tool.Grant]) && (s.client.Welcome.Tools == nil || s.client.Welcome.Tools[tool.Name]) {
					filtered = append(filtered, tool)
				}
			}
			tools = filtered
		}
		response.Result = map[string]any{"tools": tools}
	case "tools/call":
		var call localMCPToolCallParams
		if err := decodeStrict(request.Params, &call); err != nil || call.Name == "" {
			message := "name is required"
			if err != nil {
				message = err.Error()
			}
			response.Error = &localMCPError{Code: -32602, Message: "invalid tools/call params: " + message}
			break
		}
		tool, known := control.Lookup(call.Name)
		if !known || !tool.Supports(control.SurfaceLocalMCP) {
			response.Error = &localMCPError{Code: -32602, Message: "unknown tool: " + call.Name}
			break
		}
		arguments, err := shapeLocalArguments(tool, call.Arguments)
		var data map[string]any
		isError := false
		if err == nil {
			data, isError, err = s.client.Call(ctx, call.Name, arguments)
			if err != nil && s.onForwardError != nil {
				s.onForwardError(err)
			}
		}
		if err != nil {
			data = map[string]any{"error": err.Error()}
			isError = true
		}
		response.Result = localMCPToolResult(data, isError)
	default:
		if len(request.ID) == 0 {
			return localMCPResponse{}, false
		}
		response.Error = &localMCPError{Code: -32601, Message: "method not found: " + request.Method}
	}
	return response, len(request.ID) > 0
}

func shapeLocalArguments(tool control.Tool, arguments json.RawMessage) (json.RawMessage, error) {
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	properties, _ := tool.InputSchema["properties"].(map[string]any)
	if _, hasCWD := properties["cwd"]; !hasCWD {
		return arguments, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("tool arguments must be an object")
	}
	var cwd string
	if value, exists := fields["cwd"]; exists {
		if err := json.Unmarshal(value, &cwd); err != nil {
			return nil, fmt.Errorf("cwd must be a string")
		}
	}
	// This is argument shaping, not admission. The wing checks existence and policy.
	resolved, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	fields["cwd"], _ = json.Marshal(resolved)
	return json.Marshal(fields)
}

// CallLocalWingTool also lets CLI helpers use the wing's authority and runtime.
func CallLocalWingTool(ctx context.Context, dir, clientName, name string, arguments json.RawMessage) (map[string]any, error) {
	client, err := controlsocket.Dial(ctx, dir, controlsocket.Hello{Client: clientName})
	if err != nil {
		return nil, err
	}
	defer client.Close()
	result, isError, err := client.Call(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
	if isError {
		return nil, fmt.Errorf("%v", result["error"])
	}
	return result, nil
}
