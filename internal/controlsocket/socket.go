// Package controlsocket transports the wing control contract over owner-only local IPC.
package controlsocket

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
 "io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
)

const maxEnvelope = 1024 * 1024
const SocketName = "control.sock"

type Hello struct {
	Version      string `json:"version"`
	WingID       string `json:"wing_id,omitempty"`
	Client       string `json:"client,omitempty"`
	Unsandboxed  bool   `json:"unsandboxed,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	Execution    string `json:"execution,omitempty"`
}
type Welcome struct {
 Isolation string `json:"isolation,omitempty"`
	Version   string          `json:"version"`
	WingID    string          `json:"wing_id"`
	Principal string          `json:"principal,omitempty"`
	Actor     string          `json:"actor,omitempty"`
	Grants    map[string]bool `json:"grants"`
	Tools     map[string]bool `json:"tools,omitempty"`
	Error     string          `json:"error,omitempty"`
}
type Handler func(context.Context, control.DirectRequest) control.DirectResponse
type Bind func(Hello) (Welcome, Handler, error)

type Server struct {
	listener *net.UnixListener
	cancel   context.CancelFunc
	done     chan struct{}
}

// Listen never starts a wing. Only the wing calls it, before connecting to its relay.
func Listen(ctx context.Context, dir, wingID string, bind Bind) (*Server, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !ownedByUser(info) {
		return nil, errors.New("local control state directory must be owned by the wing user and cannot be a symlink")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, SocketName)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 || !ownedByUser(info) {
			return nil, errors.New("local control path is not an owned socket")
		}
		c, dialErr := net.Dial("unix", path)
		if dialErr == nil {
			_ = c.Close()
			return nil, errors.New("a wing already serves this state directory")
		}
		if !staleSocketError(dialErr) {
			return nil, dialErr
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on local wing control socket: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{listener: listener, cancel: cancel, done: make(chan struct{})}
	go func() { <-ctx.Done(); _ = listener.Close() }()
	go func() {
		defer close(s.done)
		var connections sync.WaitGroup
		defer connections.Wait()
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() { defer connections.Done(); serve(ctx, conn, wingID, bind) }()
		}
	}()
	return s, nil
}
func (s *Server) Close() error { s.cancel(); <-s.done; return nil }

func scan(conn net.Conn) *bufio.Scanner {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), maxEnvelope)
	return scanner
}
func serve(ctx context.Context, conn *net.UnixConn, wingID string, bind Bind) {
	defer conn.Close()
	if err := checkPeer(conn); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-ctx.Done(); _ = conn.Close() }()
	// Bound incomplete handshakes; this deadline is not an execution deadline.
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	scanner := scan(conn)
	encoder := json.NewEncoder(conn)
	if !scanner.Scan() {
		return
	}
	var hello Hello
	if err := strictJSON(scanner.Bytes(), &hello); err != nil {
		_ = encoder.Encode(Welcome{Version: control.ContractVersion, WingID: wingID, Error: "invalid local control handshake"})
		return
	}
	welcome := Welcome{Version: control.ContractVersion, WingID: wingID}
	if hello.Version != control.ContractVersion || hello.WingID != "" && hello.WingID != wingID {
		welcome.Error = "incompatible local wing control protocol or wing ID; upgrade wt and restart the wing"
		_ = encoder.Encode(welcome)
		return
	}
	resolved, handler, err := bind(hello)
	if err != nil {
		welcome.Error = err.Error()
	} else {
		welcome = resolved
		welcome.Version = control.ContractVersion
		welcome.WingID = wingID
	}
	if encoder.Encode(welcome) != nil || err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	var writes sync.Mutex
	var calls sync.WaitGroup
	defer calls.Wait()
	defer cancel()
	slots := make(chan struct{}, 32)
	send := func(response control.DirectResponse) {
		payload, err := json.Marshal(response)
		if err != nil || len(payload) > maxEnvelope {
			payload, _ = json.Marshal(control.DirectResponse{Version: control.ContractVersion, ID: response.ID, Error: "response exceeds local control envelope limit", IsError: true})
		}
		writes.Lock()
		defer writes.Unlock()
		_, _ = conn.Write(append(payload, '\n'))
	}
	for scanner.Scan() {
		var request control.DirectRequest
		if strictJSON(scanner.Bytes(), &request) != nil {
			send(control.DirectResponse{Version: control.ContractVersion, Error: "invalid control request"})
			continue
		}
		select {
		case slots <- struct{}{}:
			calls.Add(1)
			go func() { defer calls.Done(); defer func() { <-slots }(); send(handler(ctx, request)) }()
		default:
			send(control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Error: "too many concurrent control requests"})
		}
	}
}

func strictJSON(data []byte, out any) error {
	if len(data) == 0 || data[0] != '{' {
		return errors.New("control envelope must be an object")
	}
	// Decode closed transport envelopes, so caller-supplied identity is never accepted.
	reader := bytes.NewReader(data)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err:=decoder.Decode(out);err!=nil {return err}
 if err:=decoder.Decode(new(any));err!=io.EOF {return errors.New("control envelope must contain one object")}
 return nil
}

type Client struct {
	conn    net.Conn
	Welcome Welcome
	scanner *bufio.Scanner
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan control.DirectResponse
	done    chan struct{}
	next    atomic.Uint64
}

func Dial(ctx context.Context, dir string, hello Hello) (*Client, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, SocketName))
	if err != nil {
		return nil, fmt.Errorf("no local wing for WINGTHING_DIR=%s: %w; start one with wt roost start or wt wing", dir, err)
	}
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	hello.Version = control.ContractVersion
	if unixConn, ok := conn.(*net.UnixConn); !ok {
		return nil, errors.New("local wing control requires a Unix socket")
	} else if err := checkPeer(unixConn); err != nil {
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(hello); err != nil {
		return nil, err
	}
	scanner := scan(conn)
	if !scanner.Scan() {
		return nil, errors.New("local wing handshake closed")
	}
	var welcome Welcome
	if err := json.Unmarshal(scanner.Bytes(), &welcome); err != nil {
		return nil, err
	}
	if welcome.Error != "" {
		return nil, errors.New(welcome.Error)
	}
	if welcome.Version != control.ContractVersion || welcome.WingID == "" || hello.WingID != "" && welcome.WingID != hello.WingID {
		return nil, errors.New("incompatible local wing handshake; upgrade wt and restart the wing")
	}
	_ = conn.SetDeadline(time.Time{})
	c := &Client{conn: conn, Welcome: welcome, scanner: scanner, pending: make(map[string]chan control.DirectResponse), done: make(chan struct{})}
	success = true
	go c.read()
	return c, nil
}
func (c *Client) read() {
	defer close(c.done)
	defer c.conn.Close()
	for c.scanner.Scan() {
		var r control.DirectResponse
		if json.Unmarshal(c.scanner.Bytes(), &r) != nil || r.Version != control.ContractVersion {
			return
		}
		c.mu.Lock()
		ch := c.pending[r.ID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- r:
			default:
			}
		}
	}
}
func (c *Client) Call(ctx context.Context, tool string, arguments json.RawMessage) (map[string]any, bool, error) {
	id := fmt.Sprint(c.next.Add(1))
	ch := make(chan control.DirectResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	payload, err := json.Marshal(control.DirectRequest{Version: control.ContractVersion, ID: id, Tool: tool, Arguments: arguments})
	if err != nil {
		return nil, true, err
	}
	if len(payload) > maxEnvelope {
		return nil, true, errors.New("request exceeds local control envelope limit")
	}
	c.writeMu.Lock()
	_, err = c.conn.Write(append(payload, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		return nil, true, err
	}
	select {
	case r := <-ch:
		if r.Error != "" {
			return nil, true, errors.New(r.Error)
		}
		return r.Result, r.IsError, nil
	case <-ctx.Done():
		return nil, true, ctx.Err()
	case <-c.done:
		return nil, true, errors.New("local wing control disconnected")
	}
}
func (c *Client) Close() error { return c.conn.Close() }
