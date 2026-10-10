package wingconnect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testssh"
)

type fakeTimer struct {
	channel chan time.Time
	delay   time.Duration
}

func (t *fakeTimer) C() <-chan time.Time { return t.channel }
func (t *fakeTimer) Stop()               {}

type fakeClock struct{ timers chan *fakeTimer }

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	t := &fakeTimer{channel: make(chan time.Time, 1), delay: d}
	c.timers <- t
	return t
}
func fakeWing(t *testing.T, h *testssh.Harness, name, id string, handler controlsocket.Handler) (config.Remote, *controlsocket.Server) {
	t.Helper()
	dir := filepath.Join(h.Root, name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if handler == nil {
		handler = func(_ context.Context, r control.DirectRequest) control.DirectResponse {
			return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"tool": r.Tool, "host": name}}
		}
	}
	s, err := controlsocket.Listen(t.Context(), dir, id, func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, handler, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := sshcontrol.InspectLocal(t.Context(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, name, meta)
	return config.Remote{SSHTarget: name, WingthingDir: dir, WingID: id, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}, s
}
func waitEntries(t *testing.T, p *Pool, changes <-chan struct{}, predicate func([]map[string]any) bool) []map[string]any {
	t.Helper()
	for {
		rows, _ := p.Entries()
		if predicate(rows) {
			return rows
		}
		select {
		case <-changes:
		case <-t.Context().Done():
			t.Fatal("directory barrier canceled")
		}
	}
}
func newTestPool(t *testing.T, h *testssh.Harness, dir string) (*Pool, chan struct{}, *fakeClock) {
	t.Helper()
	changes := make(chan struct{}, 64)
	clock := &fakeClock{timers: make(chan *fakeTimer, 64)}
	p, err := New(t.Context(), Options{Dir: dir, LocalWingID: "local-wing", Transport: sshcontrol.Transport{SSHPath: h.SSHPath, SocketDir: h.Root}, NewTimer: clock.NewTimer, Jitter: func(d time.Duration) time.Duration { return d }, OnChange: func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, changes, clock
}
func TestRememberedPoolLiveReloadDropAndBackoffReconnect(t *testing.T) {
	h := testssh.New(t)
	state := filepath.Join(h.Root, "registry")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	one, s1 := fakeWing(t, h, "one", "wing-one", nil)
	defer s1.Close()
	two, s2 := fakeWing(t, h, "two", "wing-two", nil)
	defer s2.Close()
	p, changes, clock := newTestPool(t, h, state)
	if err := config.SaveRemotes(state, map[string]config.Remote{"one": one, "two": two}); err != nil {
		t.Fatal(err)
	}
	waitEntries(t, p, changes, func(rows []map[string]any) bool {
		return len(rows) == 2 && rows[0]["online"] == true && rows[1]["online"] == true
	})
	forwards := map[string]*testssh.Forward{}
	for range 2 {
		f := <-h.Started
		forwards[f.Host] = f
	}
	for _, id := range []string{"wing-one", "wing-two"} {
		data, denied, err := p.Call(t.Context(), id, "terminal_list", json.RawMessage(`{}`))
		if err != nil || denied || data["wing_id"] != id {
			t.Fatalf("routing: %v %v", data, err)
		}
	}
	h.Offline(t, "one", true)
	forwards["one"].Drop()
	rows := waitEntries(t, p, changes, func(rows []map[string]any) bool { return len(rows) == 2 && rows[0]["online"] == false })
	if rows[0]["last_error"] == "" || rows[1]["online"] != true {
		t.Fatalf("offline directory: %v", rows)
	}
	timer := <-clock.timers
	if timer.delay != time.Second {
		t.Fatalf("initial backoff: %v", timer.delay)
	}
	if _, _, err := p.Call(t.Context(), "wing-two", "terminal_list", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("healthy peer stalled: %v", err)
	}
	h.Offline(t, "one", false)
	timer.channel <- time.Unix(1, 0)
	waitEntries(t, p, changes, func(rows []map[string]any) bool { return rows[0]["online"] == true })
	if _, _, err := p.Call(t.Context(), "wing-one", "terminal_list", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := config.UpdateRemotes(state, func(remotes map[string]config.Remote) error { delete(remotes, "one"); return nil }); err != nil {
		t.Fatal(err)
	}
	waitEntries(t, p, changes, func(rows []map[string]any) bool { return len(rows) == 1 && rows[0]["name"] == "two" })
	if _, _, err := p.Call(t.Context(), "wing-one", "terminal_list", json.RawMessage(`{}`)); err == nil {
		t.Fatal("removed wing still routes")
	}
}
func TestRememberedPoolHostAndWingIdentityChangesFailClosed(t *testing.T) {
	h := testssh.New(t)
	one, s := fakeWing(t, h, "one", "wing-one", nil)
	defer s.Close()
	state := filepath.Join(h.Root, "registry")
	if err := config.SaveRemotes(state, map[string]config.Remote{"one": one}); err != nil {
		t.Fatal(err)
	}
	p, changes, _ := newTestPool(t, h, state)
	waitEntries(t, p, changes, func(rows []map[string]any) bool { return rows[0]["online"] == true })
	f := <-h.Started
	changedKey := filepath.Join(h.Root, "one.changed-key")
	if err := os.WriteFile(changedKey, nil, 0600); err != nil {
		t.Fatal(err)
	}
	f.Drop()
	waitEntries(t, p, changes, func(rows []map[string]any) bool { return rows[0]["online"] == false })
	if _, _, err := p.Call(t.Context(), one.WingID, "terminal_list", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("host identity change: %v", err)
	}
	if err := os.Remove(changedKey); err != nil {
		t.Fatal(err)
	}
	s.Close()
	replacement, err := controlsocket.Listen(t.Context(), one.WingthingDir, "replacement-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if _, _, err := p.Call(t.Context(), one.WingID, "terminal_list", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "wing ID") {
		t.Fatalf("wing identity change: %v", err)
	}
	rows, _ := p.Entries()
	if len(rows) != 1 || rows[0]["wing_id"] != one.WingID || rows[0]["online"] != false {
		t.Fatalf("pin changed: %v", rows)
	}
}
func TestRememberedPoolLostAdmissionReconcilesOriginalKey(t *testing.T) {
	h := testssh.New(t)
	var mu sync.Mutex
	keys := map[string]string{}
	calls := 0
	admitted := make(chan struct{})
	release := make(chan struct{})
	handler := func(ctx context.Context, r control.DirectRequest) control.DirectResponse {
		var args struct {
			Key string `json:"idempotency_key"`
		}
		if err := json.Unmarshal(r.Arguments, &args); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		calls++
		first := calls == 1
		if keys[args.Key] == "" {
			keys[args.Key] = "single-egg"
		}
		id := keys[args.Key]
		mu.Unlock()
		if first {
			close(admitted)
			select {
			case <-ctx.Done():
			case <-release:
			}
		}
		return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"run_id": "single-run", "session_id": id}}
	}
	one, s := fakeWing(t, h, "one", "wing-one", handler)
	defer s.Close()
	state := filepath.Join(h.Root, "registry")
	if err := config.SaveRemotes(state, map[string]config.Remote{"one": one}); err != nil {
		t.Fatal(err)
	}
	p, changes, _ := newTestPool(t, h, state)
	waitEntries(t, p, changes, func(rows []map[string]any) bool { return rows[0]["online"] == true })
	f := <-h.Started
	type result struct {
		data map[string]any
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, _, err := p.Call(t.Context(), one.WingID, "agent_run", json.RawMessage(`{"prompt":"request","idempotency_key":"original"}`))
		done <- result{data, err}
	}()
	<-admitted
	f.Drop()
	close(release)
	got := <-done
	if got.err != nil || got.data["session_id"] != "single-egg" || got.data["idempotency_key"] != "original" {
		t.Fatalf("reconciliation: %v %v", got.data, got.err)
	}
	again, _, err := p.Call(t.Context(), one.WingID, "agent_run", json.RawMessage(`{"prompt":"request","idempotency_key":"original"}`))
	if err != nil || again["run_id"] != got.data["run_id"] {
		t.Fatalf("explicit retry: %v %v", again, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 1 || calls != 3 {
		t.Fatalf("duplicated admission: %v calls=%d", keys, calls)
	}
}
func TestRememberedPoolBackoffBoundsAndDemandRetry(t *testing.T) {
	h := testssh.New(t)
	r := config.Remote{SSHTarget: "one", WingID: "wing", WingthingDir: "/state", ControlSocket: "/s", ControlVersion: control.ContractVersion}
	if err := config.SaveRemotes(h.Root, map[string]config.Remote{"one": r}); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{timers: make(chan *fakeTimer, 64)}
	attempts := make(chan struct{}, 64)
	p, err := New(t.Context(), Options{Dir: h.Root, NewTimer: clock.NewTimer, Jitter: func(d time.Duration) time.Duration { return d }, Dial: func(context.Context, config.Remote, controlsocket.Hello) (Connection, error) {
		attempts <- struct{}{}
		return nil, errors.New("offline")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for index, want := range []time.Duration{1, 2, 4, 8, 16, 32, 60, 60} {
		<-attempts
		timer := <-clock.timers
		if timer.delay != want*time.Second {
			t.Fatalf("backoff=%v want=%vs", timer.delay, want)
		}
		if index < 7 {
			timer.channel <- time.Unix(1, 0)
		}
	}
	// Demand wakes the suspended retry without advancing the fake clock.
	if _, _, err := p.Call(t.Context(), "wing", "terminal_list", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("demand retry: %v", err)
	}
	<-attempts
}

func TestRememberedPoolUnreconciledAdmissionReportsOriginalKey(t *testing.T) {
	h := testssh.New(t)
	admitted := make(chan struct{})
	one, s := fakeWing(t, h, "one", "wing-one", func(ctx context.Context, r control.DirectRequest) control.DirectResponse {
		close(admitted)
		<-ctx.Done()
		return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"session_id": "one-egg"}}
	})
	defer s.Close()
	state := filepath.Join(h.Root, "registry")
	if err := config.SaveRemotes(state, map[string]config.Remote{"one": one}); err != nil {
		t.Fatal(err)
	}
	p, changes, _ := newTestPool(t, h, state)
	waitEntries(t, p, changes, func(rows []map[string]any) bool { return rows[0]["online"] == true })
	f := <-h.Started
	result := make(chan error, 1)
	go func() {
		_, _, err := p.Call(t.Context(), one.WingID, "agent_run", json.RawMessage(`{"prompt":"request","idempotency_key":"original-key"}`))
		result <- err
	}()
	<-admitted
	h.Offline(t, "one", true)
	f.Drop()
	var unknown *UnknownOutcome
	if err := <-result; !errors.As(err, &unknown) || unknown.Key != "original-key" || unknown.WingID != one.WingID {
		t.Fatalf("unknown admission lost its key: %v", err)
	}
}
func TestRememberedPoolReadRetryPreservesCursor(t *testing.T) {
	h := testssh.New(t)
	first := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var arguments []string
	one, s := fakeWing(t, h, "one", "wing-one", func(ctx context.Context, r control.DirectRequest) control.DirectResponse {
		mu.Lock()
		arguments = append(arguments, string(r.Arguments))
		isFirst := len(arguments) == 1
		mu.Unlock()
		if isFirst {
			once.Do(func() { close(first) })
			<-ctx.Done()
		}
		return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"cursor": 42}}
	})
	defer s.Close()
	state := filepath.Join(h.Root, "registry")
	if err := config.SaveRemotes(state, map[string]config.Remote{"one": one}); err != nil {
		t.Fatal(err)
	}
	p, changes, _ := newTestPool(t, h, state)
	waitEntries(t, p, changes, func(rows []map[string]any) bool { return rows[0]["online"] == true })
	f := <-h.Started
	done := make(chan error, 1)
	wire := json.RawMessage(`{"run_id":"run","after_seq":41}`)
	go func() {
		data, denied, err := p.Call(t.Context(), one.WingID, "agent_events", wire)
		if err == nil && (denied || data["cursor"] != float64(42)) {
			err = fmt.Errorf("bad read: %v", data)
		}
		done <- err
	}()
	<-first
	f.Drop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(arguments) != 2 || arguments[0] != string(wire) || arguments[1] != string(wire) {
		t.Fatalf("replay changed cursor: %v", arguments)
	}
}
func TestRememberedPoolCanceledCallIsBounded(t *testing.T) {
	h := testssh.New(t)
	entered := make(chan struct{})
	one, s := fakeWing(t, h, "one", "wing-one", func(ctx context.Context, r control.DirectRequest) control.DirectResponse {
		close(entered)
		<-ctx.Done()
		return control.DirectResponse{Version: control.ContractVersion, ID: r.ID}
	})
	defer s.Close()
	state := filepath.Join(h.Root, "registry")
	if err := config.SaveRemotes(state, map[string]config.Remote{"one": one}); err != nil {
		t.Fatal(err)
	}
	p, changes, _ := newTestPool(t, h, state)
	waitEntries(t, p, changes, func(rows []map[string]any) bool { return rows[0]["online"] == true })
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, _, err := p.Call(ctx, one.WingID, "agent_wait", json.RawMessage(`{"run_id":"held"}`))
		result <- err
	}()
	<-entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation did not bound a held call: %v", err)
	}
}
