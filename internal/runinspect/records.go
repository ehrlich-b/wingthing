package runinspect

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

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
	SpecHash  *string `json:"spec_hash"`
	Agent     string  `json:"agent"`
	CWD       string  `json:"cwd"`
	ID        string  `json:"run_id"`
	SessionID string  `json:"session_id"`
	ParentID  string  `json:"parent_id"`
	Phase     string  `json:"phase"`
	Cancelled bool    `json:"cancelled"`
	Launch    struct {
		Authority struct{ Principal, UserID string }
	} `json:"launch"`
	Result resultRecord `json:"result"`
}

type wingRow struct {
	revisionKnown                bool
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
	checkpointKnown                              bool
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

type legacyRow struct {
	id, parent, principal, status string
	output                        *string
	source                        Ref
}

type records struct {
	legacy        []legacyRow
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
	file, err := s.root.Open(ref.Path)
	if err != nil {
		r.issue("wing_database_unreadable", ref)
		return
	}
	defer file.Close()
	// Retain the rooted descriptor so path replacement cannot redirect SQLite
	// to another state directory after manifest verification.
	uri := (&url.URL{Scheme: "file", Path: filepath.Join("/dev/fd", fmt.Sprint(file.Fd()))}).String() + "?mode=ro&immutable=1&_pragma=query_only(1)&_pragma=temp_store(2)"
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
	columns := func(table string) map[string]bool {
		out := map[string]bool{}
		rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return out
		}
		defer rows.Close()
		for rows.Next() {
			var index, notnull, primary int
			var name, typ string
			var defaultValue any
			if rows.Scan(&index, &name, &typ, &notnull, &defaultValue, &primary) == nil {
				out[name] = true
			}
		}
		return out
	}
	projection := func(table string, required []string, optional []struct{ name, fallback string }) (string, map[string]bool) {
		cols := columns(table)
		selected := append([]string{}, required...)
		for _, field := range optional {
			if cols[field.name] {
				selected = append(selected, field.name)
			} else {
				selected = append(selected, field.fallback)
				r.issue("older_schema_field_unavailable", Ref{Path: "wt.db", Record: table, Field: field.name})
			}
		}
		return "SELECT " + strings.Join(selected, ",") + " FROM " + table, cols
	}
	wingQuery, wingColumns := projection("agent_runs", []string{"id", "session_id", "principal", "spec_hash", "record"}, []struct{ name, fallback string }{{"revision", "0"}})
	read("agent_runs", wingQuery+" ORDER BY id", func(rows *sql.Rows) error {
		var row wingRow
		if err := rows.Scan(&row.id, &row.session, &row.principal, &row.spec, &row.raw, &row.revision); err != nil {
			return err
		}
		row.source = Ref{Path: "wt.db", Record: "agent_runs:" + row.id}
		row.revisionKnown = wingColumns["revision"]
		row.valid = decodeRecord([]byte(row.raw), &row.record) == nil && row.record.ID != "" && row.record.SessionID != ""
		if !row.valid {
			r.issue("partial_run_record", row.source, row.id)
		}
		r.wings = append(r.wings, row)
		return nil
	})
	conversationQuery, conversationColumns := projection("conversations", []string{"id", "owner_id", "session_id"}, []struct{ name, fallback string }{{"root_id", "''"}, {"parent_id", "''"}, {"checkpoint", "''"}, {"revision", "NULL"}, {"delivered_cursor", "NULL"}})
	read("conversations", conversationQuery+" ORDER BY id", func(rows *sql.Rows) error {
		var c conversationRow
		if err := rows.Scan(&c.id, &c.owner, &c.session, &c.root, &c.parent, &c.checkpoint, &c.revision, &c.delivered); err != nil {
			return err
		}
		c.source = Ref{Path: "wt.db", Record: "conversations:" + c.id}
		c.checkpointKnown = conversationColumns["checkpoint"]
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
		e.source = Ref{Path: "wt.db", Record: "conversation_events:" + strconv.FormatInt(e.sequence, 10), Field: "sequence"}
		r.events = append(r.events, e)
		return nil
	})
	// Historical agent_run tasks can be ancestors without an agent_runs row.
	if cols := columns("tasks"); cols["type"] && cols["id"] {
		legacyQuery, _ := projection("tasks", []string{"id", "status"}, []struct{ name, fallback string }{{"parent_id", "''"}, {"principal", "''"}, {"output", "NULL"}})
		read("tasks", legacyQuery+" WHERE type='agent_run' ORDER BY id", func(rows *sql.Rows) error {
			var task legacyRow
			var parent sql.NullString
			if err := rows.Scan(&task.id, &task.status, &parent, &task.principal, &task.output); err != nil {
				return err
			}
			task.parent = parent.String
			task.source = Ref{Path: "wt.db", Record: "tasks:" + task.id}
			r.legacy = append(r.legacy, task)
			return nil
		})
	}
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
