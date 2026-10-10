package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAgentRunAdmissionSurvivesReopenAndLostAcknowledgement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := &Task{ID: "reserved-run", What: "prompt", Agent: "codex", Principal: "owner", RunAt: now, CreatedAt: now}
	run := &AgentRun{ID: task.ID, SessionID: "reserved-session", Principal: "owner", RequestKey: "retry", SpecHash: "exact", Record: []byte(`{"phase":"admitted"}`)}
	if _, fresh, err := db.AdmitAgentRun(run, task); err != nil || !fresh {
		t.Fatalf("admit: %v %v", fresh, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	retry := *run
	retry.ID = "other-run"
	retry.SessionID = "other-session"
	got, fresh, err := db.AdmitAgentRun(&retry, task)
	if err != nil || fresh || got.ID != run.ID || got.SessionID != run.SessionID {
		t.Fatalf("lost ack: %+v %v %v", got, fresh, err)
	}
	retry.SpecHash = "changed"
	if _, _, err := db.AdmitAgentRun(&retry, task); err == nil {
		t.Fatal("changed retry admitted")
	}
	task.Status = "done"
	output := "full Ω result"
	task.Output = &output
	task.FinishedAt = &now
	run.Record = []byte(`{"phase":"terminal","output":"full Ω result"}`)
	stale := *run
	if err := db.SaveAgentRun(run, task, "done", ""); err != nil {
		t.Fatal(err)
	}
	stale.Record = []byte(`{"phase":"running"}`)
	if err := db.SaveAgentRun(&stale, task, "running", ""); err == nil {
		t.Fatal("stale observer replaced terminal result")
	}
	stored, err := db.GetTask(task.ID)
	if err != nil || stored.Status != "done" || stored.Output == nil || *stored.Output != output || stored.FinishedAt == nil {
		t.Fatalf("projection: %+v %v", stored, err)
	}
	logs, err := db.ListLogByTask(task.ID)
	if err != nil || len(logs) != 2 {
		t.Fatalf("events: %v %v", logs, err)
	}
}

func TestConcurrentAgentRunAdmissionReservesOneRetryKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	var wg sync.WaitGroup
	results := make(chan *AgentRun, 2)
	failures := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			db, err := Open(path)
			if err != nil {
				failures <- err
				return
			}
			defer db.Close()
			task := &Task{ID: id, Principal: "owner", CreatedAt: time.Now(), RunAt: time.Now()}
			run, _, err := db.AdmitAgentRun(&AgentRun{ID: id, SessionID: id, Principal: "owner", RequestKey: "same", SpecHash: "same", Record: []byte(`{}`)}, task)
			if err != nil {
				failures <- err
				return
			}
			results <- run
		}(id)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	a, b := <-results, <-results
	if a.ID != b.ID {
		t.Fatalf("duplicate admission: %s %s", a.ID, b.ID)
	}
}
