package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/ws"
	pionwebrtc "github.com/pion/webrtc/v4"
)

type ConnectMCPServer struct {
	protocol   mcpProtocolState
	Version    string
	In         io.Reader
	Out        io.Writer
	Actor      string
	Tunnel     connectMCPTunnel
	Timeout    time.Duration
	mu         sync.Mutex
	Controls   map[string]*webrtcpkg.ControlClient
	connecting map[string]*controlConnectAttempt
}

type controlConnectAttempt struct {
	done   chan struct{}
	client *webrtcpkg.ControlClient
	err    error
}

const maxConcurrentConnectMCPCalls = 64

type connectMCPTunnel interface {
	ListWings(ctx context.Context) ([]ws.WingInfo, error)
	DiscoverWing(ctx context.Context, wingID string) (*ws.WingInfo, error)
	Stream(ctx context.Context, wingID, wingPublicKey string, inner any, onChunk func([]byte) error) error
}

func (s *ConnectMCPServer) Serve(ctx context.Context) error {
	return serveStdio(ctx, s.In, s.Out, s.handle)
}

func (s *ConnectMCPServer) handle(ctx context.Context, request localMCPRequest) (localMCPResponse, bool) {
	response := localMCPResponse{JSONRPC: "2.0", ID: request.ID}
	if request.JSONRPC != "2.0" || request.Method == "" {
		response.Error = &localMCPError{Code: -32600, Message: "invalid request"}
		return response, len(request.ID) > 0
	}
	switch request.Method {
	case "initialize":
		version := s.protocol.negotiate(request.Params)
		response.Result = map[string]any{
			"protocolVersion": version,
			"capabilities":    mcpCapabilities(version),
			"serverInfo":      map[string]any{"name": "wingthing-agent-manager", "version": s.Version, "actor": s.Actor},
			"instructions":    "Wingthing manages durable agents across machines. Call wing_list, then pass an explicit wing_id to every wing-owned tool. Remote control travels directly to that wing; the roost is used for identity, directory, and signaling.",
		}
	case "notifications/initialized", "notifications/cancelled":
		return localMCPResponse{}, false
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": mcpVersionTools(control.Tools(control.SurfaceDirectMCP), s.protocol.tasksEnabled())}
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
		if len(call.Arguments) == 0 {
			call.Arguments = json.RawMessage(`{}`)
		}
		if call.Task != nil && s.protocol.tasksEnabled() {
			return handleMCPTaskRequest(ctx, request, s.callTaskControl, true), len(request.ID) > 0
		}
		result, isError, err := s.callTool(ctx, call.Name, call.Arguments)
		if err != nil {
			result = control.ErrorResult(err)
			isError = true
		}
		response.Result = localMCPToolResult(result, isError)
	case "tasks/get", "tasks/result", "tasks/list", "tasks/cancel":
		if s.protocol.tasksEnabled() {
			return handleMCPTaskRequest(ctx, request, s.callTaskControl, true), len(request.ID) > 0
		}
		response.Error = &localMCPError{Code: -32601, Message: "tasks require MCP 2025-11-25"}
	default:
		if len(request.ID) == 0 {
			return localMCPResponse{}, false
		}
		response.Error = &localMCPError{Code: -32601, Message: "method not found: " + request.Method}
	}
	return response, len(request.ID) > 0
}

func (s *ConnectMCPServer) callTool(ctx context.Context, name string, arguments json.RawMessage) (map[string]any, bool, error) {
	tool, ok := control.Lookup(name)
	if !ok || !tool.Supports(control.SurfaceDirectMCP) {
		return nil, true, fmt.Errorf("unknown direct MCP tool %q", name)
	}
	if tool.Authority == control.AuthorityPortal {
		if name != "wing_list" {
			return nil, true, fmt.Errorf("portal control handler unavailable for %q", name)
		}
		var empty struct{}
		if err := decodeStrict(arguments, &empty); err != nil {
			return nil, true, fmt.Errorf("wing_list arguments: %w", err)
		}
		wings, err := s.Tunnel.ListWings(ctx)
		if err != nil {
			return nil, true, err
		}
		entries := make([]map[string]any, 0, len(wings))
		for _, wing := range wings {
			hostedRelay := ws.HostedRelayDeny
			if ws.HostedRelayAllowed(wing.HostedRelay) {
				hostedRelay = ws.HostedRelayAllow
			}
			entry := map[string]any{
				"wing_id": wing.WingID, "public_key": wing.PublicKey,
				"owner": wing.Owner, "org_id": wing.OrgID, "online": true,
				"mcp_control": wing.PurposeBinding && wing.DirectMCP && !wing.Locked, "mcp_transport": "direct-webrtc", "hosted_relay": hostedRelay,
			}
			if !wing.PurposeBinding {
				entry["mcp_control_reason"] = "wing-upgrade-required"
			} else if !wing.DirectMCP {
				entry["mcp_control_reason"] = "wing-direct-control-disabled"
			} else if wing.Locked {
				entry["mcp_control_reason"] = "native-passkey-not-supported"
			}
			entries = append(entries, entry)
		}
		return map[string]any{"wings": entries, "count": len(entries), "control_scope": "qualified-direct"}, false, nil
	}
	wingID, forwarded, err := control.SplitWingTarget(arguments)
	if err != nil {
		return nil, true, err
	}
	client, err := s.controlClient(ctx, wingID)
	if err != nil {
		return nil, true, fmt.Errorf("direct connection to %s failed: %w; the native connector does not use the hosted relay—put both peers on the same LAN/tailnet, configure ICE, use SSH, or connect through a self-hosted roost", wingID, err)
	}
	result, isError, err := client.Call(ctx, name, forwarded)
	if err != nil {
		if client.Closed() {
			s.evictControl(wingID, client)
		}
		return nil, true, err
	}
	return control.QualifyResult(wingID, result), isError, nil
}

func (s *ConnectMCPServer) controlClient(ctx context.Context, wingID string) (*webrtcpkg.ControlClient, error) {
	for {
		s.mu.Lock()
		if existing := s.Controls[wingID]; existing != nil {
			if !existing.Closed() {
				s.mu.Unlock()
				return existing, nil
			}
			delete(s.Controls, wingID)
			s.mu.Unlock()
			_ = existing.Close()
			continue
		}
		if attempt := s.connecting[wingID]; attempt != nil {
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-attempt.done:
				return attempt.client, attempt.err
			}
		}
		if s.connecting == nil {
			s.connecting = make(map[string]*controlConnectAttempt)
		}
		attempt := &controlConnectAttempt{done: make(chan struct{})}
		s.connecting[wingID] = attempt
		s.mu.Unlock()

		client, err := s.establishControlClient(ctx, wingID)
		attempt.client = client
		attempt.err = err
		s.mu.Lock()
		if s.connecting[wingID] == attempt {
			delete(s.connecting, wingID)
		}
		if err == nil {
			if s.Controls == nil {
				s.Controls = make(map[string]*webrtcpkg.ControlClient)
			}
			s.Controls[wingID] = client
		}
		close(attempt.done)
		s.mu.Unlock()
		return client, err
	}
}

func (s *ConnectMCPServer) establishControlClient(ctx context.Context, wingID string) (*webrtcpkg.ControlClient, error) {
	connectCtx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	wing, err := s.Tunnel.DiscoverWing(connectCtx, wingID)
	if err != nil {
		return nil, err
	}
	if !wing.PurposeBinding {
		return nil, fmt.Errorf("wing does not advertise purpose-bound signaling; upgrade wt on the wing before using native direct MCP")
	}
	if !wing.DirectMCP {
		return nil, fmt.Errorf("wing does not have its WebRTC direct-control endpoint enabled; change connection_mode from direct or use that wing's configured direct endpoint")
	}
	if wing.Locked {
		return nil, fmt.Errorf("wing requires passkey authentication, which native direct MCP does not support in this release")
	}
	var wingDetails struct {
		ICEServers []config.ICEServer `json:"ice_servers"`
	}
	if err := s.Tunnel.Stream(connectCtx, wing.WingID, wing.PublicKey, map[string]any{
		"type": "wing.info",
	}, func(payload []byte) error { return json.Unmarshal(payload, &wingDetails) }); err != nil {
		return nil, fmt.Errorf("read direct connection metadata: %w", err)
	}
	iceServers := make([]pionwebrtc.ICEServer, 0, len(wingDetails.ICEServers))
	for _, server := range wingDetails.ICEServers {
		iceServers = append(iceServers, pionwebrtc.ICEServer{
			URLs: server.URLs, Username: server.Username, Credential: server.Credential,
		})
	}
	client, err := webrtcpkg.NewControlClient(s.Actor, iceServers)
	if err != nil {
		return nil, err
	}
	offer, err := client.Offer(connectCtx)
	if err == nil {
		var answer struct {
			SDP string `json:"sdp"`
		}
		err = s.Tunnel.Stream(connectCtx, wing.WingID, wing.PublicKey, map[string]any{
			"type": "webrtc.offer", "sdp": offer,
		}, func(payload []byte) error { return json.Unmarshal(payload, &answer) })
		if err == nil && answer.SDP == "" {
			err = fmt.Errorf("wing returned no WebRTC answer")
		}
		if err == nil {
			err = client.AcceptAnswer(answer.SDP)
		}
		if err == nil {
			err = client.WaitReady(connectCtx)
		}
	}
	if err != nil {
		cmdutil.CloseWithLog("WebRTC client", client)
		return nil, err
	}
	return client, nil
}

func (s *ConnectMCPServer) evictControl(wingID string, client *webrtcpkg.ControlClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Controls[wingID] == client {
		delete(s.Controls, wingID)
		_ = client.Close()
	}
}

func (s *ConnectMCPServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for wingID, client := range s.Controls {
		_ = client.Close()
		delete(s.Controls, wingID)
	}
}
