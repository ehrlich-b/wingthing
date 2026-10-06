package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func fakeAgentWaitRuns(t *testing.T, tasks ...*store.Task) (*Server, *store.Store) {
	t.Helper()
	cfg := &config.Config{Dir: t.TempDir()}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeForTest(t, "fake run store", db) })
	for _, task := range tasks {
		if err := db.CreateTask(task); err != nil {
			t.Fatal(err)
		}
	}
	return &Server{Cfg: cfg, Principal: "owner", Logs: io.Discard}, db
}

func TestAgentWaitAnyReturnsFirstOfThreeFakeRuns(t *testing.T) {
	server, db := fakeAgentWaitRuns(t,
		&store.Task{ID: "first", Type: "agent_run", Principal: "owner", Status: "running"},
		&store.Task{ID: "second", Type: "agent_run", Principal: "owner", Status: "running"},
		&store.Task{ID: "third", Type: "agent_run", Principal: "owner", Status: "running"},
	)
	pending := []string{"first", "second", "third"}
	for index, id := range []string{"third", "first", "second"} {
		arguments, err := json.Marshal(map[string]any{"run_ids": pending, "timeout_seconds": 2})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		type response struct {
			data map[string]any
			err  error
		}
		waited := make(chan response, 1)
		go func() {
			data, err := server.toolAgentWaitAny(ctx, arguments)
			waited <- response{data, err}
		}()
		select {
		case result := <-waited:
			t.Fatalf("wait returned before any run finished: %#v, %v", result.data, result.err)
		case <-time.After(50 * time.Millisecond):
		}
		status := "done"
		if index == 1 {
			status = "failed"
		}
		if err := db.UpdateTaskStatus(id, status); err != nil {
			t.Fatal(err)
		}
		wantPending := []string{}
		for _, candidate := range pending {
			if candidate != id {
				wantPending = append(wantPending, candidate)
			}
		}
		select {
		case result := <-waited:
			want := map[string]any{"finished": []map[string]any{{"run_id": id, "status": status}}, "pending": wantPending}
			if result.err != nil || !reflect.DeepEqual(result.data, want) {
				t.Fatalf("completion %d = %#v, %v; want %#v", index, result.data, result.err, want)
			}
		case <-time.After(time.Second):
			t.Fatal("wait stayed blocked after one run finished")
		}
		cancel()
		pending = wantPending
	}
}

func TestAgentWaitAnyTimeout(t *testing.T) {
	server, _ := fakeAgentWaitRuns(t,
		&store.Task{ID: "running", Type: "agent_run", Principal: "owner", Status: "running"},
		&store.Task{ID: "foreign", Type: "agent_run", Principal: "other", Status: "done"},
	)
	started := time.Now()
	data, err := server.toolAgentWaitAny(context.Background(), json.RawMessage(`{"run_ids":["running"],"timeout_seconds":0.1}`))
	want := map[string]any{"finished": []map[string]any{}, "pending": []string{"running"}}
	if err != nil || !reflect.DeepEqual(data, want) {
		t.Fatalf("timeout = %#v, %v; want %#v", data, err, want)
	}
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond || elapsed > time.Second {
		t.Fatalf("timeout elapsed = %v", elapsed)
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
	data, err = server.toolAgentWaitAny(context.Background(), json.RawMessage(`{"run_ids":["foreign","unknown"],"timeout_seconds":3600}`))
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
	}
}

func TestAgentWaitAnyCancellation(t *testing.T) {
	server, _ := fakeAgentWaitRuns(t, &store.Task{ID: "running", Type: "agent_run", Principal: "owner", Status: "running"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := server.toolAgentWaitAny(ctx, json.RawMessage(`{"run_ids":["running"],"timeout_seconds":3600}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait error = %v", err)
	}
}

func TestAgentWaitAnyValidatesBounds(t *testing.T) {
	server := &Server{}
	tooMany, _ := json.Marshal(map[string]any{"run_ids": make([]string, 65)})
	for _, input := range []string{
		`{}`, `{"run_ids":null}`, `{"run_ids":[]}`, string(tooMany),
		`{"run_ids":["id"],"timeout_seconds":-1}`,
		`{"run_ids":["id"],"timeout_seconds":0.01}`,
		`{"run_ids":["id"],"timeout_seconds":3601}`,
		`{"run_ids":[1]}`, `{"run_ids":["id"],"extra":true}`,
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := server.toolAgentWaitAny(context.Background(), json.RawMessage(input)); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
}
