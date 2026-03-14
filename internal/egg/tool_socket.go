package egg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
)

// ToolRequest is sent by `wt tool-call` over the Unix socket.
type ToolRequest struct {
	Action string   `json:"action,omitempty"` // "list" for tool discovery
	Tool   string   `json:"tool,omitempty"`
	Args   []string `json:"args,omitempty"`
}

// ToolResponse is returned to the client.
type ToolResponse struct {
	ExitCode int    `json:"exit_code,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ToolListEntry describes one tool for the list action.
type ToolListEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ToolListResponse is returned for the "list" action.
type ToolListResponse struct {
	Tools []ToolListEntry `json:"tools"`
}

// ToolListener accepts connections on a Unix socket and dispatches tool execution.
type ToolListener struct {
	mu       sync.RWMutex
	tools    map[string]*config.ToolConfig
	listener net.Listener
	wg       sync.WaitGroup
	sema     map[string]chan struct{} // per-tool concurrency semaphores
}

// NewToolListener creates and starts a tool socket listener.
// sockPath is the path for the Unix socket (e.g. ~/.wingthing/eggs/<session>/tool.sock).
func NewToolListener(sockPath string, tools []*config.ToolConfig) (*ToolListener, error) {
	os.Remove(sockPath) // clean up stale socket
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen tool socket: %w", err)
	}
	os.Chmod(sockPath, 0700)
	tl := &ToolListener{
		tools:    make(map[string]*config.ToolConfig, len(tools)),
		listener: ln,
		sema:     make(map[string]chan struct{}),
	}
	for _, t := range tools {
		tl.tools[t.Name] = t
		if t.MaxConcurrent > 0 {
			tl.sema[t.Name] = make(chan struct{}, t.MaxConcurrent)
		}
	}
	tl.wg.Add(1)
	go tl.acceptLoop()
	return tl, nil
}

// Close stops the listener and waits for in-flight requests to finish.
func (tl *ToolListener) Close() error {
	err := tl.listener.Close()
	tl.wg.Wait()
	return err
}

// Reload replaces the tool configs atomically.
func (tl *ToolListener) Reload(tools []*config.ToolConfig) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	newMap := make(map[string]*config.ToolConfig, len(tools))
	newSema := make(map[string]chan struct{})
	for _, t := range tools {
		newMap[t.Name] = t
		if t.MaxConcurrent > 0 {
			// Reuse existing semaphore if same capacity
			if old, ok := tl.sema[t.Name]; ok && cap(old) == t.MaxConcurrent {
				newSema[t.Name] = old
			} else {
				newSema[t.Name] = make(chan struct{}, t.MaxConcurrent)
			}
		}
	}
	tl.tools = newMap
	tl.sema = newSema
}

func (tl *ToolListener) acceptLoop() {
	defer tl.wg.Done()
	for {
		conn, err := tl.listener.Accept()
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed") {
				log.Printf("tool socket accept: %v", err)
			}
			return
		}
		tl.wg.Add(1)
		go func() {
			defer tl.wg.Done()
			tl.handleConn(conn)
		}()
	}
}

func (tl *ToolListener) handleConn(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	data, err := io.ReadAll(conn)
	if err != nil {
		writeJSON(conn, ToolResponse{Error: "read failed: " + err.Error()})
		return
	}
	var req ToolRequest
	if err := json.Unmarshal(data, &req); err != nil {
		writeJSON(conn, ToolResponse{Error: "invalid JSON: " + err.Error()})
		return
	}
	if req.Action == "list" {
		tl.handleList(conn)
		return
	}
	if req.Tool == "" {
		writeJSON(conn, ToolResponse{Error: "missing tool name"})
		return
	}
	tl.mu.RLock()
	tc, ok := tl.tools[req.Tool]
	var sema chan struct{}
	if ok {
		sema = tl.sema[req.Tool]
	}
	tl.mu.RUnlock()
	if !ok {
		writeJSON(conn, ToolResponse{Error: "unknown tool: " + req.Tool})
		return
	}
	// Extend deadline based on tool timeout
	toolTimeout := tc.TimeoutDuration()
	if toolTimeout <= 0 {
		toolTimeout = 60 * time.Second
	}
	deadline := 5 * time.Minute
	if toolTimeout+30*time.Second > deadline {
		deadline = toolTimeout + 30*time.Second
	}
	conn.SetDeadline(time.Now().Add(deadline))
	// Concurrency semaphore
	if sema != nil {
		select {
		case sema <- struct{}{}:
			defer func() { <-sema }()
		default:
			writeJSON(conn, ToolResponse{Error: fmt.Sprintf("tool %s: max concurrent limit reached", req.Tool)})
			return
		}
	}
	resp := tl.executeTool(tc, req.Args)
	writeJSON(conn, resp)
}

func (tl *ToolListener) handleList(conn net.Conn) {
	tl.mu.RLock()
	var entries []ToolListEntry
	for _, t := range tl.tools {
		entries = append(entries, ToolListEntry{Name: t.Name, Description: t.Description})
	}
	tl.mu.RUnlock()
	data, _ := json.Marshal(ToolListResponse{Tools: entries})
	conn.Write(data)
}

func (tl *ToolListener) executeTool(tc *config.ToolConfig, args []string) ToolResponse {
	timeout := tc.TimeoutDuration()
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// sh -c 'script' tool arg1 arg2 ...
	// "tool" is $0, args become $1, $2, etc.
	cmdArgs := append([]string{"-c", tc.Run, "tool"}, args...)
	cmd := exec.CommandContext(ctx, "sh", cmdArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Build environment: inherit minimal host env + tool-specific env
	cmd.Env = append(os.Environ(), toolEnvSlice(tc.Env)...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return ToolResponse{ExitCode: 124, Stderr: "tool execution timed out"}
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return ToolResponse{ExitCode: 1, Stderr: err.Error()}
		}
	}
	return ToolResponse{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String()}
}

func toolEnvSlice(env map[string]string) []string {
	s := make([]string, 0, len(env))
	for k, v := range env {
		s = append(s, k+"="+v)
	}
	return s
}

func writeJSON(conn net.Conn, v any) {
	data, _ := json.Marshal(v)
	conn.Write(data)
}
