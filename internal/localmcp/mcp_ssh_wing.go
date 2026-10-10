package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/wingconnect"
)

// rememberedWings lives with the wing, outside eggs and MCP stdio lifetimes.
// Pools are isolated by the client's handshake authority, never shared by actor.
type rememberedWings struct {
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	pools       map[controlsocket.Hello]*wingconnect.Pool
	dir, wingID string
}

func newRememberedWings(ctx context.Context, dir, wingID string) *rememberedWings {
	ctx, cancel := context.WithCancel(ctx)
	return &rememberedWings{ctx: ctx, cancel: cancel, dir: dir, wingID: wingID, pools: map[controlsocket.Hello]*wingconnect.Pool{}}
}
func (m *rememberedWings) Close() error {
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pools {
		_ = p.Close()
	}
	return nil
}
func (m *rememberedWings) pool(hello controlsocket.Hello) (*wingconnect.Pool, error) {
	if hello.Conversation != "" || hello.Execution != "" {
		return nil, errors.New("remote control is unavailable on a conversation-bound MCP connection")
	}
	hello.Aggregate = false
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return nil, m.ctx.Err()
	}
	if p := m.pools[hello]; p != nil {
		return p, nil
	}
	p, err := wingconnect.New(m.ctx, wingconnect.Options{Dir: m.dir, LocalWingID: m.wingID, Hello: hello, Transport: sshcontrol.Transport{SocketDir: m.dir}})
	if err == nil {
		m.pools[hello] = p
	}
	return p, err
}
func (m *rememberedWings) remoteByName(ctx context.Context, hello controlsocket.Hello, name, tool string, args json.RawMessage) (map[string]any, error) {
	p, err := m.pool(hello)
	if err != nil {
		return nil, err
	}
	wingID, err := p.Resolve(name)
	if err != nil {
		return nil, err
	}
	result, denied, err := p.Call(ctx, wingID, tool, args)
	if err == nil && denied {
		err = control.ToolError(result)
		if err == nil {
			err = fmt.Errorf("%v", result["error"])
		}
	}
	return result, err
}
func (m *rememberedWings) dispatch(ctx context.Context, s *Server, hello controlsocket.Hello, request control.DirectRequest) control.DirectResponse {
	if _, taskOperation := control.MCPTaskTool(request.Tool); taskOperation && request.Version == control.ContractVersion && request.ID != "" {
		return m.dispatchTasks(ctx, s, hello, request)
	}
	fail := func(err error) control.DirectResponse {
		data := control.ErrorResult(err)
		var unknown *wingconnect.UnknownOutcome
		if errors.As(err, &unknown) {
			data["outcome"] = "unknown"
			data["wing_id"] = unknown.WingID
			if unknown.Key != "" {
				data["idempotency_key"] = unknown.Key
			}
		}
		return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Result: data, IsError: true}
	}
	tool, known := control.Lookup(request.Tool)
	if request.Version != control.ContractVersion || request.ID == "" || !known || !tool.Supports(control.SurfaceLocalMCP) || !s.toolAllowed(request.Tool) {
		return s.handleDirectRequest(ctx, request)
	}
	if request.Tool == "wing_list" {
		data, err := s.toolLocalWingList(request.Arguments)
		if err != nil {
			return fail(err)
		}
		p, err := m.pool(hello)
		if err != nil {
			return fail(err)
		}
		remotes, registryError := p.Entries()
		local := data["wings"].([]map[string]any)
		local[0]["name"] = "local"
		local[0]["last_error"] = ""
		data["wings"] = append(local, remotes...)
		data["count"] = len(local) + len(remotes)
		data["control_scope"] = "remembered"
		if registryError != "" {
			data["registry_error"] = registryError
		}
		return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Result: data}
	}
	wingID, args, err := control.SplitWingTarget(request.Arguments)
	if err != nil && request.Tool == "terminal_list" {
		// Legacy remote spelling selects exactly one pinned wing, with old bounds.
		var legacy struct {
			Remote string `json:"remote"`
		}
		if decodeStrict(request.Arguments, &legacy) == nil && legacy.Remote != "" {
			if s.enforcePathBounds || s.BoundConversation != "" {
				return fail(errors.New("remote terminal_list is unavailable on a path- or conversation-bound MCP connection"))
			}
			p, poolErr := m.pool(hello)
			if poolErr != nil {
				return fail(poolErr)
			}
			wingID, err = p.Resolve(legacy.Remote)
			args = json.RawMessage(`{}`)
		}
	}
	if err != nil {
		return fail(err)
	}
	if wingID != m.wingID {
		if hello.Conversation != "" || hello.Execution != "" {
			return fail(errors.New("remote control is unavailable on a conversation-bound MCP connection"))
		}
		// remote is only a legacy selector; never allow recursive onward SSH routing.
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(args, &fields)
		if _, hasRemote := fields["remote"]; hasRemote {
			return fail(errors.New("terminal_list.remote cannot be combined with a remote wing_id"))
		}
		p, err := m.pool(hello)
		if err != nil {
			return fail(err)
		}
		data, denied, err := p.Call(ctx, wingID, request.Tool, args)
		if err != nil {
			return fail(err)
		}
		return control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Result: data, IsError: denied}
	}
	request.Arguments = args
	key := ""
	if request.Tool == "agent_run" {
		request.Arguments, key, err = wingconnect.AdmissionArguments(args)
		if err != nil {
			return fail(err)
		}
	}
	response := s.handleDirectRequest(ctx, request)
	if key != "" && response.Result != nil {
		response.Result["idempotency_key"] = key
	}
	return response
}

// ServeRememberedWingClient is a thin stdio adapter. SSH keys and the connection
// pool remain in the independently running local wing.
func ServeRememberedWingClient(ctx context.Context, version, dir, clientName string, unsandboxed bool, timeout time.Duration, in io.Reader, out io.Writer) error {
	ctxDial, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := controlsocket.Dial(ctxDial, dir, controlsocket.Hello{Client: clientName, Unsandboxed: unsandboxed, Aggregate: true})
	if err != nil {
		return fmt.Errorf("connect to local wing: %w", err)
	}
	defer c.Close()
	proxy := &localWingProxy{version: version, client: c, aggregate: true}
	return serveStdio(ctx, in, out, proxy.handle)
}
