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
	"sync"
	"sync/atomic"
	"time"

	"github.com/ehrlich-b/wingthing/internal/control"
)

const maxEnvelope = 1024 * 1024
const SocketName = "control.sock"

type Hello struct {
	Version      string      `json:"version"`
	WingID       string      `json:"wing_id,omitempty"`
	Client       string      `json:"client,omitempty"`
	Unsandboxed  bool        `json:"unsandboxed,omitempty"`
	Conversation string      `json:"conversation,omitempty"`
	Execution    string      `json:"execution,omitempty"`
	Aggregate    bool        `json:"aggregate,omitempty"`
	Attach       *Attachment `json:"attach,omitempty"`
	// Scope requests a narrower ceiling from an authenticated receiving wing.
	// It never supplies identity or grants greater authority than the peer UID.
	Scope string `json:"scope,omitempty"`
}
type Welcome struct {
	Isolation     string          `json:"isolation,omitempty"`
	RemoteAllowed bool            `json:"remote_allowed,omitempty"`
	Version       string          `json:"version"`
	WingID        string          `json:"wing_id"`
	Principal     string          `json:"principal,omitempty"`
	Actor         string          `json:"actor,omitempty"`
	Grants        map[string]bool `json:"grants"`
	Tools         map[string]bool `json:"tools,omitempty"`
	Error         string          `json:"error,omitempty"`
}
type Handler func(context.Context, control.DirectRequest) control.DirectResponse
type Bind func(Hello) (Welcome, Handler, error)

type Server struct {
	listener  *net.UnixListener
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	closers   []io.Closer
}

// Listen never starts a wing. Only the wing calls it, before connecting to its relay.
func Listen(ctx context.Context, dir, wingID string, bind Bind, attachments ...AttachBind) (*Server, error) {
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
	path, relocated, err := socketPath(dir)
	if err != nil {
		return nil, err
	}
	if relocated {
		if err := verifyRuntimeSocketDir(runtimeSocketDir(), true); err != nil {
			return nil, err
		}
	}
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
	if relocated {
		if err := writeSocketPath(dir, path); err != nil {
			_ = listener.Close()
			return nil, err
		}
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
			go func() { defer connections.Done(); serve(ctx, conn, wingID, bind, attachments) }()
		}
	}()
	return s, nil
}

// AddCloser attaches wing-owned transports before publishing the server.
func (s *Server) AddCloser(closer io.Closer) { s.closers = append(s.closers, closer) }
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		for _, closer := range s.closers {
			_ = closer.Close()
		}
		<-s.done
	})
	return nil
}

func scan(conn net.Conn) *bufio.Scanner {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), maxEnvelope)
	return scanner
}
func serve(ctx context.Context, conn *net.UnixConn, wingID string, bind Bind, attachments []AttachBind) {
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
	if hello.Attach != nil {
		serveAttachment(ctx, conn, scanner, encoder, wingID, hello, attachments)
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
	var lifetimes control.RequestLifetimes
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
		if id, notification := control.CancellationID(request); notification {
			lifetimes.Cancel(id)
			continue
		}
		select {
		case slots <- struct{}{}:
			requestCtx, finish, ok := lifetimes.Start(ctx, request.ID)
			if !ok {
				<-slots
				send(control.DirectResponse{Version: control.ContractVersion, ID: request.ID, Error: "duplicate active request ID"})
				continue
			}
			calls.Add(1)
			go func() {
				defer calls.Done()
				defer func() { <-slots }()
				response := handler(requestCtx, request)
				finish()
				send(response)
			}()
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
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("control envelope must contain one object")
	}
	return nil
}

type Client struct {
	conn      net.Conn
	Welcome   Welcome
	scanner   *bufio.Scanner
	writeSlot chan struct{}
	mu        sync.Mutex
	pending   map[string]chan control.DirectResponse
	done      chan struct{}
	next      atomic.Uint64
}

// Path returns the derived endpoint, never a caller-writable socket pointer.
func Path(dir string) (string, error) {
	path, relocated, err := socketPath(dir)
	if err == nil && relocated {
		err = verifyRuntimeSocketDir(runtimeSocketDir(), false)
	}
	return path, err
}
func Dial(ctx context.Context, dir string, hello Hello) (*Client, error) {
	path, err := Path(dir)
	var client *Client
	if err == nil {
		client, err = DialPath(ctx, path, hello)
	}
	if err != nil {
		return nil, fmt.Errorf("no local wing for WINGTHING_DIR=%s: %w; start one with wt wing start --local-only or wt roost start", dir, err)
	}
	return client, nil
}

// DialPath connects to a verified SSH stream-local forward as well as local IPC.
// The forwarding process must belong to this UID; Hello pins the remote wing.
func DialPath(ctx context.Context, path string, hello Hello) (*Client, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
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
	c := &Client{conn: conn, Welcome: welcome, scanner: scanner, pending: make(map[string]chan control.DirectResponse), done: make(chan struct{}), writeSlot: make(chan struct{}, 1)}
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
	select {
	case c.writeSlot <- struct{}{}:
	case <-ctx.Done():
		return nil, true, ctx.Err()
	case <-c.done:
		return nil, true, &TransportError{Err: errors.New("wing control disconnected")}
	}
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.conn.SetWriteDeadline(deadline)
	_, err = c.conn.Write(append(payload, '\n'))
	<-c.writeSlot
	if err != nil {
		return nil, true, &TransportError{Err: err}
	}
	select {
	case r := <-ch:
		if err := r.Err(); err != nil {
			return r.Result, true, err
		}
		return r.Result, r.IsError, nil
	case <-ctx.Done():
		c.cancelRequest(id)
		return nil, true, ctx.Err()
	case <-c.done:
		return nil, true, &TransportError{Err: errors.New("wing control disconnected")}
	}
}

func (c *Client) cancelRequest(id string) {
	// Forward cancellation even if another request is writing, with a bounded
	// cleanup deadline independent of the already cancelled caller context.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case c.writeSlot <- struct{}{}:
	case <-c.done:
		return
	case <-ctx.Done():
		return
	}
	defer func() { <-c.writeSlot }()
	payload, _ := json.Marshal(control.CancellationRequest(id))
	_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = c.conn.Write(append(payload, '\n'))
}
func (c *Client) Close() error { return c.conn.Close() }

// Done signals a lost channel without requiring another tool call.
func (c *Client) Done() <-chan struct{} { return c.done }

// TransportError distinguishes unknown delivery from authoritative tool errors.
type TransportError struct{ Err error }

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }
