// Package wingconnect owns a wing's remembered SSH connection pool. Remote
// execution stays in the receiving wing; this pool only routes bounded calls.
package wingconnect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
)

type Timer interface {
	C() <-chan time.Time
	Stop()
}
type realTimer struct{ *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.Timer.C }
func (t realTimer) Stop()               { t.Timer.Stop() }

type Connection interface {
	Call(context.Context, string, json.RawMessage) (map[string]any, bool, error)
	Done() <-chan struct{}
	Close() error
}
type sshConnection struct{ *sshcontrol.Connection }

func (c sshConnection) Call(ctx context.Context, name string, args json.RawMessage) (map[string]any, bool, error) {
	return c.Client.Call(ctx, name, args)
}

type Options struct {
	Dir, LocalWingID            string
	Hello                       controlsocket.Hello
	Transport                   sshcontrol.Transport
	ConnectTimeout, CallTimeout time.Duration
	// Deterministic fixture seams; production uses real timers and OpenSSH.
	Dial     func(context.Context, config.Remote, controlsocket.Hello) (Connection, error)
	NewTimer func(time.Duration) Timer
	Jitter   func(time.Duration) time.Duration
	OnChange func()
}
type Pool struct {
	opts          Options
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	entries       map[string]*entry
	registryError string
	watcher       *fsnotify.Watcher
	wg            sync.WaitGroup
	reloadMu      sync.Mutex
	closeOnce     sync.Once
}
type entry struct {
	pool       *Pool
	name       string
	remote     config.Remote
	ctx        context.Context
	cancel     context.CancelFunc
	wake       chan struct{}
	mu         sync.Mutex
	changed    chan struct{}
	connection Connection
	connecting bool
	attempts   uint64
	lastError  string
}

func New(ctx context.Context, opts Options) (*Pool, error) {
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 10 * time.Second
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = 2 * time.Minute
	}
	if opts.NewTimer == nil {
		opts.NewTimer = func(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }
	}
	if opts.Jitter == nil {
		opts.Jitter = func(d time.Duration) time.Duration { return time.Duration(float64(d) * (0.8 + rand.Float64()*0.4)) }
	}
	if opts.Dial == nil {
		opts.Dial = func(ctx context.Context, r config.Remote, h controlsocket.Hello) (Connection, error) {
			c, err := opts.Transport.Dial(ctx, r, h)
			if err != nil {
				return nil, err
			}
			return sshConnection{c}, nil
		}
	}
	opts.Hello.Aggregate = false // Never recursively discover another machine's remotes.
	ctx, cancel := context.WithCancel(ctx)
	p := &Pool{opts: opts, ctx: ctx, cancel: cancel, entries: map[string]*entry{}}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		cancel()
		return nil, err
	}
	p.watcher = watcher
	if err = watcher.Add(opts.Dir); err != nil {
		watcher.Close()
		cancel()
		return nil, err
	}
	// A bad registry cannot take local control down; retain/report directory errors.
	_ = p.Reload()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Name == filepath.Join(opts.Dir, "remotes.yaml") {
					_ = p.Reload()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				p.mu.Lock()
				p.registryError = err.Error()
				p.mu.Unlock()
				p.notify()
			}
		}
	}()
	return p, nil
}
func (p *Pool) notify() {
	if p.opts.OnChange != nil {
		p.opts.OnChange()
	}
}
func (e *entry) signal() { close(e.changed); e.changed = make(chan struct{}); e.pool.notify() }
func (p *Pool) Reload() error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()
	if p.ctx.Err() != nil {
		return p.ctx.Err()
	}
	remotes, err := config.LoadRemotes(p.opts.Dir)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.registryError = err.Error()
		p.notify()
		return err
	}
	p.registryError = ""
	for name, e := range p.entries {
		if r, ok := remotes[name]; !ok || r != e.remote {
			e.cancel()
			delete(p.entries, name)
		}
	}
	ids := map[string]int{}
	for _, r := range remotes {
		if r.WingID != "" {
			ids[r.WingID]++
		}
	}
	for name, r := range remotes {
		if _, ok := p.entries[name]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(p.ctx)
		e := &entry{pool: p, name: name, remote: r, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), changed: make(chan struct{}), lastError: "connecting"}
		p.entries[name] = e
		if r.WingID == "" {
			e.lastError = "remote is not verified; remove it and run wt mcp connect add"
			continue
		}
		if ids[r.WingID] > 1 || r.WingID == p.opts.LocalWingID {
			e.lastError = "duplicate wing ID in remembered directory; routing refused"
			continue
		}
		p.wg.Add(1)
		go func() { defer p.wg.Done(); e.run() }()
	}
	p.notify()
	return nil
}
func (p *Pool) Close() error {
	p.closeOnce.Do(func() { p.cancel(); _ = p.watcher.Close(); p.reloadMu.Lock(); p.reloadMu.Unlock(); p.wg.Wait() })
	return nil
}
func (e *entry) run() {
	backoff := time.Second
	for {
		if e.ctx.Err() != nil {
			return
		}
		// Coalesce demand with an attempt already in progress.
		select {
		case <-e.wake:
		default:
		}
		e.mu.Lock()
		e.connecting = true
		e.mu.Unlock()
		ctx, cancel := context.WithTimeout(e.ctx, e.pool.opts.ConnectTimeout)
		conn, err := e.pool.opts.Dial(ctx, e.remote, e.pool.opts.Hello)
		cancel()
		e.mu.Lock()
		e.connecting = false
		e.attempts++
		if err != nil {
			e.lastError = err.Error()
		} else {
			e.connection = conn
			e.lastError = ""
		}
		e.signal()
		e.mu.Unlock()
		if err == nil {
			backoff = time.Second
			select {
			case <-e.ctx.Done():
			case <-conn.Done():
			}
			_ = conn.Close()
			e.mu.Lock()
			if e.connection == conn {
				e.connection = nil
				e.lastError = "SSH wing control disconnected"
				e.signal()
			}
			e.mu.Unlock()
		}
		if e.ctx.Err() != nil {
			return
		}
		delay := max(time.Second, min(60*time.Second, e.pool.opts.Jitter(backoff)))
		timer := e.pool.opts.NewTimer(delay)
		select {
		case <-e.ctx.Done():
			timer.Stop()
			return
		case <-e.wake:
		case <-timer.C():
		}
		timer.Stop()
		backoff = min(60*time.Second, backoff*2)
	}
}
func (e *entry) acquire(ctx context.Context) (Connection, error) {
	e.mu.Lock()
	if e.connection != nil {
		select {
		case <-e.connection.Done():
		default:
			conn := e.connection
			e.mu.Unlock()
			return conn, nil
		}
	}
	version := e.attempts
	connecting := e.connecting
	e.mu.Unlock()
	if !connecting {
		select {
		case e.wake <- struct{}{}:
		default:
		}
	}
	for {
		e.mu.Lock()
		conn, changed, err, attempts := e.connection, e.changed, e.lastError, e.attempts
		e.mu.Unlock()
		if conn != nil {
			select {
			case <-conn.Done():
			default:
				return conn, nil
			}
		}
		if attempts > version {
			return nil, fmt.Errorf("remote %s is offline: %s", e.name, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.ctx.Done():
			return nil, errors.New("remote removed from directory")
		case <-changed:
		}
	}
}

func (e *entry) discard(conn Connection) {
	// Call can observe transport failure before the Done notifier runs. Detach
	// that exact connection before retrying; never evict a newer replacement.
	e.mu.Lock()
	if e.connection == conn {
		e.connection = nil
		e.lastError = "SSH wing control disconnected"
		e.signal()
	}
	e.mu.Unlock()
	_ = conn.Close()
}
func (p *Pool) Entries() ([]map[string]any, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	names := make([]string, 0, len(p.entries))
	for name := range p.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		e := p.entries[name]
		e.mu.Lock()
		online := e.connection != nil
		if online {
			select {
			case <-e.connection.Done():
				online = false
			default:
			}
		}
		row := map[string]any{"wing_id": e.remote.WingID, "name": name, "hostname": e.remote.SSHTarget, "online": online, "last_error": e.lastError, "mcp_control": e.remote.WingID != "", "mcp_transport": "ssh-socket", "wingthing_dir": e.remote.WingthingDir}
		if !online && row["last_error"] == "" {
			row["last_error"] = "SSH wing control disconnected"
		}
		e.mu.Unlock()
		rows = append(rows, row)
	}
	return rows, p.registryError
}
func (p *Pool) Resolve(name string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[name]
	if e == nil {
		return "", fmt.Errorf("unknown remote %q", name)
	}
	if e.remote.WingID == "" {
		return "", fmt.Errorf("remote %q is not verified; remove it and run wt mcp connect add", name)
	}
	return e.remote.WingID, nil
}
func (p *Pool) target(wingID string) (*entry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var found *entry
	for _, e := range p.entries {
		if e.remote.WingID == wingID {
			if found != nil {
				return nil, errors.New("duplicate wing ID; routing refused")
			}
			found = e
		}
	}
	if found == nil {
		return nil, fmt.Errorf("unknown wing_id %q", wingID)
	}
	if wingID == p.opts.LocalWingID {
		return nil, errors.New("remote wing ID matches local wing; routing refused")
	}
	return found, nil
}

// UnknownOutcome means delivery may have succeeded. Only the original key may
// reconcile admission; the caller must not launch a replacement request.
type UnknownOutcome struct {
	WingID, Tool, Key string
	Err               error
}

func (e *UnknownOutcome) Error() string {
	if e.Key == "" {
		return fmt.Sprintf("%s outcome unknown on wing %s; read its authoritative state before retrying: %v", e.Tool, e.WingID, e.Err)
	}
	return fmt.Sprintf("%s outcome unknown on wing %s; reconcile with the original idempotency_key %q: %v", e.Tool, e.WingID, e.Key, e.Err)
}
func (e *UnknownOutcome) Unwrap() error { return e.Err }
func AdmissionArguments(args json.RawMessage) (json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil || fields == nil {
		return nil, "", errors.New("tool arguments must be an object")
	}
	var key string
	if raw, ok := fields["idempotency_key"]; ok {
		if err := json.Unmarshal(raw, &key); err != nil {
			return nil, "", errors.New("idempotency_key must be a string")
		}
	}
	if key == "" {
		key = uuid.NewString()
		fields["idempotency_key"], _ = json.Marshal(key)
	}
	wire, err := json.Marshal(fields)
	return wire, key, err
}
func (p *Pool) Call(ctx context.Context, wingID, name string, args json.RawMessage) (map[string]any, bool, error) {
	tool, ok := control.Lookup(name)
	if !ok {
		tool, ok = control.MCPTaskTool(name)
	}
	if !ok || !tool.Supports(control.SurfaceLocalMCP) {
		return nil, true, fmt.Errorf("unknown tool %q", name)
	}
	if name != control.MCPTaskResult {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.opts.CallTimeout)
		defer cancel()
	}
	e, err := p.target(wingID)
	if err != nil {
		return nil, true, err
	}
	key := ""
	if name == "agent_run" || name == "agent_steer" {
		args, key, err = AdmissionArguments(args)
		if err != nil {
			return nil, true, err
		}
	}
	if name == control.MCPTaskCreate {
		args, key, err = control.TaskAdmissionArguments(args, AdmissionArguments)
		if err != nil {
			return nil, true, err
		}
	}
	delivered := false
	retry := tool.Annotations["readOnlyHint"] == true || name == "agent_run" || name == "agent_steer" || name == control.MCPTaskCreate
	for attempt := 0; attempt < 2; attempt++ {
		conn, connectErr := e.acquire(ctx)
		if connectErr != nil {
			err = connectErr
			break
		}
		delivered = true
		data, denied, callErr := conn.Call(ctx, name, args)
		if callErr == nil {
			if key != "" {
				if data == nil {
					data = map[string]any{}
				}
				data["idempotency_key"] = key
			}
			return control.QualifyResult(wingID, data), denied, nil
		}
		var transportErr *controlsocket.TransportError
		if !errors.As(callErr, &transportErr) && ctx.Err() == nil {
			return data, denied, callErr
		}
		err = callErr
		if name == control.MCPTaskResult && ctx.Err() != nil {
			// The transport forwards request cancellation. Retain this shared
			// connection and its other observers when just one wait ends.
			return data, denied, ctx.Err()
		}
		e.discard(conn)
		if !retry || ctx.Err() != nil {
			break
		}
	}
	if delivered && tool.Annotations["readOnlyHint"] != true {
		return nil, true, &UnknownOutcome{WingID: wingID, Tool: name, Key: key, Err: err}
	}
	return nil, true, err
}
