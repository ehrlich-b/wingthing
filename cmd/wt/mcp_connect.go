package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/ws"
	pionwebrtc "github.com/pion/webrtc/v4"
	"github.com/spf13/cobra"
)

type connectMCPServer struct {
	in       io.Reader
	out      io.Writer
	actor    string
	tunnel   connectMCPTunnel
	timeout  time.Duration
	mu       sync.Mutex
	controls map[string]*webrtcpkg.ControlClient
}

type connectMCPTunnel interface {
	ListWings(ctx context.Context) ([]ws.WingInfo, error)
	DiscoverWing(ctx context.Context, wingID string) (*ws.WingInfo, error)
	Stream(ctx context.Context, wingID, wingPublicKey string, inner any, onChunk func([]byte) error) error
}

func connectMCPCmd() *cobra.Command {
	var clientName string
	var roost string
	var connectTimeout time.Duration
	command := &cobra.Command{
		Use:   "connect",
		Short: "Manage agents on remote wings over direct encrypted connections",
		Long: "Run one local MCP server for every accessible wing. Wingthing uses the roost for " +
			"identity, inventory, and WebRTC signaling; control payloads go directly to the selected wing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			actor := strings.TrimSpace(clientName)
			if actor == "" {
				actor = strings.TrimSpace(os.Getenv("WT_MCP_CLIENT"))
			}
			if actor == "" {
				actor = "default"
			}
			if err := validateSessionName(actor); err != nil {
				return fmt.Errorf("invalid MCP client name: %w", err)
			}
			tokenStore := auth.NewTokenStore(cfg.Dir)
			token, err := tokenStore.Load()
			if err != nil || !tokenStore.IsValid(token) {
				return fmt.Errorf("not logged in — run: wt login --roost <url>")
			}
			privateKey, err := auth.LoadPrivateKey(cfg.Dir)
			if err != nil {
				return fmt.Errorf("load native client key: %w", err)
			}
			server := &connectMCPServer{
				in: os.Stdin, out: os.Stdout, actor: actor, timeout: connectTimeout,
				tunnel: &ws.TunnelClient{
					RelayURL: finderRelayURL(cfg, roost), DeviceToken: token.Token,
					PrivKey: privateKey, KnownWingsPath: filepath.Join(cfg.Dir, "known_wings.json"),
				},
				controls: make(map[string]*webrtcpkg.ControlClient),
			}
			defer server.close()
			return server.serve(cmd.Context())
		},
	}
	command.Flags().StringVar(&clientName, "client", "", "MCP actor name used for attribution (or WT_MCP_CLIENT)")
	command.Flags().StringVar(&roost, "roost", "", "coordination roost URL (default: config or wingthing.ai)")
	command.Flags().DurationVar(&connectTimeout, "connect-timeout", 15*time.Second, "deadline for establishing each direct wing connection")
	return command
}

func (s *connectMCPServer) serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(s.out)
	var calls sync.WaitGroup
	var encodeMu sync.Mutex
	var encodeErr error
	write := func(response localMCPResponse) {
		encodeMu.Lock()
		defer encodeMu.Unlock()
		if encodeErr == nil {
			encodeErr = encoder.Encode(response)
		}
	}
	for scanner.Scan() {
		var request localMCPRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			write(localMCPResponse{JSONRPC: "2.0", Error: &localMCPError{Code: -32700, Message: "parse error"}})
			continue
		}
		dispatch := func() {
			response, respond := s.handle(ctx, request)
			if respond {
				write(response)
			}
		}
		if request.Method == "tools/call" {
			calls.Add(1)
			go func() { defer calls.Done(); dispatch() }()
		} else {
			dispatch()
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP request: %w", err)
	}
	calls.Wait()
	return encodeErr
}

func (s *connectMCPServer) handle(ctx context.Context, request localMCPRequest) (localMCPResponse, bool) {
	response := localMCPResponse{JSONRPC: "2.0", ID: request.ID}
	if request.JSONRPC != "2.0" || request.Method == "" {
		response.Error = &localMCPError{Code: -32600, Message: "invalid request"}
		return response, len(request.ID) > 0
	}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": localMCPProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "wingthing-agent-manager", "version": version, "actor": s.actor},
			"instructions":    "Wingthing manages durable agents across machines. Call wing_list, then pass an explicit wing_id to every wing-owned tool. Remote control travels directly to that wing; the roost is used for identity, directory, and signaling.",
		}
	case "notifications/initialized", "notifications/cancelled":
		return localMCPResponse{}, false
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		response.Result = map[string]any{"tools": control.Tools(control.SurfaceDirectMCP)}
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
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
		result, isError, err := s.callTool(ctx, call.Name, call.Arguments)
		if err != nil {
			result = map[string]any{"error": err.Error()}
			isError = true
		}
		response.Result = localMCPToolResult(result, isError)
	default:
		if len(request.ID) == 0 {
			return localMCPResponse{}, false
		}
		response.Error = &localMCPError{Code: -32601, Message: "method not found: " + request.Method}
	}
	return response, len(request.ID) > 0
}

func (s *connectMCPServer) callTool(ctx context.Context, name string, arguments json.RawMessage) (map[string]any, bool, error) {
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
		wings, err := s.tunnel.ListWings(ctx)
		if err != nil {
			return nil, true, err
		}
		entries := make([]map[string]any, 0, len(wings))
		for _, wing := range wings {
			hostedRelay := ws.HostedRelayDeny
			if ws.HostedRelayAllowed(wing.HostedRelay) {
				hostedRelay = ws.HostedRelayAllow
			}
			entries = append(entries, map[string]any{
				"wing_id": wing.WingID, "public_key": wing.PublicKey,
				"owner": wing.Owner, "org_id": wing.OrgID, "online": true,
				"mcp_control": true, "mcp_transport": "direct-webrtc", "hosted_relay": hostedRelay,
			})
		}
		return map[string]any{"wings": entries, "count": len(entries), "control_scope": "qualified-direct"}, false, nil
	}
	wingID, forwarded, err := control.SplitWingTarget(arguments)
	if err != nil {
		return nil, true, err
	}
	client, err := s.controlClient(ctx, wingID)
	if err != nil {
		return nil, true, fmt.Errorf("direct connection to %s failed: %w; put both peers on the same LAN/tailnet, configure ICE, use SSH or a self-hosted roost, or enable Pro relay", wingID, err)
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

func (s *connectMCPServer) controlClient(ctx context.Context, wingID string) (*webrtcpkg.ControlClient, error) {
	s.mu.Lock()
	if existing := s.controls[wingID]; existing != nil {
		if !existing.Closed() {
			s.mu.Unlock()
			return existing, nil
		}
		delete(s.controls, wingID)
		s.mu.Unlock()
		_ = existing.Close()
	} else {
		s.mu.Unlock()
	}
	connectCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	wing, err := s.tunnel.DiscoverWing(connectCtx, wingID)
	if err != nil {
		return nil, err
	}
	var wingDetails struct {
		ICEServers []config.ICEServer `json:"ice_servers"`
	}
	if err := s.tunnel.Stream(connectCtx, wing.WingID, wing.PublicKey, map[string]any{
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
	client, err := webrtcpkg.NewControlClient(s.actor, iceServers)
	if err != nil {
		return nil, err
	}
	offer, err := client.Offer(connectCtx)
	if err == nil {
		var answer struct {
			SDP string `json:"sdp"`
		}
		err = s.tunnel.Stream(connectCtx, wing.WingID, wing.PublicKey, map[string]any{
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
		client.Close()
		return nil, err
	}
	// Concurrent calls to different wings establish independently. If two
	// first calls race for the same wing, keep the first ready transport and
	// discard the redundant connection without replaying either operation.
	s.mu.Lock()
	if existing := s.controls[wingID]; existing != nil && !existing.Closed() {
		s.mu.Unlock()
		_ = client.Close()
		return existing, nil
	}
	replaced := s.controls[wingID]
	s.controls[wingID] = client
	s.mu.Unlock()
	if replaced != nil {
		_ = replaced.Close()
	}
	return client, nil
}

func (s *connectMCPServer) evictControl(wingID string, client *webrtcpkg.ControlClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.controls[wingID] == client {
		delete(s.controls, wingID)
		_ = client.Close()
	}
}

func (s *connectMCPServer) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for wingID, client := range s.controls {
		_ = client.Close()
		delete(s.controls, wingID)
	}
}
