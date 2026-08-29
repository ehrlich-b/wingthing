package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
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

type blockingConnectMCPTunnel struct {
	started chan struct{}
	release chan struct{}
}

type blockingDiscoverTunnel struct {
	mu      sync.Mutex
	count   int
	started chan struct{}
	release chan struct{}
}

type disconnectAwareConnectTunnel struct {
	started  chan struct{}
	canceled chan struct{}
}

func (t *disconnectAwareConnectTunnel) ListWings(ctx context.Context) ([]ws.WingInfo, error) {
	close(t.started)
	<-ctx.Done()
	close(t.canceled)
	return nil, ctx.Err()
}

func (*disconnectAwareConnectTunnel) DiscoverWing(context.Context, string) (*ws.WingInfo, error) {
	return nil, fmt.Errorf("unused")
}

func (*disconnectAwareConnectTunnel) Stream(context.Context, string, string, any, func([]byte) error) error {
	return fmt.Errorf("unused")
}

func (*blockingDiscoverTunnel) ListWings(context.Context) ([]ws.WingInfo, error) {
	return nil, fmt.Errorf("unused")
}

func (t *blockingDiscoverTunnel) DiscoverWing(ctx context.Context, _ string) (*ws.WingInfo, error) {
	t.mu.Lock()
	t.count++
	if t.count == 1 {
		close(t.started)
	}
	t.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.release:
		return nil, fmt.Errorf("planned discovery failure")
	}
}

func (*blockingDiscoverTunnel) Stream(context.Context, string, string, any, func([]byte) error) error {
	return fmt.Errorf("unused")
}

func (t *blockingDiscoverTunnel) discoveryCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

func (t *blockingConnectMCPTunnel) ListWings(context.Context) ([]ws.WingInfo, error) {
	t.started <- struct{}{}
	<-t.release
	return nil, nil
}

func (*blockingConnectMCPTunnel) DiscoverWing(context.Context, string) (*ws.WingInfo, error) {
	return nil, fmt.Errorf("unused")
}

func (*blockingConnectMCPTunnel) Stream(context.Context, string, string, any, func([]byte) error) error {
	return fmt.Errorf("unused")
}

func newDirectConnectorTestTunnel(t *testing.T) *directConnectorTestTunnel {
	t.Helper()
	tunnel := &directConnectorTestTunnel{wings: map[string]*directConnectorTestWing{}}
	for _, wingID := range []string{"office", "home"} {
		wing := &directConnectorTestWing{
			info:      ws.WingInfo{WingID: wingID, PublicKey: wingID + "-public-key", PurposeBinding: true, DirectMCP: true, HostedRelay: ws.HostedRelayDeny},
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
		"params": map[string]any{
			"name": name, "arguments": arguments,
			"_meta": map[string]any{"progressToken": fmt.Sprintf("claude-%d", id)},
		},
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
		if entry["mcp_control"] != true || entry["mcp_control_reason"] != nil {
			t.Fatalf("wing control capability = %#v", entry)
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
	if got := office["messages"].([]any)[0].(map[string]any)["wing_id"]; got != "office" {
		t.Fatalf("nested message wing_id = %#v; result=%#v", got, office)
	}
	unreachable := connector.tool("message_list", map[string]any{"wing_id": "missing", "include_sent": true})
	errText, _ := unreachable["error"].(string)
	if unreachable["_is_error"] != true || !strings.Contains(errText, "native connector does not use the hosted relay") || strings.Contains(errText, "enable Pro relay") {
		t.Fatalf("unreachable-wing remediation is misleading: %#v", unreachable)
	}
	connector.close()

	// A new MCP process establishes a fresh data channel, then discovers state
	// held by the wing rather than by the previous connector process.
	reconnected := newConnectMCPStdioHarness(t, tunnel)
	defer reconnected.close()
	afterReconnect := reconnected.tool("message_list", map[string]any{"wing_id": "office", "include_sent": true})
	messages := afterReconnect["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["content"] != "office-only durable state" || messages[0].(map[string]any)["wing_id"] != "office" {
		t.Fatalf("durable state after reconnect = %#v", afterReconnect)
	}

	for _, observed := range tunnel.observedTypes() {
		if observed != "wing.info" && observed != "webrtc.offer" {
			t.Fatalf("coordinator observed direct MCP payload type %q", observed)
		}
	}
}

func TestConnectMCPStdioBoundsConcurrentToolCalls(t *testing.T) {
	tunnel := &blockingConnectMCPTunnel{
		started: make(chan struct{}, maxConcurrentConnectMCPCalls),
		release: make(chan struct{}),
	}
	var input strings.Builder
	for id := 1; id <= maxConcurrentConnectMCPCalls+1; id++ {
		fmt.Fprintf(&input, `{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"wing_list","arguments":{}}}`+"\n", id)
	}
	var output strings.Builder
	server := &connectMCPServer{
		in: strings.NewReader(input.String()), out: &output, actor: "test", tunnel: tunnel,
		controls: map[string]*webrtcpkg.ControlClient{},
	}
	done := make(chan error, 1)
	go func() { done <- server.serve(context.Background()) }()
	for index := 0; index < maxConcurrentConnectMCPCalls; index++ {
		select {
		case <-tunnel.started:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d tool calls reached the bounded worker set", index)
		}
	}
	close(tunnel.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded connector did not drain")
	}

	decoder := json.NewDecoder(strings.NewReader(output.String()))
	overloaded := false
	responses := 0
	for {
		var response localMCPResponse
		if err := decoder.Decode(&response); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		responses++
		if string(response.ID) == fmt.Sprintf("%d", maxConcurrentConnectMCPCalls+1) && response.Error != nil && strings.Contains(response.Error.Message, "too many") {
			overloaded = true
		}
	}
	if responses != maxConcurrentConnectMCPCalls+1 || !overloaded {
		t.Fatalf("responses=%d overloaded=%v output=%s", responses, overloaded, output.String())
	}
}

func TestConnectMCPStdioEOFCancelsOutstandingRemoteWait(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	tunnel := &disconnectAwareConnectTunnel{started: make(chan struct{}), canceled: make(chan struct{})}
	var output strings.Builder
	server := &connectMCPServer{
		in: inputReader, out: &output, actor: "test", tunnel: tunnel,
		controls: map[string]*webrtcpkg.ControlClient{},
	}
	done := make(chan error, 1)
	go func() { done <- server.serve(context.Background()) }()
	if _, err := io.WriteString(inputWriter, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wing_list","arguments":{}}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tunnel.started:
	case <-time.After(time.Second):
		t.Fatal("remote wait did not start")
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tunnel.canceled:
	case <-time.After(time.Second):
		t.Fatal("stdin EOF did not cancel the outstanding remote wait")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("connector did not exit after canceling its outstanding call")
	}
}

func TestConnectMCPCoalescesConcurrentSetupForOneWing(t *testing.T) {
	tunnel := &blockingDiscoverTunnel{started: make(chan struct{}), release: make(chan struct{})}
	server := &connectMCPServer{
		actor: "test", tunnel: tunnel, timeout: 2 * time.Second,
		controls: make(map[string]*webrtcpkg.ControlClient),
	}
	leaderDone := make(chan error, 1)
	go func() {
		_, err := server.controlClient(context.Background(), "office")
		leaderDone <- err
	}()
	select {
	case <-tunnel.started:
	case <-time.After(time.Second):
		t.Fatal("connection leader did not start discovery")
	}

	// Every follower has its own bounded wait, but none may start another
	// offer for the same wing while the leader is still negotiating.
	for index := range 8 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err := server.controlClient(ctx, "office")
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("follower %d error = %v, want deadline", index, err)
		}
	}
	if got := tunnel.discoveryCount(); got != 1 {
		t.Fatalf("same-wing discovery attempts = %d, want 1", got)
	}

	close(tunnel.release)
	select {
	case err := <-leaderDone:
		if err == nil || !strings.Contains(err.Error(), "planned discovery failure") {
			t.Fatalf("leader error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("connection leader did not finish")
	}

	// A completed failure must leave no poisoned in-flight entry; a later call
	// gets a fresh attempt.
	_, err := server.controlClient(context.Background(), "office")
	if err == nil || !strings.Contains(err.Error(), "planned discovery failure") {
		t.Fatalf("retry error = %v", err)
	}
	if got := tunnel.discoveryCount(); got != 2 {
		t.Fatalf("discovery attempts after retry = %d, want 2", got)
	}
}

func TestConnectMCPReportsLegacyWingAsUpgradeRequired(t *testing.T) {
	tunnel := newDirectConnectorTestTunnel(t)
	legacy := tunnel.wings["office"]
	legacy.info.PurposeBinding = false
	connector := newConnectMCPStdioHarness(t, tunnel)
	defer connector.close()

	listed := connector.tool("wing_list", map[string]any{})
	var office map[string]any
	for _, raw := range listed["wings"].([]any) {
		entry := raw.(map[string]any)
		if entry["wing_id"] == "office" {
			office = entry
		}
	}
	if office == nil || office["mcp_control"] != false || office["mcp_control_reason"] != "wing-upgrade-required" {
		t.Fatalf("legacy wing capability = %#v", office)
	}

	result := connector.tool("message_list", map[string]any{"wing_id": "office"})
	errText, _ := result["error"].(string)
	if result["_is_error"] != true || !strings.Contains(errText, "upgrade wt on the wing") {
		t.Fatalf("legacy wing control result = %#v", result)
	}
	if got := tunnel.observedTypes(); len(got) != 0 {
		t.Fatalf("legacy wing should fail before signaling, observed %#v", got)
	}
}

func TestDirectMCPRequestSlotsAreBounded(t *testing.T) {
	slots := make(chan struct{}, maxConcurrentDirectMCPRequests)
	for i := 0; i < maxConcurrentDirectMCPRequests; i++ {
		if !acquireDirectMCPRequestSlot(slots) {
			t.Fatalf("slot %d unexpectedly rejected", i)
		}
	}
	if acquireDirectMCPRequestSlot(slots) {
		t.Fatal("request beyond direct MCP concurrency bound was admitted")
	}
	<-slots
	if !acquireDirectMCPRequestSlot(slots) {
		t.Fatal("released direct MCP request slot was not reusable")
	}
}

func TestConnectMCPReportsConfiguredDirectEndpointWithoutWebRTC(t *testing.T) {
	tunnel := newDirectConnectorTestTunnel(t)
	direct := tunnel.wings["office"]
	direct.info.DirectMCP = false
	connector := newConnectMCPStdioHarness(t, tunnel)
	defer connector.close()

	listed := connector.tool("wing_list", map[string]any{})
	for _, raw := range listed["wings"].([]any) {
		entry := raw.(map[string]any)
		if entry["wing_id"] == "office" && (entry["mcp_control"] != false || entry["mcp_control_reason"] != "wing-direct-control-disabled") {
			t.Fatalf("configured direct wing capability = %#v", entry)
		}
	}
	result := connector.tool("message_list", map[string]any{"wing_id": "office"})
	errText, _ := result["error"].(string)
	if result["_is_error"] != true || !strings.Contains(errText, "WebRTC direct-control endpoint") {
		t.Fatalf("configured direct wing result = %#v", result)
	}
	if got := tunnel.observedTypes(); len(got) != 0 {
		t.Fatalf("disabled direct MCP should fail before signaling, observed %#v", got)
	}
}

func TestConnectMCPReportsLockedWingPasskeyLimitation(t *testing.T) {
	tunnel := newDirectConnectorTestTunnel(t)
	locked := tunnel.wings["office"]
	locked.info.Locked = true
	connector := newConnectMCPStdioHarness(t, tunnel)
	defer connector.close()

	listed := connector.tool("wing_list", map[string]any{})
	for _, raw := range listed["wings"].([]any) {
		entry := raw.(map[string]any)
		if entry["wing_id"] == "office" && (entry["mcp_control"] != false || entry["mcp_control_reason"] != "native-passkey-not-supported") {
			t.Fatalf("locked wing capability = %#v", entry)
		}
	}
	result := connector.tool("message_list", map[string]any{"wing_id": "office"})
	errText, _ := result["error"].(string)
	if result["_is_error"] != true || !strings.Contains(errText, "requires passkey authentication") {
		t.Fatalf("locked wing result = %#v", result)
	}
	if got := tunnel.observedTypes(); len(got) != 0 {
		t.Fatalf("locked wing should fail before signaling, observed %#v", got)
	}
}
