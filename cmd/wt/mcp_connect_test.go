package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	webrtcpkg "github.com/ehrlich-b/wingthing/internal/webrtc"
	"github.com/ehrlich-b/wingthing/internal/ws"
	pionwebrtc "github.com/pion/webrtc/v4"
)

type directConnectorTestWing struct {
	info      ws.WingInfo
	manager   *webrtcpkg.PeerManager
	cfg       *config.Config
	wingCfg   *config.WingConfig
	admission *mcpAdmissionState
}

type directConnectorTestTunnel struct {
	wings map[string]*directConnectorTestWing
	mu    sync.Mutex
	seen  []string
}

func newDirectConnectorTestTunnel(t *testing.T) *directConnectorTestTunnel {
	t.Helper()
	tunnel := &directConnectorTestTunnel{wings: map[string]*directConnectorTestWing{}}
	for _, wingID := range []string{"office", "home"} {
		wing := &directConnectorTestWing{
			info:      ws.WingInfo{WingID: wingID, PublicKey: wingID + "-public-key", HostedRelay: ws.HostedRelayDeny},
			manager:   webrtcpkg.NewPeerManager(nil),
			cfg:       &config.Config{Dir: t.TempDir(), DefaultAgent: "claude", WingID: wingID},
			wingCfg:   &config.WingConfig{WingID: wingID, HostedRelay: config.HostedRelayDeny},
			admission: newMCPAdmissionState(),
		}
		wing.manager.OnDC(func(_ string, _ string, identity webrtcpkg.PeerIdentity, dc *pionwebrtc.DataChannel) {
			serveDirectMCPChannel(wing.cfg, wing.wingCfg, wing.cfg.Dir, false, nil, wing.admission, identity, dc)
		})
		t.Cleanup(wing.manager.Close)
		tunnel.wings[wingID] = wing
	}
	return tunnel
}

func (tunnel *directConnectorTestTunnel) ListWings(context.Context) ([]ws.WingInfo, error) {
	wingIDs := make([]string, 0, len(tunnel.wings))
	for wingID := range tunnel.wings {
		wingIDs = append(wingIDs, wingID)
	}
	sort.Strings(wingIDs)
	wings := make([]ws.WingInfo, 0, len(wingIDs))
	for _, wingID := range wingIDs {
		wings = append(wings, tunnel.wings[wingID].info)
	}
	return wings, nil
}

func (tunnel *directConnectorTestTunnel) DiscoverWing(_ context.Context, wingID string) (*ws.WingInfo, error) {
	wing := tunnel.wings[wingID]
	if wing == nil {
		return nil, fmt.Errorf("wing %s not found", wingID)
	}
	info := wing.info
	return &info, nil
}

func (tunnel *directConnectorTestTunnel) Stream(_ context.Context, wingID, _ string, inner any, onChunk func([]byte) error) error {
	wing := tunnel.wings[wingID]
	if wing == nil {
		return fmt.Errorf("wing %s not found", wingID)
	}
	payload, err := json.Marshal(inner)
	if err != nil {
		return err
	}
	var request struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return err
	}
	tunnel.mu.Lock()
	tunnel.seen = append(tunnel.seen, request.Type)
	tunnel.mu.Unlock()
	switch request.Type {
	case "wing.info":
		return onChunk([]byte(`{"ice_servers":[],"hosted_relay":"deny"}`))
	case "webrtc.offer":
		answer, err := wing.manager.HandleOffer(
			"native-client-public-key", "owner-user", "owner@example.com", "owner", nil, request.SDP,
		)
		if err != nil {
			return err
		}
		response, _ := json.Marshal(map[string]string{"sdp": answer})
		return onChunk(response)
	default:
		return fmt.Errorf("unexpected coordinator payload type %q", request.Type)
	}
}

func (tunnel *directConnectorTestTunnel) observedTypes() []string {
	tunnel.mu.Lock()
	defer tunnel.mu.Unlock()
	return append([]string(nil), tunnel.seen...)
}

type connectMCPStdioHarness struct {
	t      *testing.T
	ctx    context.Context
	cancel context.CancelFunc
	input  *io.PipeWriter
	output *json.Decoder
	done   <-chan error
	nextID int
	server *connectMCPServer
}

func newConnectMCPStdioHarness(t *testing.T, tunnel connectMCPTunnel) *connectMCPStdioHarness {
	t.Helper()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	server := &connectMCPServer{
		in: inputReader, out: outputWriter, actor: "codex", tunnel: tunnel,
		timeout: 10 * time.Second, controls: map[string]*webrtcpkg.ControlClient{},
	}
	done := make(chan error, 1)
	go func() {
		done <- server.serve(ctx)
		_ = outputWriter.Close()
	}()
	return &connectMCPStdioHarness{
		t: t, ctx: ctx, cancel: cancel, input: inputWriter,
		output: json.NewDecoder(outputReader), done: done, nextID: 1, server: server,
	}
}

func (h *connectMCPStdioHarness) tool(name string, arguments map[string]any) map[string]any {
	h.t.Helper()
	id := h.nextID
	h.nextID++
	request := map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments},
	}
	if err := json.NewEncoder(h.input).Encode(request); err != nil {
		h.t.Fatal(err)
	}
	var response struct {
		Error  *localMCPError `json:"error"`
		Result struct {
			Structured map[string]any `json:"structuredContent"`
			IsError    bool           `json:"isError"`
		} `json:"result"`
	}
	if err := h.output.Decode(&response); err != nil {
		h.t.Fatal(err)
	}
	if response.Error != nil {
		h.t.Fatalf("%s JSON-RPC error: %#v", name, response.Error)
	}
	response.Result.Structured["_is_error"] = response.Result.IsError
	return response.Result.Structured
}

func (h *connectMCPStdioHarness) close() {
	h.t.Helper()
	_ = h.input.Close()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Fatalf("connector serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		h.t.Fatal("connector did not stop after stdin closed")
	}
	h.server.close()
	h.cancel()
}

func TestConnectMCPStdioRoutesTwoWingsDirectlyAndPersistsAcrossReconnect(t *testing.T) {
	tunnel := newDirectConnectorTestTunnel(t)
	connector := newConnectMCPStdioHarness(t, tunnel)

	listed := connector.tool("wing_list", map[string]any{})
	if listed["count"] != float64(2) {
		t.Fatalf("wing inventory = %#v", listed)
	}
	wingIDs := []string{}
	for _, raw := range listed["wings"].([]any) {
		entry := raw.(map[string]any)
		wingIDs = append(wingIDs, entry["wing_id"].(string))
		if entry["hosted_relay"] != ws.HostedRelayDeny || entry["mcp_transport"] != "direct-webrtc" {
			t.Fatalf("wing transport metadata = %#v", entry)
		}
	}
	if !reflect.DeepEqual(wingIDs, []string{"home", "office"}) {
		t.Fatalf("wing IDs = %#v", wingIDs)
	}

	missingTarget := connector.tool("message_list", map[string]any{})
	if missingTarget["_is_error"] != true || missingTarget["error"] != "wing_id is required" {
		t.Fatalf("missing target result = %#v", missingTarget)
	}

	sent := connector.tool("message_send", map[string]any{
		"wing_id": "office", "content": "office-only durable state", "kind": "evidence",
	})
	if sent["_is_error"] != false || sent["wing_id"] != "office" {
		t.Fatalf("message_send result = %#v", sent)
	}
	office := connector.tool("message_list", map[string]any{"wing_id": "office", "include_sent": true})
	home := connector.tool("message_list", map[string]any{"wing_id": "home", "include_sent": true})
	if len(office["messages"].([]any)) != 1 || len(home["messages"].([]any)) != 0 {
		t.Fatalf("cross-wing state leaked: office=%#v home=%#v", office, home)
	}
	if office["wing_id"] != "office" || home["wing_id"] != "home" {
		t.Fatalf("results are not qualified: office=%#v home=%#v", office, home)
	}
	connector.close()

	// A new MCP process establishes a fresh data channel, then discovers state
	// held by the wing rather than by the previous connector process.
	reconnected := newConnectMCPStdioHarness(t, tunnel)
	defer reconnected.close()
	afterReconnect := reconnected.tool("message_list", map[string]any{"wing_id": "office", "include_sent": true})
	messages := afterReconnect["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["content"] != "office-only durable state" {
		t.Fatalf("durable state after reconnect = %#v", afterReconnect)
	}

	for _, observed := range tunnel.observedTypes() {
		if observed != "wing.info" && observed != "webrtc.offer" {
			t.Fatalf("coordinator observed direct MCP payload type %q", observed)
		}
	}
}
