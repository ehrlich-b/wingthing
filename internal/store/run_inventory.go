package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// RunInventoryScope is supplied by the authenticated wing service, never tool
// arguments. Legacy tasks have no user identity, matching the known-ID path.
type RunInventoryScope struct {
	Principal, UserID                string
	LegacyLocalDefault, EnforcePaths bool
	AllowedPaths                     []string
}

type RunInventoryItem struct {
	ID                 string `json:"run_id"`
	SessionID          string `json:"session_id"`
	Agent              string `json:"agent"`
	Model              string `json:"model"`
	Label              string `json:"label,omitempty"`
	CWD                string `json:"cwd"`
	Isolation          string `json:"isolation"`
	Status             string `json:"status"`
	Phase              string `json:"phase"`
	ParentID           string `json:"parent_id,omitempty"`
	RequestKey         string `json:"idempotency_key,omitempty"`
	CreatedAt          string `json:"created_at"`
	StartedAt          string `json:"started_at,omitempty"`
	FinishedAt         string `json:"finished_at,omitempty"`
	TimeoutSeconds     int    `json:"timeout_seconds"`
	FailureKind        string `json:"failure_kind,omitempty"`
	Terminal           bool   `json:"terminal"`
	Legacy             bool   `json:"legacy"`
	Cleanup            string `json:"cleanup"`
	OutputAvailability string `json:"output_availability"`
}

type RunInventoryPosition struct {
	Watermark    int64  `json:"watermark"`
	AnchorID     string `json:"anchor_id"`
	AfterCreated string `json:"after_created"`
	AfterID      string `json:"after_id"`
}

type RunInventoryQuery struct {
	Scope         RunInventoryScope
	Limit         int
	RequestKey    string
	TerminalSince time.Time
	Position      RunInventoryPosition
	Continuing    bool
}

type RunInventoryPage struct {
	Runs     []RunInventoryItem
	Position RunInventoryPosition
	More     bool
}

var ErrRunInventoryCursor = fmt.Errorf("run inventory cursor is invalid or expired; restart discovery: %w", fs.ErrInvalid)

// Keep path semantics equal to Runs.owned, including clean/relative path rules.
// This pure SQL predicate runs before LIMIT; denied rows cannot occupy a page.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("wt_inventory_path_allowed", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		cwd, _ := args[0].(string)
		encoded, _ := args[1].(string)
		var paths []string
		if err := json.Unmarshal([]byte(encoded), &paths); err != nil {
			return nil, err
		}
		for _, p := range paths {
			rel, err := filepath.Rel(p, cwd)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return int64(1), nil
			}
		}
		return int64(0), nil
	})
}

func inventoryScopeSQL(s RunInventoryScope) (string, []any) {
	owner := "(t.principal=?)"
	args := []any{s.Principal}
	if s.LegacyLocalDefault {
		owner = "(t.principal=? OR t.principal IN ('','default'))"
	}
	// The immutable durable authority is checked as well as its task projection.
	owner += " AND (a.id IS NULL OR ((json_extract(a.record,'$.launch.Authority.Principal')=?"
	args = append(args, s.Principal)
	if s.LegacyLocalDefault {
		owner += " OR json_extract(a.record,'$.launch.Authority.Principal') IN ('','default')"
	}
	owner += ")"
	if s.UserID != "" {
		owner += " AND json_extract(a.record,'$.launch.Authority.UserID')=?"
		args = append(args, s.UserID)
	}
	owner += "))"
	if s.EnforcePaths {
		paths, _ := json.Marshal(s.AllowedPaths)
		if s.AllowedPaths == nil {
			paths = []byte("[]")
		}
		owner += " AND wt_inventory_path_allowed(COALESCE(json_extract(a.record,'$.cwd'),t.cwd),?)=1"
		args = append(args, string(paths))
	}
	return owner, args
}

const inventoryFrom = " FROM tasks t LEFT JOIN agent_runs a ON a.id=t.id WHERE t.type='agent_run' AND "

// ListRunInventory enumerates committed metadata in a read transaction. It
// does not load run output, touch eggs, reconcile state, or instantiate workers.
func (s *Store) ListRunInventory(ctx context.Context, q RunInventoryQuery) (RunInventoryPage, error) {
	page := RunInventoryPage{Runs: []RunInventoryItem{}, Position: q.Position}
	if q.Limit < 1 || q.Limit > 200 || len(q.RequestKey) > 200 || q.TerminalSince.IsZero() {
		return page, errors.New("invalid run inventory bounds")
	}
	scope, scopeArgs := inventoryScopeSQL(q.Scope)
	prefix, from, candidateArgs := inventorySelection(q)
	args := append(candidateArgs, scopeArgs...)
	eligible := scope

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	// Fail closed on corrupt candidate records rather than silently omitting an
	// admitted ID. The task projection's principal is the prefilter authority.
	var corrupt int
	integrity := `SELECT count(*)` + from + `a.id IS NOT NULL AND CASE WHEN json_valid(a.record)=0 THEN 1 ELSE
 json_extract(a.record,'$.run_id') IS NULL OR json_extract(a.record,'$.run_id')!=t.id
 OR json_extract(a.record,'$.session_id') IS NULL OR json_extract(a.record,'$.session_id')!=a.session_id
 OR json_extract(a.record,'$.launch.Authority.Principal') IS NULL
 OR json_extract(a.record,'$.launch.Authority.Principal')!=a.principal
 OR json_extract(a.record,'$.launch.Authority.UserID') IS NULL END`
	if err = tx.QueryRowContext(ctx, prefix+integrity, candidateArgs...).Scan(&corrupt); err != nil {
		return page, err
	}
	if corrupt != 0 {
		return page, errors.New("run inventory contains unavailable durable records; discovery is incomplete")
	}
	if !q.Continuing {
		var id sql.NullString
		var watermark sql.NullInt64
		err = tx.QueryRowContext(ctx, prefix+"SELECT t.id,t.rowid"+from+eligible+" ORDER BY t.rowid DESC LIMIT 1", args...).Scan(&id, &watermark)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return page, err
		}
		page.Position.Watermark, page.Position.AnchorID = watermark.Int64, id.String
	} else {
		// A removed or no-longer-authorized anchor must not become an empty success.
		var count int
		// Scope arguments, rather than eligibility, validate anchors across transitions.
		_, scopeArgs := inventoryScopeSQL(q.Scope)
		anchorArgs := append(append([]any{}, scopeArgs...), q.Position.AnchorID, q.Position.Watermark)
		if q.Position.Watermark <= 0 || q.Position.AfterID == "" {
			return page, ErrRunInventoryCursor
		}
		if err = tx.QueryRowContext(ctx, "SELECT count(*)"+inventoryFrom+scope+" AND t.id=? AND t.rowid=?", anchorArgs...).Scan(&count); err != nil {
			return page, err
		}
		if count != 1 {
			return page, ErrRunInventoryCursor
		}
		afterArgs := append(append([]any{}, scopeArgs...), q.Position.AfterID, q.Position.AfterCreated)
		if err = tx.QueryRowContext(ctx, "SELECT count(*)"+inventoryFrom+scope+" AND t.id=? AND strftime('%Y-%m-%dT%H:%M:%SZ',t.created_at)=?", afterArgs...).Scan(&count); err != nil {
			return page, err
		}
		if count != 1 {
			return page, ErrRunInventoryCursor
		}
	}
	query := `SELECT t.id, COALESCE(a.session_id,''),
 COALESCE(json_extract(a.record,'$.agent'),t.agent), COALESCE(json_extract(a.record,'$.model'),t.model),
 COALESCE(json_extract(a.record,'$.label'),''), COALESCE(json_extract(a.record,'$.cwd'),t.cwd),
 COALESCE(json_extract(a.record,'$.isolation'),t.isolation),t.status,
 COALESCE(json_extract(a.record,'$.phase'),'terminal'), COALESCE(t.parent_id,''),COALESCE(a.request_key,''),
 strftime('%Y-%m-%dT%H:%M:%SZ',t.created_at),COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ',t.started_at),''),COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ',t.finished_at),''),t.timeout_seconds,
 COALESCE(json_extract(a.record,'$.result.failure_kind'),CASE WHEN t.status!='done' THEN 'unknown_outcome' ELSE '' END),
 t.status IN ('done','failed','timeout','stopped'),a.id IS NULL,
 CASE WHEN COALESCE(json_array_length(json_extract(a.record,'$.result.surviving_descendants')),0)>0
 OR COALESCE(json_extract(a.record,'$.result.containment_error'),'')!='' THEN 'attention' ELSE 'unknown' END,
 CASE WHEN t.status NOT IN ('done','failed','timeout','stopped') THEN 'pending'
 WHEN t.status='done' AND (a.id IS NOT NULL AND json_type(a.record,'$.result.text')='text' OR a.id IS NULL AND t.output IS NOT NULL) THEN 'available'
 WHEN t.status='done' THEN 'unavailable' ELSE 'unknown' END`
	query = prefix + query
	query += from + eligible + " AND t.rowid<=?"
	queryArgs := append(append([]any{}, args...), page.Position.Watermark)
	if q.Continuing {
		query += " AND (strftime('%Y-%m-%dT%H:%M:%SZ',t.created_at)>? OR (strftime('%Y-%m-%dT%H:%M:%SZ',t.created_at)=? AND t.id>?))"
		queryArgs = append(queryArgs, q.Position.AfterCreated, q.Position.AfterCreated, q.Position.AfterID)
	}
	query += " ORDER BY strftime('%Y-%m-%dT%H:%M:%SZ',t.created_at),t.id LIMIT ?"
	queryArgs = append(queryArgs, q.Limit+1)
	rows, err := tx.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var r RunInventoryItem
		if err = rows.Scan(&r.ID, &r.SessionID, &r.Agent, &r.Model, &r.Label, &r.CWD, &r.Isolation, &r.Status, &r.Phase, &r.ParentID, &r.RequestKey, &r.CreatedAt, &r.StartedAt, &r.FinishedAt, &r.TimeoutSeconds, &r.FailureKind, &r.Terminal, &r.Legacy, &r.Cleanup, &r.OutputAvailability); err != nil {
			rows.Close()
			return page, err
		}
		page.Runs = append(page.Runs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Runs) > q.Limit {
		page.More = true
		page.Runs = page.Runs[:q.Limit]
	}
	if len(page.Runs) > 0 {
		last := page.Runs[len(page.Runs)-1]
		page.Position.AfterCreated, page.Position.AfterID = last.CreatedAt, last.ID
	}
	if err = tx.Commit(); err != nil {
		return page, err
	}
	return page, nil
}

// Candidates contain IDs only. The partial indexes constrain work to active
// rows and the fixed recent-terminal window, before any durable JSON is read.
func inventorySelection(q RunInventoryQuery) (string, string, []any) {
	owner := "principal=?"
	if q.Scope.LegacyLocalDefault {
		owner = "(principal=? OR principal IN ('','default'))"
	}
	args := []any{q.Scope.Principal}
	var candidates string
	if q.RequestKey != "" {
		candidates = "SELECT id FROM agent_runs WHERE " + owner + " AND request_key=?"
		args = append(args, q.RequestKey)
	} else {
		candidates = "SELECT id FROM tasks INDEXED BY idx_tasks_run_inventory_active WHERE type='agent_run' AND status NOT IN ('done','failed','timeout','stopped') AND " + owner
		candidates += " UNION ALL SELECT id FROM tasks INDEXED BY idx_tasks_run_inventory_recent WHERE type='agent_run' AND status IN ('done','failed','timeout','stopped') AND " + owner + " AND julianday(COALESCE(finished_at,created_at))>=julianday(?)"
		args = append(args, q.Scope.Principal, q.TerminalSince.UTC().Format(timeFmt))
	}
	return "WITH inventory_candidates AS (" + candidates + ") ", " FROM inventory_candidates c JOIN tasks t ON t.id=c.id LEFT JOIN agent_runs a ON a.id=t.id WHERE t.type='agent_run' AND ", args
}
