package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/store"
	"github.com/ehrlich-b/wingthing/internal/wingsession"
)

func fakeAgentWaitRuns(t *testing.T, tasks ...*store.Task) (*Server, *store.Store) {
	t.Helper()
	f := newRunWingFixture(t, nil)
	if err := f.service.RunManager.Close(); err != nil {
		t.Fatal(err)
	}
	f.service.RunManager = nil
	db, err := store.Open(f.server.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, task := range tasks {
		if task.Type != "agent_run" {
			if err := db.CreateTask(task); err != nil {
				t.Fatal(err)
			}
			continue
		}
		r := &wingsession.Run{ID: task.ID, SessionID: "session-" + task.ID, Agent: "claude", CWD: f.server.Cfg.Dir, Phase: "observing", CreatedAt: time.Now().UTC(), Launch: wingsession.RunLaunch{Authority: wingsession.Authority{Principal: task.Principal, UserID: f.server.identity.UserID}}, Result: egg.RunTurnResult{RunID: task.ID, SessionID: "session-" + task.ID, Status: task.Status}}
		if r.Result.Terminal() {
			r.Phase = "terminal"
		}
		wire, _ := json.Marshal(r)
		if _, _, err := db.AdmitAgentRun(&store.AgentRun{ID: r.ID, SessionID: r.SessionID, Principal: task.Principal, Record: wire, SpecHash: r.ID}, task); err != nil {
			t.Fatal(err)
		}
		e := &fixtureRunEgg{result: r.Result, done: make(chan struct{})}
		if e.result.Terminal() {
			close(e.done)
		}
		f.eggs[r.ID] = e
	}
	f.restart(t)
	return f.server, db
}

func TestAgentWaitAnyReturnsFirstOfThreeFakeRuns(t *testing.T) {

	f := newRunWingFixture(t, nil)
	ids := []string{f.admit(t, "first"), f.admit(t, "second"), f.admit(t, "third")}
	for range ids {
		<-f.submitted
	}
	pending := append([]string{}, ids...)
	for _, id := range []string{ids[2], ids[0], ids[1]} {
		wire, _ := json.Marshal(map[string]any{"run_ids": pending})
		type response struct {
			data map[string]any
			err  error
		}
		result := make(chan response, 1)
		go func() { data, err := f.server.toolAgentWaitAny(t.Context(), wire); result <- response{data, err} }()
		f.finish(t, id, "done", "")
		got := <-result
		wantPending := []string{}
		for _, candidate := range pending {
			if candidate != id {
				wantPending = append(wantPending, candidate)
			}
		}
		if got.err != nil || !reflect.DeepEqual(got.data["finished"], []map[string]any{{"run_id": id, "status": "done"}}) || !reflect.DeepEqual(got.data["pending"], wantPending) {
			t.Fatalf("completion: %v %v", got.data, got.err)
		}
		pending = wantPending
	}

}

func TestAgentWaitAnyTimeout(t *testing.T) {
	server, _ := fakeAgentWaitRuns(t,
		&store.Task{ID: "running", Type: "agent_run", Principal: "owner", Status: "running"},
		&store.Task{ID: "foreign", Type: "agent_run", Principal: "other", Status: "done"},
	)
	data, err := server.toolAgentWaitAny(context.Background(), json.RawMessage(`{"run_ids":["running"],"timeout_seconds":0.1}`))
	want := map[string]any{"finished": []map[string]any{}, "pending": []string{"running"}}
	if err != nil || !reflect.DeepEqual(data, want) {
		t.Fatalf("timeout = %#v, %v; want %#v", data, err, want)
	}

	data, err = server.AgentWaitAny(context.Background(), json.RawMessage(`{"run_ids":["foreign","running","missing"],"timeout_seconds":0.1}`))
	if err != nil || !reflect.DeepEqual(data["finished"], want["finished"]) || !reflect.DeepEqual(data["pending"], want["pending"]) || len(data["errors"].([]map[string]any)) != 2 {
		t.Fatalf("timeout with invalid IDs = %#v, %v", data, err)
	}
}

func TestAgentWaitAnyAccepts64Runs(t *testing.T) {
	tasks := make([]*store.Task, 64)
	ids := make([]string, len(tasks))
	for index := range tasks {
		ids[index] = strings.Repeat("r", index+1)
		tasks[index] = &store.Task{ID: ids[index], Type: "agent_run", Principal: "owner", Status: "done"}
	}
	server, _ := fakeAgentWaitRuns(t, tasks...)
	arguments, err := json.Marshal(map[string]any{"run_ids": ids})
	if err != nil {
		t.Fatal(err)
	}
	data, err := server.toolAgentWaitAny(context.Background(), arguments)
	if err != nil || len(data["finished"].([]map[string]any)) != 64 || len(data["pending"].([]string)) != 0 {
		t.Fatalf("64 runs = %#v, %v", data, err)
	}
}

func TestAgentWaitAnyHidesForeignAndUnknownIDs(t *testing.T) {
	server, _ := fakeAgentWaitRuns(t,
		&store.Task{ID: "owned", Type: "agent_run", Principal: "owner", Status: "done"},
		&store.Task{ID: "foreign", Type: "agent_run", Principal: "secret-owner", Status: "failed", What: "secret prompt"},
		&store.Task{ID: "ordinary-task", Type: "prompt", Principal: "owner", Status: "done"},
	)
	data, err := server.toolAgentWaitAny(context.Background(), json.RawMessage(`{"run_ids":["foreign","unknown","ordinary-task","owned"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data["finished"], []map[string]any{{"run_id": "owned", "status": "done"}}) || !reflect.DeepEqual(data["pending"], []string{}) {
		t.Fatalf("foreign IDs included in results: %#v", data)
	}
	lookupErrors := data["errors"].([]map[string]any)
	if len(lookupErrors) != 3 {
		t.Fatalf("errors = %#v", lookupErrors)
	}
	for index, id := range []string{"foreign", "unknown", "ordinary-task"} {
		want := map[string]any{"run_id": id, "error": `agent run "` + id + `" not found or not owned by caller`}
		if !reflect.DeepEqual(lookupErrors[index], want) {
			t.Fatalf("lookup error = %#v, want %#v", lookupErrors[index], want)
		}
	}
	encoded, _ := json.Marshal(data)
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("foreign metadata leaked: %s", encoded)
	}
	data, err = server.toolAgentWaitAny(context.Background(), json.RawMessage(`{"run_ids":["foreign","unknown"],"timeout_seconds":600}`))
	if err != nil || len(data["errors"].([]map[string]any)) != 2 || len(data["pending"].([]string)) != 0 {
		t.Fatalf("all invalid IDs = %#v, %v", data, err)
	}
}

func TestAgentWaitAnyIncludesAllAlreadyFinishedAndDeduplicates(t *testing.T) {
	server, _ := fakeAgentWaitRuns(t,
		&store.Task{ID: "done", Type: "agent_run", Principal: "owner", Status: "done"},
		&store.Task{ID: "failed", Type: "agent_run", Principal: "owner", Status: "failed"},
		&store.Task{ID: "running", Type: "agent_run", Principal: "owner", Status: "running"},
	)
	data, err := server.toolAgentWaitAny(context.Background(), json.RawMessage(`{"run_ids":["done","running","failed","done","running"]}`))
	want := map[string]any{
		"finished": []map[string]any{{"run_id": "done", "status": "done"}, {"run_id": "failed", "status": "failed"}},
		"pending":  []string{"running"},
	}
	if err != nil || !reflect.DeepEqual(data, want) {
		t.Fatalf("already finished = %#v, %v; want %#v", data, err, want)
	}
}

func TestAgentWaitAnyGrantEnforcementAndAudit(t *testing.T) {
	server, _ := fakeAgentWaitRuns(t, &store.Task{ID: "done", Type: "agent_run", Principal: "owner", Status: "done"})
	server.Grants = GrantSet([]string{"agent.run"})
	arguments := json.RawMessage(`{"run_ids":["done"]}`)
	if _, err := server.AgentWaitAny(context.Background(), arguments); err == nil || !strings.Contains(err.Error(), `lacks grant "agent.read"`) {
		t.Fatalf("denied wait error = %v", err)
	}
	server.Grants = GrantSet([]string{"agent.read"})
	if data, err := server.AgentWaitAny(context.Background(), arguments); err != nil || len(data["finished"].([]map[string]any)) != 1 {
		t.Fatalf("allowed wait = %#v, %v", data, err)
	}
	audit, err := os.ReadFile(filepath.Join(server.Cfg.Dir, "mcp-audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(audit)), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit records = %s", audit)
	}
	for index, decision := range []string{"error", "allowed"} {
		var record map[string]any
		if err := json.Unmarshal([]byte(lines[index]), &record); err != nil {
			t.Fatal(err)
		}
		if record["tool"] != "agent_wait_any" || record["principal"] != "owner" || record["decision"] != decision || record["argument_sha256"] == nil {
			t.Fatalf("audit record = %#v", record)
		}
		if record["run_ids"] != nil || record["arguments"] != nil {
			t.Fatalf("audit leaked arguments: %#v", record)
		}
		if record["target"] != `{"run_ids":["done"],"count":1}` {
			t.Fatalf("audit target = %#v", record["target"])
		}
	}
}

func TestAgentWaitAnyCancellation(t *testing.T) {
	server, _ := fakeAgentWaitRuns(t, &store.Task{ID: "running", Type: "agent_run", Principal: "owner", Status: "running"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := server.toolAgentWaitAny(ctx, json.RawMessage(`{"run_ids":["running"],"timeout_seconds":600}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait error = %v", err)
	}
}

func TestAgentWaitAnyValidatesBounds(t *testing.T) {
	server := testWingServer(t, &Server{Version: "test"})
	tooMany, _ := json.Marshal(map[string]any{"run_ids": make([]string, 65)})
	for _, input := range []string{
		`{}`, `{"run_ids":null}`, `{"run_ids":[]}`, string(tooMany),
		`{"run_ids":["id"],"timeout_seconds":-1}`,
		`{"run_ids":["id"],"timeout_seconds":0.01}`,
		`{"run_ids":["id"],"timeout_seconds":600.1}`,
		`{"run_ids":[1]}`, `{"run_ids":["id"],"extra":true}`,
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := server.toolAgentWaitAny(context.Background(), json.RawMessage(input)); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
}

func TestAgentWaitAnyBatchesStatusReads(t *testing.T) {
	tasks := make([]*store.Task, 64)
	ids := make([]string, 64)
	for i := range tasks {
		ids[i] = fmt.Sprintf("run-%d", i)
		tasks[i] = &store.Task{ID: ids[i], Type: "agent_run", Principal: "owner", Status: "running"}
	}
	server, _ := fakeAgentWaitRuns(t, tasks...)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{}, 1)
	observed := &agentWaitStartedContext{Context: ctx, started: started}
	wire, _ := json.Marshal(map[string]any{"run_ids": ids})
	result := make(chan map[string]any, 1)
	errs := make(chan error, 1)
	go func() { data, err := server.toolAgentWaitAny(observed, wire); result <- data; errs <- err }()
	<-started
	activeAgentWaitAnyCalls.Lock()
	count := activeAgentWaitAnyCalls.counts[server.Cfg.DBPath()+"\x00"+server.clientPrincipal()]
	activeAgentWaitAnyCalls.Unlock()
	if count != 1 {
		t.Fatalf("64 IDs used %d bounded wait slots", count)
	}
	if _, err := server.Sessions.RunBackend.Stop(t.Context(), server.Cfg, eggclient.LocalSession{ID: "session-" + ids[63]}, ids[63]); err != nil {
		t.Fatal(err)
	}
	data := <-result
	if err := <-errs; err != nil || len(data["finished"].([]map[string]any)) != 1 || len(data["pending"].([]string)) != 63 {
		t.Fatalf("subscription completion: %v %v", data, err)
	}
}

type agentWaitStartedContext struct {
	context.Context
	started chan<- struct{}
	once    sync.Once
}

func (c *agentWaitStartedContext) Done() <-chan struct{} {
	c.once.Do(func() { c.started <- struct{}{} })
	return c.Context.Done()
}

func TestAgentWaitAnyLimitsConcurrentCallsPerPrincipal(t *testing.T) {
	server, _ := fakeAgentWaitRuns(t,
		&store.Task{ID: "running", Type: "agent_run", Principal: "owner", Status: "running"},
		&store.Task{ID: "done", Type: "agent_run", Principal: "owner", Status: "done"},
		&store.Task{ID: "other-done", Type: "agent_run", Principal: "other", Status: "done"},
	)
	for _, timeout := range []float64{600, 0.1} {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{}, 4)
		results := make(chan error, 4)
		remaining := 4
		t.Cleanup(func() {
			cancel()
			for ; remaining > 0; remaining-- {
				select {
				case <-results:
				}
			}
		})
		arguments, _ := json.Marshal(map[string]any{"run_ids": []string{"running"}, "timeout_seconds": timeout})
		for index := 0; index < 4; index++ {
			peer := testWingServer(t, &Server{Version: "test", Cfg: server.Cfg, Principal: "owner", Actor: strings.Repeat("a", index+1), Logs: io.Discard})
			peer.Sessions = server.Sessions
			waitCtx := &agentWaitStartedContext{Context: ctx, started: started}
			go func() {
				_, err := peer.toolAgentWaitAny(waitCtx, arguments)
				results <- err
			}()
		}
		for index := 0; index < 4; index++ {
			select {
			case <-started:
			case err := <-results:
				remaining--
				t.Fatalf("one of the first four waits was rejected: %v", err)
			}
		}
		if _, err := server.AgentWaitAny(context.Background(), json.RawMessage(`{"run_ids":["done"]}`)); err == nil || !strings.Contains(err.Error(), "too many concurrent waits") {
			t.Fatalf("fifth wait error = %v", err)
		}
		other := testWingServer(t, &Server{Version: "test", Cfg: server.Cfg, Principal: "other", Logs: io.Discard})
		other.Sessions = server.Sessions
		if data, err := other.AgentWaitAny(context.Background(), json.RawMessage(`{"run_ids":["other-done"]}`)); err != nil || len(data["finished"].([]map[string]any)) != 1 {
			t.Fatalf("other principal wait = %#v, %v", data, err)
		}
		if timeout == 600 {
			cancel()
		}
		for ; remaining > 0; remaining-- {
			select {
			case err := <-results:
				if timeout == 600 && !errors.Is(err, context.Canceled) || timeout == 0.1 && err != nil {
					t.Errorf("wait error = %v", err)
				}
			}
		}
		cancel()
	}
	if _, err := server.AgentWaitAny(context.Background(), json.RawMessage(`{"run_ids":["done"]}`)); err != nil {
		t.Fatalf("wait slot was not released: %v", err)
	}
}

func TestAgentWaitAnyPreservesDefaultOwnershipAndOrphanCleanup(t *testing.T) {

	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, task := range []*store.Task{{ID: "orphan", Type: "agent_run", Status: "running", RunnerPID: 1 << 30}, {ID: "live", Type: "agent_run", Principal: "default", Status: "running", RunnerPID: os.Getpid()}, {ID: "foreign", Type: "agent_run", Principal: "other", Status: "running"}} {
		if err := db.CreateTask(task); err != nil {
			t.Fatal(err)
		}
	}
	f := newRunWingFixture(t, &Server{Version: "test", Cfg: cfg, Logs: io.Discard})
	data, err := f.server.toolAgentWaitAny(t.Context(), json.RawMessage(`{"run_ids":["orphan","live","foreign"]}`))
	if err != nil || len(data["finished"].([]map[string]any)) != 2 || len(data["errors"].([]map[string]any)) != 1 {
		t.Fatalf("legacy history: %v %v", data, err)
	}
	for _, id := range []string{"orphan", "live"} {
		task, err := db.GetTask(id)
		if err != nil || task.Status != "failed" || task.Error == nil || *task.Error != "Wingthing legacy run ended: unknown_outcome." {
			t.Fatalf("legacy must not use PID liveness: %+v %v", task, err)
		}
	}

}
