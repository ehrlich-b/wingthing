package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
	"github.com/ehrlich-b/wingthing/internal/testssh"
	"github.com/ehrlich-b/wingthing/internal/wingconnect"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func TestRememberedAggregateCatalogLocalAndTwoRemoteWings(t *testing.T) {
	h := testssh.New(t)
	t.Setenv("PATH", h.Root+string(os.PathListSeparator)+os.Getenv("PATH"))
	remotes := map[string]config.Remote{}
	slowEntered, slowRelease := make(chan struct{}), make(chan struct{})
	for _, name := range []string{"one", "two"} {
		name := name
		dir := filepath.Join(h.Root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		remote, err := controlsocket.Listen(t.Context(), dir, "wing-"+name, func(hello controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
			if hello.Aggregate {
				t.Error("remote received recursive directory authority")
			}
			return controlsocket.Welcome{}, func(ctx context.Context, r control.DirectRequest) control.DirectResponse {
				if name == "one" && r.Tool == "terminal_read" {
					close(slowEntered)
					select {
					case <-ctx.Done():
					case <-slowRelease:
					}
				}
				return control.DirectResponse{Version: control.ContractVersion, ID: r.ID, Result: map[string]any{"host": name, "tool": r.Tool}}
			}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { remote.Close() })
		meta, err := sshcontrol.InspectLocal(t.Context(), dir, "")
		if err != nil {
			t.Fatal(err)
		}
		h.Host(t, name, meta)
		remotes[name] = config.Remote{SSHTarget: name, WingID: meta.WingID, WingthingDir: meta.WingthingDir, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}
	}
	state := filepath.Join(h.Root, "local")
	if err := config.SaveRemotes(state, remotes); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Dir: state, WingID: "wing-local", DefaultAgent: "claude"}
	wc := &config.WingConfig{WingID: cfg.WingID}
	service := &wingsession.Service{Config: cfg, Home: h.Root, Policy: func() wingsession.Policy { return wingsession.Policy{Wing: wc, Egg: egg.DefaultEggConfig()} }}
	listener, err := ListenLocalWingControl(t.Context(), "test", service, "owner", NewMCPAdmissionState())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := controlsocket.Dial(t.Context(), state, controlsocket.Hello{Aggregate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	proxy := &localWingProxy{version: "test", client: client, aggregate: true}
	catalog, _ := proxy.handle(t.Context(), localMCPRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"})
	if !reflect.DeepEqual(catalog.Result.(map[string]any)["tools"], control.Tools(control.SurfaceDirectMCP)) {
		t.Fatal("aggregate changed the common catalog")
	}
	call := func(name string, args string) map[string]any {
		t.Helper()
		data, denied, err := client.Call(t.Context(), name, json.RawMessage(args))
		if err != nil || denied {
			t.Fatalf("%s: %v %v", name, data, err)
		}
		return data
	}
	data := call("wing_list", `{}`)
	if data["count"] != float64(3) {
		t.Fatalf("directory: %v", data)
	}
	for _, name := range []string{"one", "two"} {
		data := call("terminal_list", `{"wing_id":"wing-`+name+`"}`)
		if data["host"] != name || data["wing_id"] != "wing-"+name {
			t.Fatalf("qualified remote routing: %v", data)
		}
	}
	for range 2 {
		<-h.Started
	}
	data = call("terminal_list", `{"wing_id":"wing-local"}`)
	if data["wing_id"] != "wing-local" {
		t.Fatalf("local routing: %v", data)
	}
	data = call("terminal_list", `{"remote":"two"}`)
	if data["wing_id"] != "wing-two" {
		t.Fatalf("legacy remote translation: %v", data)
	}
	pending := make(chan error, 1)
	go func() {
		_, _, err := client.Call(t.Context(), "terminal_read", json.RawMessage(`{"wing_id":"wing-one","session":"held"}`))
		pending <- err
	}()
	<-slowEntered
	call("terminal_list", `{"wing_id":"wing-local"}`)
	close(slowRelease)
	if err := <-pending; err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{}`, `{"wing_id":"missing"}`, `{"wing_id":"wing-one","remote":"two"}`} {
		if data, denied, err := client.Call(t.Context(), "terminal_list", json.RawMessage(args)); err == nil && !denied {
			t.Fatalf("invalid routing accepted: %v", data)
		}
	}
	wc.Paths = config.PathList{{Path: h.Root}}
	if data, denied, err := client.Call(t.Context(), "terminal_list", json.RawMessage(`{"remote":"two"}`)); !denied || !strings.Contains(fmt.Sprint(data["error"]), "bound MCP connection") {
		t.Fatalf("legacy path restriction lost: %v %v", data, err)
	}
	data = call("wing_list", `{}`)
	for _, row := range data["wings"].([]any) {
		if row.(map[string]any)["online"] != true {
			t.Fatalf("online state: %v", data)
		}
	}
}
func TestRememberedSSHDroppedAdmissionYieldsOneWingEgg(t *testing.T) {
	h := testssh.New(t)
	f := newRunWingFixture(t, nil)
	admitted, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	listener, err := controlsocket.Listen(t.Context(), f.server.Cfg.Dir, "run-wing", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, func(ctx context.Context, r control.DirectRequest) control.DirectResponse {
			response := f.server.handleDirectRequest(ctx, r)
			if first.CompareAndSwap(false, true) {
				close(admitted)
				select {
				case <-ctx.Done():
				case <-release:
				}
			}
			return response
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	meta, err := sshcontrol.InspectLocal(t.Context(), f.server.Cfg.Dir, "")
	if err != nil {
		t.Fatal(err)
	}
	h.Host(t, "remote", meta)
	state := filepath.Join(h.Root, "registry")
	if err := config.SaveRemotes(state, map[string]config.Remote{"remote": {SSHTarget: "remote", WingID: meta.WingID, WingthingDir: meta.WingthingDir, ControlSocket: meta.ControlSocket, ControlVersion: meta.Version}}); err != nil {
		t.Fatal(err)
	}
	pool, err := wingconnect.New(t.Context(), wingconnect.Options{Dir: state, Transport: sshcontrol.Transport{SSHPath: h.SSHPath, SocketDir: h.Root}})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	forward := <-h.Started
	wire, _ := json.Marshal(map[string]any{"prompt": "request", "agent": "claude", "cwd": f.server.Cfg.Dir, "idempotency_key": "original"})
	done := make(chan map[string]any, 1)
	fail := make(chan error, 1)
	go func() {
		data, _, err := pool.Call(t.Context(), meta.WingID, "agent_run", wire)
		if err != nil {
			fail <- err
		} else {
			done <- data
		}
	}()
	<-admitted
	forward.Drop()
	close(release)
	var receipt map[string]any
	select {
	case receipt = <-done:
	case err := <-fail:
		t.Fatal(err)
	}
	id := <-f.submitted
	if receipt["run_id"] != id {
		t.Fatalf("receipt changed: %v", receipt)
	}
	again, _, err := pool.Call(t.Context(), meta.WingID, "agent_run", wire)
	if err != nil || again["run_id"] != id || again["session_id"] != receipt["session_id"] {
		t.Fatalf("retry changed egg: %v %v", again, err)
	}
	opts := <-f.spawned
	if opts.SessionID != receipt["session_id"] {
		t.Fatal("wrong egg")
	}
	select {
	case duplicate := <-f.spawned:
		t.Fatalf("second egg: %v", duplicate)
	default:
	}
	f.finish(t, id, "single egg result", "")
	f.wait(t, id)
}
