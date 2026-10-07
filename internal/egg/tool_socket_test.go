package egg

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestToolCapabilityReclaimRequiresEggAuthenticationAndPreservesAuthority(t *testing.T) {
	path := shortSockPath(t)
	tools := []*config.ToolConfig{{Name: "echo", Run: "printf restored"}, {Name: "context", Context: "jira-search"}}
	first, err := NewToolListener(path, tools)
	if err != nil {
		t.Fatal(err)
	}
	secret := first.capability
	closeToolListenerForTest(t, first)
	dir := shortEndpointTempDir(t)
	server := &Server{dir: dir, token: "host-only-token", toolCapability: secret,
		session: &Session{ID: "fixture", StartedAt: time.Now(), replay: newReplayBuffer("claude")}}
	endpoint, err := server.prepareEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	defer server.closeEndpoint(endpoint)
	rpc := grpc.NewServer(grpc.UnaryInterceptor(server.authUnary))
	defer rpc.Stop()
	pb.RegisterEggServer(rpc, server)
	go func() { _ = rpc.Serve(endpoint) }()
	client, err := Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client.token = "wrong-token"
	if _, err := client.ReclaimToolCapability(ctx); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated recovery admitted: %v", err)
	}
	client.token = server.token
	ordinary, err := client.Status(ctx)
	if err != nil || ordinary.ToolCapability != "" {
		t.Fatalf("ordinary status disclosed tool authority: %v", err)
	}
	recovered, err := client.ReclaimToolCapability(ctx)
	if err != nil || recovered != secret {
		t.Fatalf("recovery lost the original capability: %v", err)
	}
	restarted, err := NewToolListenerWithCapability(path, tools, recovered, ToolContext{Reclaimed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeToolListenerForTest(t, restarted)
	if result := toolCall(t, path, ToolRequest{Tool: "echo", Capability: secret}); result.Error != "" || result.Stdout != "restored" {
		t.Fatalf("surviving agent cannot call restored tools: %#v", result)
	}
	if result := toolCall(t, path, ToolRequest{Tool: "context", Capability: secret}); result.Error != "Context tools are unavailable in sessions that survived a wing restart; start a new session" {
		t.Fatalf("surviving agent retained Context authority: %#v", result)
	}
	if result := toolCall(t, path, ToolRequest{Tool: "echo", Capability: strings.Repeat("0", 64)}); result.Error == "" {
		t.Fatal("another egg's capability authenticated")
	}
	if _, err := NewToolListenerWithCapability(path, tools, ""); err == nil {
		t.Fatal("legacy capability bypassed authentication")
	}
}

// shortSockPath returns a Unix socket path short enough for macOS (104 char limit).
func shortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wt-ts-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove socket directory: %v", err)
		}
	})
	return filepath.Join(dir, "t.sock")
}

func closeToolListenerForTest(t *testing.T, listener *ToolListener) {
	t.Helper()
	if err := listener.Close(); err != nil {
		t.Errorf("close tool listener: %v", err)
	}
}

func TestToolControllerRoutingUsesCanonicalSessionSocket(t *testing.T) {
	root, err := os.MkdirTemp("../..", ".ctx-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "session")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tool.sock")
	listener, err := NewToolListener(path, []*config.ToolConfig{{Name: "context", Context: "jira-search"}}, ToolContext{OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeToolListenerForTest(t, listener)
	ObserveToolController(filepath.Join(alias, "tool.sock"), "owner")
	if response := toolCall(t, path, ToolRequest{Tool: "context"}); response.Error != "context: wing context block is required" {
		t.Fatalf("owner input revoked Context: %+v", response)
	}
	ObserveToolController(filepath.Join(root, "missing.sock"), "admin")
	if response := toolCall(t, path, ToolRequest{Tool: "context"}); response.Error != "context: wing context block is required" {
		t.Fatalf("unrelated session input revoked Context: %+v", response)
	}
	ObserveToolController(filepath.Join(alias, "tool.sock"), "admin")
	ObserveToolController(path, "owner")
	listener.Reload([]*config.ToolConfig{{Name: "context", Context: "jira-search"}})
	if response := toolCall(t, path, ToolRequest{Tool: "context"}); response.Error != "Context tools are disabled after another user took control of this session" {
		t.Fatalf("aliased non-owner input retained Context: %+v", response)
	}
}

func TestToolListener_CallAndResponse(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "echo-test", Run: `echo "$1"`, Timeout: "5s"},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)

	resp := toolCall(t, sockPath, ToolRequest{Tool: "echo-test", Args: []string{"hello world"}})
	if resp.ExitCode != 0 {
		t.Errorf("exit_code = %d, stderr = %q", resp.ExitCode, resp.Stderr)
	}
	if resp.Stdout != "hello world\n" {
		t.Errorf("stdout = %q, want %q", resp.Stdout, "hello world\n")
	}
}

func TestToolListenerRequiresCapabilityForCallsAndDiscovery(t *testing.T) {
	t.Setenv(ToolCapabilityEnv, "")
	sockPath := shortSockPath(t)
	marker := filepath.Join(filepath.Dir(sockPath), "ran")
	listener, err := NewToolListener(sockPath, []*config.ToolConfig{{Name: "mark", Run: "touch " + marker}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeToolListenerForTest(t, listener)
	other, err := NewToolListener(shortSockPath(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeToolListenerForTest(t, other)
	for _, action := range []string{"", "list"} {
		for _, capability := range []string{"", "wrong", other.capability} {
			// Use the old wire shape when capability is absent: the new
			// marshaler must not hide a missing-capability compatibility bug.
			data, err := json.Marshal(map[string]any{"tool": "mark", "action": action, "capability": capability})
			if err != nil {
				t.Fatal(err)
			}
			conn, err := net.Dial("unix", sockPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			var response ToolResponse
			if err := json.NewDecoder(conn).Decode(&response); err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
			if !strings.Contains(response.Error, "authentication failed") {
				t.Fatalf("unauthenticated action %q: %#v", action, response)
			}
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unauthenticated request executed a tool")
	}
	response := toolCall(t, sockPath, ToolRequest{Tool: "mark", Capability: listener.capability})
	if response.Error != "" || response.ExitCode != 0 {
		t.Fatalf("authenticated request failed: %#v", response)
	}
}

func TestToolCapabilityFailsClosedAfterListenerRestart(t *testing.T) {
	path := shortSockPath(t)
	first, err := NewToolListener(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	capability := first.capability
	closeToolListenerForTest(t, first)
	second, err := NewToolListener(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeToolListenerForTest(t, second)
	if second.capability == capability || len(capability) != 64 {
		t.Fatal("fresh listener reused a capability")
	}
	response := toolCall(t, path, ToolRequest{Action: "list", Capability: capability})
	if !strings.Contains(response.Error, "missing or invalid egg capability") {
		t.Fatalf("surviving egg did not fail closed clearly: %#v", response)
	}
}

func TestToolCapabilityNeverCreatesCredentialFile(t *testing.T) {
	path := shortSockPath(t)
	listener, err := NewToolListener(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatal("tool capability was written to disk")
	}
	closeToolListenerForTest(t, listener)
	if _, err := ToolSocketCapability(path); err == nil {
		t.Fatal("closed listener retained its capability")
	}
}

func TestToolRequestUsesShimEnvironmentWithoutChangingArgs(t *testing.T) {
	t.Setenv(ToolCapabilityEnv, "session-secret")
	args := []string{"--help", "spaces and quotes '\"", "--expected-channel", "stable"}
	data, err := json.Marshal(ToolRequest{Tool: "tool", Args: args})
	if err != nil {
		t.Fatal(err)
	}
	var request ToolRequest
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	if request.Capability != "session-secret" || strings.Join(request.Args, "\x00") != strings.Join(args, "\x00") {
		t.Fatalf("shim request = %#v", request)
	}
	if err := json.Unmarshal([]byte(`{"action":"list"}`), &request); err != nil {
		t.Fatal(err)
	}
	// Decode into a fresh request: ambient environment must never authenticate
	// an incoming old request inside the privileged listener.
	var incoming ToolRequest
	if err := json.Unmarshal([]byte(`{"action":"list"}`), &incoming); err != nil || incoming.Capability != "" {
		t.Fatal("ambient capability authenticated an incoming request", err)
	}
}

func TestToolListener_UnknownTool(t *testing.T) {
	sockPath := shortSockPath(t)
	tl, err := NewToolListener(sockPath, nil)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)

	resp := toolCall(t, sockPath, ToolRequest{Tool: "nope"})
	if resp.Error == "" {
		t.Error("expected error for unknown tool")
	}
}

func TestToolListenerRejectsOversizedRequest(t *testing.T) {
	sockPath := shortSockPath(t)
	tl, err := NewToolListener(sockPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeToolListenerForTest(t, tl)

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write(make([]byte, maxToolRequestBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	var response ToolResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Error, "too large") {
		t.Fatalf("oversized request response = %#v", response)
	}
}

func TestToolListenerBoundsIdleConnections(t *testing.T) {
	sockPath := shortSockPath(t)
	tl, err := NewToolListener(sockPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeToolListenerForTest(t, tl)

	connections := make([]net.Conn, 0, maxConcurrentToolSocketConnections)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for range maxConcurrentToolSocketConnections {
		connection, dialErr := net.Dial("unix", sockPath)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		connections = append(connections, connection)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(tl.connections) != maxConcurrentToolSocketConnections && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(tl.connections) != maxConcurrentToolSocketConnections {
		t.Fatalf("accepted connections = %d, want %d", len(tl.connections), maxConcurrentToolSocketConnections)
	}

	overflow, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = overflow.Close() }()
	if err := overflow.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if count, readErr := overflow.Read(buffer); count != 0 || readErr == nil {
		t.Fatalf("overflow connection read = %d, %v; want immediate close", count, readErr)
	}
}

func TestToolListener_List(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "db", Description: "Query DB", Run: "echo"},
		{Name: "search", Description: "Search", Run: "echo"},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	data, err := json.Marshal(ToolRequest{Action: "list", Capability: tl.capability})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	buf, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	var listResp ToolListResponse
	if err := json.Unmarshal(buf, &listResp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listResp.Tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(listResp.Tools))
	}
}

func TestToolListener_Timeout(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "slow", Run: "sleep 60", Timeout: "1s"},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)

	start := time.Now()
	resp := toolCall(t, sockPath, ToolRequest{Tool: "slow"})
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
	if resp.ExitCode == 0 {
		t.Error("expected non-zero exit code for timeout")
	}
}

func TestToolListener_Concurrent(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "echo", Run: `echo "$1"`, Timeout: "5s"},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)

	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := toolCall(t, sockPath, ToolRequest{Tool: "echo", Args: []string{"hi"}})
			if resp.ExitCode != 0 {
				t.Errorf("concurrent call %d: exit_code=%d stderr=%q", i, resp.ExitCode, resp.Stderr)
			}
		}(i)
	}
	wg.Wait()
}

func TestToolListener_MaxConcurrent(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "slow", Run: "sleep 2", Timeout: "5s", MaxConcurrent: 1},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)

	// First call grabs the semaphore
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		toolCall(t, sockPath, ToolRequest{Tool: "slow"})
	}()
	time.Sleep(200 * time.Millisecond) // let first call start
	// Second call should be rejected
	resp := toolCall(t, sockPath, ToolRequest{Tool: "slow"})
	if resp.Error == "" {
		t.Error("expected max_concurrent error for second call")
	}
	wg.Wait()
}

func TestToolListener_EnvInjection(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "env-test", Run: `echo "$MY_SECRET"`, Env: map[string]string{"MY_SECRET": "s3cret"}, Timeout: "5s"},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)

	resp := toolCall(t, sockPath, ToolRequest{Tool: "env-test"})
	if resp.ExitCode != 0 {
		t.Errorf("exit_code = %d, stderr = %q", resp.ExitCode, resp.Stderr)
	}
	if resp.Stdout != "s3cret\n" {
		t.Errorf("stdout = %q, want %q", resp.Stdout, "s3cret\n")
	}
}

func TestToolListener_Stderr(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "stderr-test", Run: "echo err >&2", Timeout: "5s"},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)
	resp := toolCall(t, sockPath, ToolRequest{Tool: "stderr-test"})
	if resp.Stderr != "err\n" {
		t.Errorf("stderr = %q, want %q", resp.Stderr, "err\n")
	}
	if resp.Stdout != "" {
		t.Errorf("stdout = %q, want empty", resp.Stdout)
	}
}

func TestToolListener_NonZeroExit(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "exit-test", Run: "exit 42", Timeout: "5s"},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)
	resp := toolCall(t, sockPath, ToolRequest{Tool: "exit-test"})
	if resp.ExitCode != 42 {
		t.Errorf("exit_code = %d, want 42", resp.ExitCode)
	}
}

func TestToolListener_MultiArg(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "multi-arg", Run: `echo "$1 $2"`, Timeout: "5s"},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)
	resp := toolCall(t, sockPath, ToolRequest{Tool: "multi-arg", Args: []string{"hello", "world"}})
	if resp.Stdout != "hello world\n" {
		t.Errorf("stdout = %q, want %q", resp.Stdout, "hello world\n")
	}
}

func TestToolListener_DefaultTimeout(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "no-timeout", Run: "echo ok"},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)
	resp := toolCall(t, sockPath, ToolRequest{Tool: "no-timeout"})
	if resp.ExitCode != 0 {
		t.Errorf("exit_code = %d, stderr = %q", resp.ExitCode, resp.Stderr)
	}
	if resp.Stdout != "ok\n" {
		t.Errorf("stdout = %q, want %q", resp.Stdout, "ok\n")
	}
}

func TestToolListener_ReloadWhileRunning(t *testing.T) {
	sockPath := shortSockPath(t)
	tools := []*config.ToolConfig{
		{Name: "tool-a", Run: `echo "a"`, Timeout: "5s"},
	}
	tl, err := NewToolListener(sockPath, tools)
	if err != nil {
		t.Fatalf("NewToolListener: %v", err)
	}
	defer closeToolListenerForTest(t, tl)
	// Call tool-a to verify it works
	resp := toolCall(t, sockPath, ToolRequest{Tool: "tool-a"})
	if resp.Stdout != "a\n" {
		t.Errorf("tool-a stdout = %q, want %q", resp.Stdout, "a\n")
	}
	// Reload with tool-b (removes tool-a)
	tl.Reload([]*config.ToolConfig{
		{Name: "tool-b", Run: `echo "b"`, Timeout: "5s"},
	})
	// tool-a should be gone
	resp = toolCall(t, sockPath, ToolRequest{Tool: "tool-a"})
	if resp.Error == "" {
		t.Error("expected error for removed tool-a after reload")
	}
	// tool-b should work
	resp = toolCall(t, sockPath, ToolRequest{Tool: "tool-b"})
	if resp.Stdout != "b\n" {
		t.Errorf("tool-b stdout = %q, want %q", resp.Stdout, "b\n")
	}
}

// toolCall is a test helper that sends a request and reads the response.
func toolCall(t *testing.T, sockPath string, req ToolRequest) ToolResponse {
	t.Helper()
	if req.Capability == "" {
		var err error
		req.Capability, err = ToolSocketCapability(sockPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	resp, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	var tr ToolResponse
	if err := json.Unmarshal(resp, &tr); err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestToolListenerControllerTaintIsPermanent(t *testing.T) {
	tools := []*config.ToolConfig{{Name: "context", Context: "jira-search"}, {Name: "command", Run: "printf allowed"}}
	listener := &ToolListener{owner: "owner@slide.tech", ownerID: "owner", runner: NewToolRunner(tools)}
	listener.ObserveController("owner")
	response := listener.runner.CallAs("context", nil, listener.owner, nil)
	if response.Error != "context: wing context block is required" {
		t.Fatalf("owner's own claim tainted tools: %+v", response)
	}
	for _, user := range []string{"other", "owner"} {
		listener.ObserveController(user)
		listener.Reload(tools)
		response = listener.runner.CallAs("context", nil, listener.owner, nil)
		if response.Error != "Context tools are disabled after another user took control of this session" {
			t.Fatalf("controller %s: %+v", user, response)
		}
		if listener.owner != "owner@slide.tech" || listener.ownerID != "owner" {
			t.Fatal("controller rebound Context owner")
		}
	}
	response = listener.runner.Call("command", nil)
	if response.Error != "" || response.Stdout != "allowed" {
		t.Fatalf("command tool after taint: %+v", response)
	}
}

func TestReclaimedSessionToolsFailClosedWithRestartMessage(t *testing.T) {
	tools := []*config.ToolConfig{{Name: "context", Context: "jira-search"}, {Name: "command", Run: "printf allowed"}}
	runner := newSessionToolRunner(tools, ToolContext{Reclaimed: true})
	for _, owner := range []string{"", "owner@slide.tech"} {
		runner.Reload(tools)
		response := runner.CallAs("context", nil, owner, nil)
		if response.Error != "Context tools are unavailable in sessions that survived a wing restart; start a new session" {
			t.Fatalf("reclaimed Context: %+v", response)
		}
	}
	response := runner.Call("command", nil)
	if response.Error != "" || response.Stdout != "allowed" {
		t.Fatalf("reclaimed command: %+v", response)
	}
}
