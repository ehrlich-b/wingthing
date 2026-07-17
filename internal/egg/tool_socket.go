package egg

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
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
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Params      []config.ToolParam `json:"params,omitempty"`
}

// ToolListResponse is returned for the "list" action.
type ToolListResponse struct {
	Tools []ToolListEntry `json:"tools"`
}

// ToolListener accepts connections on a Unix socket and dispatches tool execution to a
// shared ToolRunner. Egg sessions reach tools this way; the remote MCP server wraps the
// same runner over HTTP.
type ToolListener struct {
	runner   *ToolRunner
	listener net.Listener
	wg       sync.WaitGroup
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
		runner:   NewToolRunner(tools),
		listener: ln,
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
	tl.runner.Reload(tools)
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
		out, _ := json.Marshal(ToolListResponse{Tools: tl.runner.List()})
		conn.Write(out)
		return
	}
	if req.Tool == "" {
		writeJSON(conn, ToolResponse{Error: "missing tool name"})
		return
	}
	// Extend deadline based on tool timeout.
	deadline := 5 * time.Minute
	if tt := tl.runner.TimeoutFor(req.Tool); tt+30*time.Second > deadline {
		deadline = tt + 30*time.Second
	}
	conn.SetDeadline(time.Now().Add(deadline))
	writeJSON(conn, tl.runner.Call(req.Tool, req.Args))
}

func writeJSON(conn net.Conn, v any) {
	data, _ := json.Marshal(v)
	conn.Write(data)
}
