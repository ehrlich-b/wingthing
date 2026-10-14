// Package permissions owns live, request-specific Claude Bash decisions.
package permissions

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/permissionbridge"
)

const Lifetime = 90 * time.Second
const HookTimeout = 95

type Request struct {
	WingID     string         `json:"wing_id"`
	SessionID  string         `json:"session_id"`
	ProviderID string         `json:"provider_id"`
	Generation string         `json:"generation"`
	RequestID  string         `json:"request_id"`
	Digest     string         `json:"digest"`
	Tool       string         `json:"tool"`
	Arguments  map[string]any `json:"arguments"`
	Directory  string         `json:"directory"`
	Machine    string         `json:"machine"`
	User       string         `json:"user"`
	Scope      string         `json:"scope"`
	Deadline   time.Time      `json:"deadline"`
}
type Response struct {
	WingID    string `json:"wing_id"`
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
	Digest    string `json:"digest"`
	AttemptID string `json:"attempt_id"`
	Decision  string `json:"decision"`
}
type Outcome struct {
	State     string `json:"state"`
	Decision  string `json:"decision"`
	AttemptID string `json:"attempt_id"`
}
type exchange struct {
	Token      string                 `json:"token"`
	SessionID  string                 `json:"session_id"`
	Generation string                 `json:"generation"`
	Input      permissionbridge.Input `json:"input"`
}
type reply struct {
	Request  Request `json:"request"`
	Decision string  `json:"decision"`
}
type pending struct {
	request  Request
	conn     net.Conn
	done     chan struct{}
	answered bool
	finish   sync.Once
	ack      chan bool
	readDone chan struct{}
}
type Broker struct {
	mu                                                      sync.Mutex
	SessionID, ProviderID, Generation, Token, Machine, User string
	Duration                                                time.Duration
	current                                                 *pending
}

func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func Socket(home, session string) string {
	return filepath.Join(home, ".claude", "wp-"+session, "p.sock")
}
func New(session, provider, machine, user string) *Broker {
	return &Broker{SessionID: session, ProviderID: provider, Generation: ID(), Token: ID(), Machine: machine, User: user, Duration: Lifetime}
}
func (b *Broker) Serve(ctx context.Context, l net.Listener) error {
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() { defer wg.Done(); defer c.Close(); b.handle(ctx, c) }()
	}
}
func (b *Broker) handle(ctx context.Context, c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	var ex exchange
	if decode(c, &ex) != nil || subtle.ConstantTimeCompare([]byte(ex.Token), []byte(b.Token)) != 1 || ex.SessionID != b.SessionID || ex.Generation != b.Generation || ex.Input.ProviderID != b.ProviderID {
		return
	}
	wire, _ := json.Marshal(ex.Input)
	in, err := permissionbridge.ParseInput(bytes.NewReader(wire))
	if err != nil {
		return
	}
	b.mu.Lock()
	if b.current != nil {
		b.current.conn.Close()
		b.current.finish.Do(func() { close(b.current.done) })
	}
	p := &pending{request: Request{SessionID: b.SessionID, ProviderID: b.ProviderID, Generation: b.Generation, RequestID: ID(), Digest: permissionbridge.Digest(in), Tool: in.Tool, Arguments: in.Arguments, Directory: in.Directory, Machine: b.Machine, User: b.User, Scope: "This Bash invocation, including the complete command and arguments", Deadline: time.Now().Add(b.Duration)}, conn: c, done: make(chan struct{})}
	p.ack = make(chan bool, 1)
	p.readDone = make(chan struct{})
	_ = c.SetDeadline(p.request.Deadline)
	b.current = p
	b.mu.Unlock()
	go func() {
		defer close(p.readDone)
		var ack struct {
			RequestID string `json:"request_id"`
		}
		err := decode(c, &ack)
		p.ack <- err == nil && ack.RequestID == p.request.RequestID
	}()
	// Closing a hook (including a native prompt answer) invalidates its handle.
	defer func() { c.Close(); <-p.readDone }()
	timer := time.NewTimer(time.Until(p.request.Deadline))
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	case <-p.done:
	case <-p.readDone:
	}
	b.mu.Lock()
	if b.current == p {
		b.current = nil
		if !p.answered {
			p.finish.Do(func() { close(p.done) })
		}
	}
	b.mu.Unlock()
}
func (b *Broker) List() []Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.current == nil || b.current.answered || !time.Now().Before(b.current.request.Deadline) {
		return []Request{}
	}
	r := b.current.request
	wire, _ := json.Marshal(r)
	var copy Request
	d := json.NewDecoder(bytes.NewReader(wire))
	d.UseNumber()
	_ = d.Decode(&copy)
	return []Request{copy}
}
func (b *Broker) Respond(r Response) (Outcome, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.current
	if p == nil || p.answered || !time.Now().Before(p.request.Deadline) || r.SessionID != b.SessionID || r.RequestID != p.request.RequestID || r.Digest != p.request.Digest || (r.Decision != "allow" && r.Decision != "deny") {
		return Outcome{}, errors.New("request is expired, replaced or already answered; Answer in terminal")
	}
	p.answered = true
	defer p.finish.Do(func() { close(p.done) })
	_ = p.conn.SetDeadline(minTime(p.request.Deadline, time.Now().Add(2*time.Second)))
	out := Outcome{State: "unconfirmed", Decision: r.Decision, AttemptID: r.AttemptID}
	if err := json.NewEncoder(p.conn).Encode(reply{p.request, r.Decision}); err != nil {
		return out, nil
	}
	if <-p.ack {
		out.State = "delivered"
	}
	return out, nil
}
func decode(r io.Reader, v any) error {
	d := json.NewDecoder(io.LimitReader(r, permissionbridge.MaxMessage+1))
	d.UseNumber()
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// Hook never emits a decision after its deadline or after a binding mismatch.
// Failure returns silently to native permission handling.
func Hook(ctx context.Context, socket, session, generation, token string, in permissionbridge.Input, out io.Writer) error {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer c.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("hook needs a deadline")
	}
	_ = c.SetDeadline(deadline)
	if err = json.NewEncoder(c).Encode(exchange{token, session, generation, in}); err != nil {
		return err
	}
	var a reply
	if err = decode(c, &a); err != nil {
		return err
	}
	r := a.Request
	if ctx.Err() != nil || !time.Now().Before(r.Deadline) || !time.Now().Before(deadline) || r.SessionID != session || r.Generation != generation || r.ProviderID != in.ProviderID || r.RequestID == "" || r.Digest != permissionbridge.Digest(in) || (a.Decision != "allow" && a.Decision != "deny") {
		return errors.New("stale permission reply")
	}
	decision := map[string]any{"behavior": a.Decision}
	if a.Decision == "deny" {
		decision["interrupt"] = false
		decision["message"] = "This request was declined."
	}
	if err = json.NewEncoder(out).Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": decision}}); err != nil {
		return err
	}
	return json.NewEncoder(c).Encode(map[string]string{"request_id": r.RequestID})
}
func Listen(socket string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(socket), 0700); err != nil {
		return nil, err
	}
	return permissionbridge.Listen(socket)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
