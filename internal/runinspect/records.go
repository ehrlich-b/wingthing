package runinspect

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// These projections deliberately omit prompts, arbitrary errors, launch config,
// provider homes and titles. Fields are the pinned serializers, not a new
// generation/owner-epoch interpretation invented by the inspector.
type resultRecord struct {
	RunID             string  `json:"run_id"`
	SessionID         string  `json:"session_id"`
	Status            string  `json:"status"`
	Text              *string `json:"text"`
	ProviderSessionID string  `json:"provider_session_id"`
	TurnID            string  `json:"turn_id"`
	StartedAt         string  `json:"started_at"`
	EndedAt           string  `json:"ended_at"`
	Deadline          string  `json:"deadline"`
	FailureKind       string  `json:"failure_kind"`
}

type wingRecord struct {
	Agent     string `json:"agent"`
	CWD       string `json:"cwd"`
	ID        string `json:"run_id"`
	SessionID string `json:"session_id"`
	ParentID  string `json:"parent_id"`
	Phase     string `json:"phase"`
	Cancelled bool   `json:"cancelled"`
	Launch    struct {
		Authority struct{ Principal, UserID string }
	} `json:"launch"`
	Result resultRecord `json:"result"`
}

type wingRow struct {
	id, session, principal, spec string
	revision                     int64
	raw                          string
	record                       wingRecord
	valid                        bool
	source                       Ref
}

type eggRecord struct {
	Version  int          `json:"version"`
	SpecHash string       `json:"spec_hash"`
	Result   resultRecord `json:"result"`
	source   Ref
	valid    bool
}

type conversationRow struct {
	id, owner, root, parent, session, checkpoint string
	revision, delivered                          *int64
	source                                       Ref
}

type executionRow struct {
	session, conversation string
	source                Ref
}
type eventRow struct {
	root, conversation, session string
	sequence                    int64
	source                      Ref
}
type wakeRow struct {
	root     string
	delivery int64
	source   Ref
}

type records struct {
	wings         []wingRow
	eggs          []eggRecord
	conversations []conversationRow
	executions    []executionRow
	events        []eventRow
	wakes         []wakeRow
	issues        []Issue
	tables        map[string]bool
}

func (r *records) issue(code string, ref Ref, ids ...string) {
	r.issues = append(r.issues, Issue{Code: code, IDs: ids, Sources: []Ref{ref}})
}

// No store.Open: that path migrates and changes journal mode. immutable=1 is
// appropriate only because the completed-copy manifest refuses WAL sidecars.
func (s *snapshot) database(ctx context.Context, r *records) {
	ref := Ref{Path: "wt.db"}
	if _, exists := s.files[ref.Path]; !exists {
		r.issue("wing_database_absent", ref)
		return
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.Join(s.path, ref.Path)}).String() + "?mode=ro&immutable=1&_pragma=query_only(1)&_pragma=temp_store(2)"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		r.issue("wing_database_unreadable", ref)
		return
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	read := func(table, query string, scan func(*sql.Rows) error) {
		ref := Ref{Path: "wt.db", Record: table}
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			r.issue("table_or_columns_unavailable", ref)
			return
		}
		defer rows.Close()
		r.tables[table] = true
		count := 0
		for rows.Next() {
			count++
			if count > 10000 {
				r.issue("table_row_limit", ref)
				r.tables[table] = false
				return
			}
			if scan(rows) != nil {
				r.issue("partial_database_record", ref)
				r.tables[table] = false
			}
		}
		if rows.Err() != nil {
			r.issue("table_read_incomplete", ref)
			r.tables[table] = false
		}
	}
	read("agent_runs", `SELECT id,session_id,principal,spec_hash,revision,record FROM agent_runs ORDER BY id`, func(rows *sql.Rows) error {
		var row wingRow
		if err := rows.Scan(&row.id, &row.session, &row.principal, &row.spec, &row.revision, &row.raw); err != nil {
			return err
		}
		row.source = Ref{Path: "wt.db", Record: "agent_runs:" + row.id}
		row.valid = json.Unmarshal([]byte(row.raw), &row.record) == nil && row.record.ID != "" && row.record.SessionID != ""
		if !row.valid {
			r.issue("partial_run_record", row.source, row.id)
		}
		r.wings = append(r.wings, row)
		return nil
	})
	read("conversations", `SELECT id,owner_id,root_id,parent_id,session_id,checkpoint,revision,delivered_cursor FROM conversations ORDER BY id`, func(rows *sql.Rows) error {
		var c conversationRow
		if err := rows.Scan(&c.id, &c.owner, &c.root, &c.parent, &c.session, &c.checkpoint, &c.revision, &c.delivered); err != nil {
			return err
		}
		c.source = Ref{Path: "wt.db", Record: "conversations:" + c.id}
		r.conversations = append(r.conversations, c)
		return nil
	})
	read("conversation_executions", `SELECT session_id,conversation_id FROM conversation_executions ORDER BY session_id`, func(rows *sql.Rows) error {
		var e executionRow
		if err := rows.Scan(&e.session, &e.conversation); err != nil {
			return err
		}
		e.source = Ref{Path: "wt.db", Record: "conversation_executions:" + e.session}
		r.executions = append(r.executions, e)
		return nil
	})
	read("conversation_events", `SELECT sequence,root_id,conversation_id,session_id FROM conversation_events ORDER BY sequence`, func(rows *sql.Rows) error {
		var e eventRow
		if err := rows.Scan(&e.sequence, &e.root, &e.conversation, &e.session); err != nil {
			return err
		}
		e.source = Ref{Path: "wt.db", Record: "conversation_events", Field: "sequence"}
		r.events = append(r.events, e)
		return nil
	})
	read("conversation_wake_policy", `SELECT root_id,delivery_cursor FROM conversation_wake_policy ORDER BY root_id`, func(rows *sql.Rows) error {
		var w wakeRow
		if err := rows.Scan(&w.root, &w.delivery); err != nil {
			return err
		}
		w.source = Ref{Path: "wt.db", Record: "conversation_wake_policy:" + w.root, Field: "delivery_cursor"}
		r.wakes = append(r.wakes, w)
		return nil
	})
}
