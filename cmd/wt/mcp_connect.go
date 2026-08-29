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
	in         io.Reader
	out        io.Writer
	actor      string
	tunnel     connectMCPTunnel
	timeout    time.Duration
	mu         sync.Mutex
	controls   map[string]*webrtcpkg.ControlClient
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
	callCtx, cancelCalls := context.WithCancel(ctx)
	defer cancelCalls()
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(s.out)
	var calls sync.WaitGroup
	requestSlots := make(chan struct{}, maxConcurrentConnectMCPCalls)
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
		select {
		case <-ctx.Done():
			cancelCalls()
			calls.Wait()
			return ctx.Err()
		default:
		}
		var request localMCPRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			write(localMCPResponse{JSONRPC: "2.0", Error: &localMCPError{Code: -32700, Message: "parse error"}})
			continue
		}
		dispatch := func() {
			response, respond := s.handle(callCtx, request)
			if respond {
				write(response)
			}
		}
		if request.Method == "tools/call" {
			select {
			case requestSlots <- struct{}{}:
			default:
				if len(request.ID) > 0 {
					write(localMCPResponse{
						JSONRPC: "2.0", ID: request.ID,
						Error: &localMCPError{Code: -32000, Message: "too many concurrent tool calls"},
					})
				}
				continue
			}
			calls.Add(1)
			go func() {
				defer calls.Done()
				defer func() { <-requestSlots }()
				dispatch()
			}()
		} else {
			dispatch()
		}
	}
	scanErr := scanner.Err()
	// The parent MCP process closing stdin is a transport disconnect. Cancel
	// outstanding waits/connection attempts without stopping durable wing work.
	cancelCalls()
	calls.Wait()
	if scanErr != nil {
		return fmt.Errorf("read MCP request: %w", scanErr)
	}
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

func (s *connectMCPServer) controlClient(ctx context.Context, wingID string) (*webrtcpkg.ControlClient, error) {
	for {
		s.mu.Lock()
		if existing := s.controls[wingID]; existing != nil {
			if !existing.Closed() {
				s.mu.Unlock()
				return existing, nil
			}
			delete(s.controls, wingID)
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
			if s.controls == nil {
				s.controls = make(map[string]*webrtcpkg.ControlClient)
			}
			s.controls[wingID] = client
		}
		close(attempt.done)
		s.mu.Unlock()
		return client, err
	}
}

func (s *connectMCPServer) establishControlClient(ctx context.Context, wingID string) (*webrtcpkg.ControlClient, error) {
	connectCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	wing, err := s.tunnel.DiscoverWing(connectCtx, wingID)
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
		closeWithLog("WebRTC client", client)
		return nil, err
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
