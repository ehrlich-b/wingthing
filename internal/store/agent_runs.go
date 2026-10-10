package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// AgentRun is the durable wing composition record. Record includes immutable
// launch provenance and the complete semantic outcome; tasks is its historical
// projection for existing conversation and CLI readers.
type AgentRun struct {
	ID, SessionID, Principal, RequestKey, SpecHash string
	Record                                         json.RawMessage
	Revision                                       int64
}

// AdmitAgentRun reserves both identifiers and the optional retry key in the
// same transaction as the legacy task projection, before any egg can launch.
func (s *Store) AdmitAgentRun(run *AgentRun, task *Task) (*AgentRun, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	// Acquire the SQLite writer reservation before reading a retry key. Two
	// admissions must not both read absent and then upgrade read transactions.
	if _, err = tx.Exec("UPDATE agent_runs SET revision=revision WHERE 0"); err != nil {
		return nil, false, err
	}
	if run.RequestKey != "" {
		existing, err := scanAgentRun(tx.QueryRow("SELECT id, session_id, principal, COALESCE(request_key,''), spec_hash, record, revision FROM agent_runs WHERE principal=? AND request_key=?", run.Principal, run.RequestKey))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
		if existing != nil {
			if existing.SpecHash != run.SpecHash {
				return nil, false, errors.New("idempotency_key already has a different run request")
			}
			return existing, false, nil
		}
	}
	if task.Status == "" {
		task.Status = "pending"
	}
	if task.Isolation == "" {
		task.Isolation = "none"
	}
	_, err = tx.Exec(`INSERT INTO tasks (id,type,what,run_at,agent,model,timeout_seconds,isolation,parent_id,status,cwd,principal,egg_config,created_at)
 VALUES (?,'agent_run',?,?,?,?,?,?,?,?,?,?,?,?)`, task.ID, task.What, task.RunAt.UTC().Format(timeFmt), task.Agent, task.Model, task.TimeoutSeconds, task.Isolation, task.ParentID, task.Status, task.CWD, task.Principal, task.EggConfigYAML, task.CreatedAt.UTC().Format(timeFmt))
	if err != nil {
		return nil, false, err
	}
	var key any
	if run.RequestKey != "" {
		key = run.RequestKey
	}
	_, err = tx.Exec("INSERT INTO agent_runs (id,session_id,principal,request_key,spec_hash,record) VALUES (?,?,?,?,?,?)", run.ID, run.SessionID, run.Principal, key, run.SpecHash, string(run.Record))
	if err != nil {
		return nil, false, err
	}
	_, err = tx.Exec("INSERT INTO task_log (task_id,event) VALUES (?,'admitted')", run.ID)
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	run.Revision = 1
	return run, true, nil
}

func scanAgentRun(row interface{ Scan(...any) error }) (*AgentRun, error) {
	r := new(AgentRun)
	var record string
	err := row.Scan(&r.ID, &r.SessionID, &r.Principal, &r.RequestKey, &r.SpecHash, &record, &r.Revision)
	if err != nil {
		return nil, err
	}
	r.Record = json.RawMessage(record)
	return r, nil
}

func (s *Store) GetAgentRun(id string) (*AgentRun, error) {
	r, err := scanAgentRun(s.db.QueryRow("SELECT id,session_id,principal,COALESCE(request_key,''),spec_hash,record,revision FROM agent_runs WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (s *Store) ListAgentRuns() ([]*AgentRun, error) {
	rows, err := s.db.Query("SELECT id,session_id,principal,COALESCE(request_key,''),spec_hash,record,revision FROM agent_runs ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AgentRun{}
	for rows.Next() {
		r, err := scanAgentRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveAgentRun atomically publishes full output, terminal metadata and a
// content-free event. Revision comparison prevents a stale observer replacing
// a stop intent or a terminal outcome after reconnect.
func (s *Store) SaveAgentRun(r *AgentRun, task *Task, event, detail string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE agent_runs SET record=?,revision=revision+1 WHERE id=? AND revision=?", string(r.Record), r.ID, r.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("agent run revision changed: %s", r.ID)
	}
	var start, finish any
	if task.StartedAt != nil {
		start = task.StartedAt.UTC().Format(timeFmt)
	}
	if task.FinishedAt != nil {
		finish = task.FinishedAt.UTC().Format(timeFmt)
	}
	_, err = tx.Exec("UPDATE tasks SET status=?,what=?,started_at=?,finished_at=?,output=?,error=? WHERE id=?", task.Status, task.What, start, finish, task.Output, task.Error, r.ID)
	if err != nil {
		return err
	}
	if event != "" {
		_, err = tx.Exec("INSERT INTO task_log (task_id,event,detail) VALUES (?,?,NULLIF(?,''))", r.ID, event, detail)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.Revision++
	return nil
}
