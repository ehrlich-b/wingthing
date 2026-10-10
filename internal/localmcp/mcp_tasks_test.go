package localmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/control"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func newTaskWingFixture(t *testing.T) *runWingFixture {
	t.Helper()
	root, err := filepath.Abs("../../.scratch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, "task-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return newRunWingFixture(t, &Server{Version: "test", Cfg: &config.Config{Dir: dir, DefaultAgent: "codex"}, Principal: "owner", Logs: io.Discard})
}

func taskRPC(t *testing.T, handle func(context.Context, localMCPRequest) (localMCPResponse, bool), method string, params any) localMCPResponse {
	t.Helper()
	wire, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	response, respond := handle(t.Context(), localMCPRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method, Params: wire})
	if !respond {
		t.Fatal("RPC did not respond")
	}
	return response
}

func taskData(t *testing.T, response localMCPResponse) map[string]any {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("RPC error: %+v", response.Error)
	}
	wire, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(wire, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func startTask(t *testing.T, f *runWingFixture, key string) string {
	t.Helper()
	data := taskData(t, taskRPC(t, f.server.handle, "tools/call", map[string]any{"name": "agent_run", "arguments": map[string]any{"agent": "codex", "prompt": "native task", "cwd": f.server.Cfg.Dir, "idempotency_key": key}, "task": map[string]any{"ttl": 60000}}))
	task := data["task"].(map[string]any)
	if task["status"] != "working" || task["createdAt"] == "" || task["lastUpdatedAt"] == "" || task["pollInterval"] != float64(5000) {
		t.Fatalf("initial task: %v", data)
	}
	if data["content"] != nil {
		t.Fatal("CreateTaskResult contains tool result")
	}
	id := task["taskId"].(string)
	if data["_meta"].(map[string]any)[control.MCPRelatedTask].(map[string]any)["taskId"] != id {
		t.Fatal("missing related task metadata")
	}
	return id
}

func TestMCPTasksVersionNegotiationAndToolMarkers(t *testing.T) {
	f := newTaskWingFixture(t)
	socket, err := controlsocket.Listen(t.Context(), f.server.Cfg.Dir, "fixture", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, f.server.handleDirectRequest, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	client, err := controlsocket.Dial(t.Context(), f.server.Cfg.Dir, controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	proxy := &localWingProxy{client: client}
	connect := &ConnectMCPServer{Version: "test"}
	for name, handle := range map[string]func(context.Context, localMCPRequest) (localMCPResponse, bool){"server": f.server.handle, "proxy": proxy.handle, "connect": connect.handle} {
		for _, version := range []string{"2025-06-18", "2025-11-25"} {
			t.Run(name+"/"+version, func(t *testing.T) {
				data := taskData(t, taskRPC(t, handle, "initialize", map[string]any{"protocolVersion": version}))
				if data["protocolVersion"] != version {
					t.Fatalf("version: %v", data)
				}
				caps := data["capabilities"].(map[string]any)
				if (caps["tasks"] != nil) != (version == localMCPProtocolVersion) {
					t.Fatalf("capabilities: %v", caps)
				}
				tools := taskData(t, taskRPC(t, handle, "tools/list", map[string]any{}))["tools"].([]any)
				for _, value := range tools {
					tool := value.(map[string]any)
					if tool["name"] == "agent_run" && version == localMCPProtocolVersion {
						if tool["execution"].(map[string]any)["taskSupport"] != "optional" {
							t.Fatalf("marker: %v", tool)
						}
					} else if tool["execution"] != nil {
						t.Fatalf("unexpected marker: %v", tool)
					}
				}
			})
		}
	}
}

func TestMCPTaskLifecycleResultParityAndIsolation(t *testing.T) {
	for _, kind := range []agent.ErrorKind{"", agent.ProviderError} {
		t.Run(string(kind), func(t *testing.T) {
			f := newTaskWingFixture(t)
			id := startTask(t, f, "one")
			if got := <-f.submitted; got != id {
				t.Fatal("wrong run")
			}
			get := taskData(t, taskRPC(t, f.server.handle, "tasks/get", map[string]any{"taskId": id, "_meta": relatedTask("wrong")}))
			if get["status"] != "working" || get["taskId"] != id {
				t.Fatalf("working: %v", get)
			}
			// Transport metadata never becomes part of the tool's strict schema.
			if run, err := f.service.RunManager.Get(f.server.sessionAuthority(), id); err != nil || run.TimeoutSeconds != 0 || !run.Result.Deadline.IsZero() {
				t.Fatalf("ttl changed timeout: %v %v", run, err)
			}
			stranger := &Server{Version: "test", Cfg: f.server.Cfg, Sessions: f.service, Principal: "stranger", identity: f.server.identity}
			for _, method := range []string{"tasks/get", "tasks/result", "tasks/cancel"} {
				response := taskRPC(t, stranger.handle, method, map[string]any{"taskId": id})
				if response.Error == nil || response.Error.Code != -32602 {
					t.Fatalf("%s disclosed task: %+v", method, response)
				}
			}
			if tasks := taskData(t, taskRPC(t, stranger.handle, "tasks/list", map[string]any{}))["tasks"].([]any); len(tasks) != 0 {
				t.Fatalf("foreign list: %v", tasks)
			}
			f.finish(t, id, "Fake Codex task Ω🙂", kind)
			f.wait(t, id)
			get = taskData(t, taskRPC(t, f.server.handle, "tasks/get", map[string]any{"taskId": id}))
			wantStatus := "completed"
			if kind != "" {
				wantStatus = "failed"
			}
			if get["status"] != wantStatus {
				t.Fatalf("terminal: %v", get)
			}
			result := taskData(t, taskRPC(t, f.server.handle, "tasks/result", map[string]any{"taskId": id}))
			plain := taskData(t, taskRPC(t, f.server.handle, "tools/call", map[string]any{"name": "agent_result", "arguments": map[string]any{"run_id": id}}))
			delete(result, "_meta")
			if !reflect.DeepEqual(result, plain) {
				t.Fatalf("result differs: %v vs %v", result, plain)
			}
			if response := taskRPC(t, f.server.handle, "tasks/cancel", map[string]any{"taskId": id}); response.Error == nil || response.Error.Code != -32602 {
				t.Fatalf("cancelled terminal task: %+v", response)
			}
		})
	}
}

func TestMCPTasksPaginationAndLegacyCalls(t *testing.T) {
	f := newTaskWingFixture(t)
	seen := map[string]bool{}
	for i := 0; i < control.MCPTaskPageSize+1; i++ {
		id := startTask(t, f, fmt.Sprint(i))
		<-f.submitted
		seen[id] = true
	}
	page := taskData(t, taskRPC(t, f.server.handle, "tasks/list", map[string]any{}))
	if len(page["tasks"].([]any)) != control.MCPTaskPageSize {
		t.Fatalf("page: %v", page)
	}
	last := taskData(t, taskRPC(t, f.server.handle, "tasks/list", map[string]any{"cursor": page["nextCursor"]}))
	if len(last["tasks"].([]any)) != 1 || last["nextCursor"] != nil {
		t.Fatalf("last: %v", last)
	}
	for _, p := range []map[string]any{page, last} {
		for _, value := range p["tasks"].([]any) {
			id := value.(map[string]any)["taskId"].(string)
			if !seen[id] {
				t.Fatalf("duplicate/unknown task %s", id)
			}
			delete(seen, id)
		}
	}
	if len(seen) != 0 {
		t.Fatal("task missing from pagination")
	}
	if response := taskRPC(t, f.server.handle, "tasks/list", map[string]any{"cursor": "bad"}); response.Error == nil || response.Error.Code != -32602 {
		t.Fatal("invalid cursor accepted")
	}
	for _, params := range []map[string]any{
		{"name": "agent_start", "task": map[string]any{}},
		{"name": "agent_run", "arguments": map[string]any{"prompt": "bad ttl"}, "task": map[string]any{"ttl": -1}},
	} {
		if response := taskRPC(t, f.server.handle, "tools/call", params); response.Error == nil {
			t.Fatalf("invalid task admitted: %+v", response)
		}
	}
	// Old clients keep ordinary admission envelopes even with augmentation.
	taskRPC(t, f.server.handle, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	data := taskData(t, taskRPC(t, f.server.handle, "tools/call", map[string]any{"name": "agent_run", "arguments": map[string]any{"prompt": "legacy", "cwd": f.server.Cfg.Dir}, "task": map[string]any{"ttl": 1}}))
	if data["task"] != nil || data["structuredContent"] == nil {
		t.Fatalf("legacy admission changed: %v", data)
	}
	id := data["structuredContent"].(map[string]any)["run_id"].(string)
	<-f.submitted
	if _, err := f.service.RunManager.Task(f.server.sessionAuthority(), id); err == nil {
		t.Fatal("legacy call created a task")
	}
}

func TestMCPTaskCancelStopsRunAndFreezesResult(t *testing.T) {
	f := newTaskWingFixture(t)
	id := startTask(t, f, "cancel")
	<-f.submitted
	data := taskData(t, taskRPC(t, f.server.handle, "tasks/cancel", map[string]any{"taskId": id}))
	if data["status"] != "cancelled" {
		t.Fatalf("cancel: %v", data)
	}
	first := taskData(t, taskRPC(t, f.server.handle, "tasks/result", map[string]any{"taskId": id}))
	f.wait(t, id)
	run, err := f.service.RunManager.Get(f.server.sessionAuthority(), id)
	if err != nil || run.Result.Status != "stopped" {
		t.Fatalf("egg not stopped: %v %v", run, err)
	}
	second := taskData(t, taskRPC(t, f.server.handle, "tasks/result", map[string]any{"taskId": id}))
	if !reflect.DeepEqual(first, second) || first["structuredContent"].(map[string]any)["ready"] != true {
		t.Fatalf("unstable cancellation result: %v %v", first, second)
	}
	if response := taskRPC(t, f.server.handle, "tasks/cancel", map[string]any{"taskId": id}); response.Error == nil || response.Error.Code != -32602 {
		t.Fatal("cancelled terminal task twice")
	}
}

func TestMCPTaskGrantsAndPathIsolation(t *testing.T) {
	f := newTaskWingFixture(t)
	id := startTask(t, f, "grants")
	<-f.submitted
	denied := &Server{Version: "test", Cfg: f.server.Cfg, Sessions: f.service, Principal: f.server.Principal, identity: f.server.identity, Grants: map[string]bool{}}
	for _, method := range []string{"tasks/get", "tasks/result", "tasks/list", "tasks/cancel"} {
		params := map[string]any{"taskId": id}
		if method == "tasks/list" {
			params = map[string]any{}
		}
		response := taskRPC(t, denied.handle, method, params)
		if response.Error == nil || response.Error.Code != -32602 {
			t.Fatalf("%s bypassed grants: %+v", method, response)
		}
	}
	response := taskRPC(t, denied.handle, "tools/call", map[string]any{"name": "agent_run", "arguments": map[string]any{"prompt": "denied"}, "task": map[string]any{}})
	if response.Error == nil {
		t.Fatal("task admission bypassed grant")
	}
	denied.Grants = nil
	denied.enforcePathBounds = true
	denied.allowedPaths = []string{filepath.Join(f.server.Cfg.Dir, "other")}
	response = taskRPC(t, denied.handle, "tasks/get", map[string]any{"taskId": id})
	if response.Error == nil {
		t.Fatal("path-bound caller read outside its paths")
	}
	tasks := taskData(t, taskRPC(t, denied.handle, "tasks/list", map[string]any{}))["tasks"].([]any)
	if len(tasks) != 0 {
		t.Fatalf("path-bound list leaked tasks: %v", tasks)
	}
}

func TestMCPTaskCancellationRecoversStopIntentAfterWingRestart(t *testing.T) {
	f := newTaskWingFixture(t)
	id := startTask(t, f, "cancel-restart")
	<-f.submitted
	if err := f.service.RunManager.Close(); err != nil {
		t.Fatal(err)
	}
	f.service.RunManager = nil
	stop := f.service.RunBackend.Stop
	entered := make(chan struct{}, 8)
	f.service.RunBackend.Stop = func(ctx context.Context, _ *config.Config, _ eggclient.LocalSession, _ string) (egg.RunTurnResult, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return egg.RunTurnResult{}, ctx.Err()
	}
	if err := f.service.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	cancelled := taskData(t, taskRPC(t, f.server.handle, "tasks/cancel", map[string]any{"taskId": id}))
	if cancelled["status"] != "cancelled" {
		t.Fatalf("cancel: %v", cancelled)
	}
	<-entered // The wing exits before its stop RPC can acknowledge termination.
	before := taskData(t, taskRPC(t, f.server.handle, "tasks/result", map[string]any{"taskId": id}))
	if err := f.service.RunManager.Close(); err != nil {
		t.Fatal(err)
	}
	f.service.RunManager = nil
	f.service.RunBackend.Stop = stop
	if err := f.service.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.wait(t, id)
	run, err := f.service.RunManager.Get(f.server.sessionAuthority(), id)
	if err != nil || run.Result.Status != "stopped" {
		t.Fatalf("restart lost stop intent: %v %v", run, err)
	}
	after := taskData(t, taskRPC(t, f.server.handle, "tasks/result", map[string]any{"taskId": id}))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("restart changed cancelled result: %v %v", before, after)
	}
}

func TestMCPTaskStdioRequestCancellationAndWingRestart(t *testing.T) {
	f := newTaskWingFixture(t)
	id := startTask(t, f, "reconnect")
	<-f.submitted
	entered, observerDone := make(chan struct{}), make(chan struct{})
	var firstWait atomic.Bool
	socket, err := controlsocket.Listen(t.Context(), f.server.Cfg.Dir, "fixture", func(controlsocket.Hello) (controlsocket.Welcome, controlsocket.Handler, error) {
		return controlsocket.Welcome{}, func(ctx context.Context, request control.DirectRequest) control.DirectResponse {
			if request.Tool == control.MCPTaskResult && firstWait.CompareAndSwap(false, true) {
				close(entered)
				defer close(observerDone)
			}
			return f.server.handleDirectRequest(ctx, request)
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	client, err := controlsocket.Dial(t.Context(), f.server.Cfg.Dir, controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	proxy := &localWingProxy{client: client}
	input, send := io.Pipe()
	receive, output := io.Pipe()
	defer input.Close()
	defer send.Close()
	defer receive.Close()
	defer output.Close()
	done := make(chan error, 1)
	go func() {
		done <- serveStdio(t.Context(), input, output, proxy.handle)
	}()
	encoder := json.NewEncoder(send)
	decoder := json.NewDecoder(receive)
	encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tasks/result", "params": map[string]any{"taskId": id}})
	<-entered
	// A blocked result must permit another request and request cancellation.
	encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tasks/get", "params": map[string]any{"taskId": id}})
	var response localMCPResponse
	if err := decoder.Decode(&response); err != nil || string(response.ID) != "2" || response.Error != nil {
		t.Fatalf("get blocked behind result: %+v %v", response, err)
	}
	encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 1}})
	if err := decoder.Decode(&response); err != nil || string(response.ID) != "1" || response.Error == nil {
		t.Fatalf("wait cancellation: %+v %v", response, err)
	}
	<-observerDone // Request cancellation crossed the socket without closing it.
	send.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if task, err := f.service.RunManager.Task(f.server.sessionAuthority(), id); err != nil || task.Status != "working" {
		t.Fatalf("wait cancelled execution: %+v %v", task, err)
	}
	if err := f.service.RunManager.Close(); err != nil {
		t.Fatal(err)
	}
	f.service.RunManager = nil
	if err := f.service.StartRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	client.Close()
	client, err = controlsocket.Dial(t.Context(), f.server.Cfg.Dir, controlsocket.Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	proxy = &localWingProxy{client: client}
	get := taskData(t, taskRPC(t, proxy.handle, "tasks/get", map[string]any{"taskId": id}))
	if get["status"] != "working" {
		t.Fatalf("restart lost task: %v", get)
	}
	f.finish(t, id, "survived client exit and wing restart", "")
	f.wait(t, id)
	data := taskData(t, taskRPC(t, proxy.handle, "tasks/result", map[string]any{"taskId": id}))
	if data["structuredContent"].(map[string]any)["output"] != "survived client exit and wing restart" {
		t.Fatalf("restart result: %v", data)
	}
}

func TestMCPTasksDirectConnectorRoutesTwoWings(t *testing.T) {
	tunnel := newDirectConnectorTestTunnel(t)
	fixtures := map[string]*runWingFixture{}
	for wingID, wing := range tunnel.wings {
		f := newTaskWingFixture(t)
		f.server.Principal = roostSessionPrincipal("owner-user")
		f.server.identity.UserID = "owner-user"
		wing.cfg = f.server.Cfg
		wing.admission.Sessions = f.service
		fixtures[wingID] = f
	}
	connector := &ConnectMCPServer{Version: "test", Actor: "fixture", Tunnel: tunnel, Timeout: 10 * time.Second}
	defer connector.Close()
	ids := map[string]string{}
	for _, wingID := range []string{"home", "office"} {
		data := taskData(t, taskRPC(t, connector.handle, "tools/call", map[string]any{"name": "agent_run", "arguments": map[string]any{"wing_id": wingID, "agent": "codex", "prompt": "direct task", "cwd": fixtures[wingID].server.Cfg.Dir}, "task": map[string]any{}}))
		id := data["task"].(map[string]any)["taskId"].(string)
		owner, runID, err := control.SplitTaskID(id)
		if err != nil || owner != wingID || <-fixtures[wingID].submitted != runID {
			t.Fatalf("wrong direct owner: %s %v", id, err)
		}
		ids[wingID] = id
		get := taskData(t, taskRPC(t, connector.handle, "tasks/get", map[string]any{"taskId": id}))
		if get["status"] != "working" {
			t.Fatalf("direct task: %v", get)
		}
	}
	list := taskData(t, taskRPC(t, connector.handle, "tasks/list", map[string]any{}))
	if len(list["tasks"].([]any)) != 2 {
		t.Fatalf("direct task inventory: %v", list)
	}
	_, homeRun, _ := control.SplitTaskID(ids["home"])
	fixtures["home"].finish(t, homeRun, "direct final Ω🙂", "")
	fixtures["home"].wait(t, homeRun)
	result := taskData(t, taskRPC(t, connector.handle, "tasks/result", map[string]any{"taskId": ids["home"]}))
	plain := taskData(t, taskRPC(t, connector.handle, "tools/call", map[string]any{"name": "agent_result", "arguments": map[string]any{"wing_id": "home", "run_id": homeRun}}))
	delete(result, "_meta")
	if !reflect.DeepEqual(result, plain) {
		t.Fatalf("direct result parity: %v vs %v", result, plain)
	}
	cancelled := taskData(t, taskRPC(t, connector.handle, "tasks/cancel", map[string]any{"taskId": ids["office"]}))
	if cancelled["status"] != "cancelled" || cancelled["taskId"] != ids["office"] {
		t.Fatalf("direct cancellation: %v", cancelled)
	}
	_, officeRun, _ := control.SplitTaskID(ids["office"])
	fixtures["office"].wait(t, officeRun)
	if response := taskRPC(t, connector.handle, "tasks/cancel", map[string]any{"taskId": ids["office"]}); response.Error == nil || response.Error.Code != -32602 {
		t.Fatalf("direct protocol error lost: %+v", response)
	}
}
