package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/wingconnect"
)

func prepareListFixture(t *testing.T) *runWingFixture {
	t.Helper()
	f := newRunWingFixture(t, nil)
	f.server.Cfg.WingID = "fixture-wing"
	if _, err := auth.EnsureKeyPair(f.server.Cfg.Dir); err != nil {
		t.Fatal(err)
	}
	return f
}

func inventoryIDs(t *testing.T, data map[string]any) []string {
	t.Helper()
	var page struct {
		Runs []struct {
			ID     string `json:"run_id"`
			WingID string `json:"wing_id"`
		}
	}
	wire, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(wire, &page); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, r := range page.Runs {
		if r.WingID != "fixture-wing" {
			t.Fatalf("unqualified retained row: %s", wire)
		}
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}

// Every fixture serializes an independent request and response. It exercises
// real dispatch and authority, with no socket, WebRTC or SSH process opened.
func serializedInventoryCall(t *testing.T, s *Server, tool string, args json.RawMessage) (map[string]any, bool, error) {
	t.Helper()
	request := control.DirectRequest{Version: control.ContractVersion, ID: "independent-wire-request", Tool: tool, Arguments: args}
	wire, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded control.DirectRequest
	if err = json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	response := s.handleDirectRequest(t.Context(), decoded)
	wire, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var got control.DirectResponse
	if err = json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	return got.Result, got.IsError, got.Err()
}

type inventoryMemoryConnection struct {
	server *Server
	t      *testing.T
	done   chan struct{}
}

func (c *inventoryMemoryConnection) Call(ctx context.Context, name string, args json.RawMessage) (map[string]any, bool, error) {
	return serializedInventoryCall(c.t, c.server, name, args)
}
func (c *inventoryMemoryConnection) Done() <-chan struct{} { return c.done }
func (c *inventoryMemoryConnection) Close() error          { return nil }

func TestAgentListSerializedAdaptersReturnSameDurableIdentities(t *testing.T) {
	f := prepareListFixture(t)
	expected := []string{}
	for _, prompt := range []string{"SECRET PROMPT A", "SECRET PROMPT B", "SECRET PROMPT C"} {
		id := f.admit(t, prompt)
		<-f.submitted
		expected = append(expected, id)
	}
	f.finish(t, expected[0], "SECRET RESULT", "")
	f.wait(t, expected[0])
	sort.Strings(expected)
	data, err := f.server.toolAgentList(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := inventoryIDs(t, data); !reflect.DeepEqual(got, expected) {
		t.Fatalf("runtime oracle: %v != %v", got, expected)
	}
	input := localMCPRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"agent_list","arguments":{}}`)}
	local, _ := f.server.handle(t.Context(), input)
	data = local.Result.(map[string]any)["structuredContent"].(map[string]any)
	if got := inventoryIDs(t, data); !reflect.DeepEqual(got, expected) {
		t.Fatalf("local: %v", got)
	}
	direct := *f.server
	direct.Surface = control.SurfaceDirectMCP
	direct.Grants = map[string]bool{"agent.read": true}
	data, denied, err := serializedInventoryCall(t, &direct, "agent_list", json.RawMessage(`{}`))
	if err != nil || denied || !reflect.DeepEqual(inventoryIDs(t, data), expected) {
		t.Fatalf("direct: %v %v", data, err)
	}
	clients := []byte("clients:\n  readclient:\n    owner: owner\n    grants: [agent.read]\n")
	if err = os.WriteFile(filepath.Join(f.server.Cfg.Dir, "clients.yaml"), clients, 0600); err != nil {
		t.Fatal(err)
	}
	hello := controlsocket.Hello{Client: "readclient"}
	resolved, err := resolveLocalWingClient("test", f.service, "fixture-user", NewMCPAdmissionState(), hello)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &localWingProxy{client: &controlsocket.Client{}, forwardTool: func(_ context.Context, name string, args json.RawMessage) (map[string]any, bool, error) {
		return serializedInventoryCall(t, resolved, name, args)
	}}
	socket, _ := proxy.handle(t.Context(), input)
	data = socket.Result.(map[string]any)["structuredContent"].(map[string]any)
	if got := inventoryIDs(t, data); !reflect.DeepEqual(got, expected) {
		t.Fatalf("socket authority/stdio: %v", got)
	}
	dir := t.TempDir()
	if err = config.SaveRemotes(dir, map[string]config.Remote{"fixture": {SSHTarget: "fixture", WingID: "fixture-wing", ControlVersion: control.ContractVersion, ControlSocket: "/fixture-unused", WingthingDir: "/fixture-state"}}); err != nil {
		t.Fatal(err)
	}
	p, err := wingconnect.New(t.Context(), wingconnect.Options{Dir: dir, LocalWingID: "coordinator", Hello: hello, Dial: func(_ context.Context, r config.Remote, h controlsocket.Hello) (wingconnect.Connection, error) {
		if h.Aggregate || r.WingID != "fixture-wing" {
			t.Error("lost receiving-wing boundary")
		}
		return &inventoryMemoryConnection{server: resolved, t: t, done: make(chan struct{})}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	remembered := &rememberedWings{wingID: "coordinator", pools: map[controlsocket.Hello]*wingconnect.Pool{hello: p}, ctx: t.Context()}
	coordinator := &Server{Cfg: &config.Config{Dir: dir}, Logs: io.Discard, Grants: map[string]bool{"agent.read": true}}
	response := remembered.dispatch(t.Context(), coordinator, hello, control.DirectRequest{Version: control.ContractVersion, ID: "ssh-shape", Tool: "agent_list", Arguments: json.RawMessage(`{"wing_id":"fixture-wing"}`)})
	if err = response.Err(); err != nil || response.IsError || !reflect.DeepEqual(inventoryIDs(t, response.Result), expected) {
		t.Fatalf("remembered: %+v %v", response, err)
	}
	wire, _ := json.Marshal(response)
	for _, secret := range []string{"SECRET", "provider_session_id", "containment_error", "\"prompt\"", "\"output\"", "\"usage\""} {
		if strings.Contains(string(wire), secret) {
			t.Fatalf("inventory leaked %s: %s", secret, wire)
		}
	}
	t.Logf("independent adapter oracle: %d identical IDs across local, direct, socket-authority proxy, remembered-SSH in-memory pool", len(expected))
}

func TestAgentListGrantBoundsAndUnsupportedErrors(t *testing.T) {
	f := prepareListFixture(t)
	restricted := *f.server
	restricted.Grants = map[string]bool{"terminal.read": true}
	data, denied, rpc := restricted.callTool(t.Context(), "agent_list", json.RawMessage(`{}`))
	if !denied || rpc != nil || data["runs"] != nil {
		t.Fatalf("grant bypass: %v %v", data, rpc)
	}
	for _, args := range []string{`{"limit":201}`, `{"limit":-1}`, `{"terminal_window_seconds":3599}`, `{"principal":"owner"}`, `{"cursor":"foreign"}`} {
		if _, err := f.server.toolAgentList(t.Context(), json.RawMessage(args)); err == nil {
			t.Fatalf("accepted bounds/authority %s", args)
		}
	}
	data, denied, err := serializedInventoryCall(t, f.server, "agent_list", json.RawMessage(`{"cursor":"foreign"}`))
	if !denied || !errors.Is(err, fs.ErrInvalid) || data["error_kind"] != string(control.ErrorInvalidArgument) {
		t.Fatalf("cursor typed wire: %v %v", data, err)
	}
	response := f.server.handleDirectRequest(t.Context(), control.DirectRequest{Version: "older", ID: "request", Tool: "agent_list", Arguments: json.RawMessage(`{}`)})
	wire, _ := json.Marshal(response)
	var old control.DirectResponse
	_ = json.Unmarshal(wire, &old)
	if !errors.Is(old.Err(), errors.ErrUnsupported) || old.Result != nil {
		t.Fatalf("unsupported is empty success: %+v", old)
	}
	// An old v1 peer has only text; it still reports a failure, never a roster.
	legacy := control.DirectResponse{Error: `unsupported v1 control operation "agent_list"; upgrade wt and the wing`}
	if legacy.Err() == nil {
		t.Fatal("old peer appeared supported")
	}
}

func TestRunInventoryUnacknowledgedAdmissionAndRestartFindsOriginalKey(t *testing.T) {
	f := prepareListFixture(t)
	receipt, err := f.server.toolAgentRun(json.RawMessage(`{"prompt":"lost receipt","agent":"claude","idempotency_key":"original-key"}`))
	if err != nil {
		t.Fatal(err)
	}
	<-f.submitted
	expected := receipt["run_id"]
	receipt = nil // coordinator forgets its entire ID list
	f.restart(t)
	data, err := f.server.toolAgentList(t.Context(), json.RawMessage(`{"idempotency_key":"original-key"}`))
	if err != nil || len(inventoryIDs(t, data)) != 1 || inventoryIDs(t, data)[0] != expected {
		t.Fatalf("lost admission: %v %v", data, err)
	}
	f.mu.Lock()
	admissions := len(f.eggs)
	f.mu.Unlock()
	if admissions != 1 {
		t.Fatalf("discovery relaunched work: %d", admissions)
	}
	select {
	case id := <-f.submitted:
		t.Fatalf("discovery resubmitted %s", id)
	default:
	}
}
