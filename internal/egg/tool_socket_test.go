package egg

import (
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
)

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
	data, err := json.Marshal(ToolRequest{Action: "list"})
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
