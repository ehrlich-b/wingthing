package egg

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
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
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "t.sock")
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
	defer tl.Close()

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
	defer tl.Close()

	resp := toolCall(t, sockPath, ToolRequest{Tool: "nope"})
	if resp.Error == "" {
		t.Error("expected error for unknown tool")
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
	defer tl.Close()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	data, _ := json.Marshal(ToolRequest{Action: "list"})
	conn.Write(data)
	conn.(*net.UnixConn).CloseWrite()
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	conn.Close()
	var listResp ToolListResponse
	if err := json.Unmarshal(buf[:n], &listResp); err != nil {
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
	defer tl.Close()

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
	defer tl.Close()

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
	defer tl.Close()

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
	defer tl.Close()

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
	defer tl.Close()
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
	defer tl.Close()
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
	defer tl.Close()
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
	defer tl.Close()
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
	defer tl.Close()
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
	defer conn.Close()
	data, _ := json.Marshal(req)
	conn.Write(data)
	conn.(*net.UnixConn).CloseWrite()
	resp, _ := io.ReadAll(conn)
	var tr ToolResponse
	json.Unmarshal(resp, &tr)
	return tr
}
