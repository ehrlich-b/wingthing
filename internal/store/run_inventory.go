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
	// SQLite accepts relative dates such as 'now' and numeric Julian days.
	// Persisted inventory evidence must use a supported, fixed store timestamp.
	sqlite.MustRegisterDeterministicScalarFunction("wt_inventory_time_valid", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		value, ok := args[0].(string)
		if !ok || parseTime(value).IsZero() {
			return int64(0), nil
		}
		return int64(1), nil
	})
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
	// Invalid JSON can belong to a denied path. Authorization must remain safe
	// even when the record was excluded from the integrity check.
	record := "CASE WHEN json_valid(a.record) THEN a.record ELSE '{}' END"
	owner += " AND (a.rowid IS NULL OR ((json_extract(" + record + ",'$.launch.Authority.Principal')=?"
	args = append(args, s.Principal)
	if s.LegacyLocalDefault {
		owner += " OR json_extract(" + record + ",'$.launch.Authority.Principal') IN ('','default')"
	}
	owner += ")"
	if s.UserID != "" {
		owner += " AND json_extract(" + record + ",'$.launch.Authority.UserID')=?"
		args = append(args, s.UserID)
	}
	owner += "))"
	if s.EnforcePaths {
		paths, _ := json.Marshal(s.AllowedPaths)
		if s.AllowedPaths == nil {
			paths = []byte("[]")
		}
		owner += " AND wt_inventory_path_allowed(COALESCE(json_extract(" + record + ",'$.cwd'),t.cwd),?)=1"
		args = append(args, string(paths))
	}
	return owner, args
}

// Integrity checks must include uncertain authority, but a known denial must
// exclude the record before any defect can affect the caller's inventory.
func inventoryIntegrityScopeSQL(s RunInventoryScope) (string, []any) {
	legacy := "(t.principal=?)"
	if s.LegacyLocalDefault {
		legacy = "(t.principal=? OR t.principal IN ('','default'))"
	}
	args := []any{s.Principal, s.Principal}
	principal := "(json_type(a.record,'$.launch.Authority.Principal') IS NOT 'text' OR json_extract(a.record,'$.launch.Authority.Principal')='' OR json_extract(a.record,'$.launch.Authority.Principal')=?"
	if s.LegacyLocalDefault {
		principal += " OR json_extract(a.record,'$.launch.Authority.Principal') IN ('','default')"
	}
	principal += ")"
	if s.UserID != "" {
		principal += " AND (json_type(a.record,'$.launch.Authority.UserID') IS NOT 'text' OR json_extract(a.record,'$.launch.Authority.UserID')='' OR json_extract(a.record,'$.launch.Authority.UserID')=?)"
		args = append(args, s.UserID)
	}
	owner := "CASE WHEN a.rowid IS NULL THEN " + legacy + " WHEN json_valid(a.record)=0 THEN 1 ELSE " + principal + " END"
	if s.EnforcePaths {
		paths, _ := json.Marshal(s.AllowedPaths)
		if s.AllowedPaths == nil {
			paths = []byte("[]")
		}
		record := "CASE WHEN json_valid(a.record) THEN a.record ELSE '{}' END"
		cwd := "COALESCE(NULLIF(CASE WHEN json_type(" + record + ",'$.cwd')='text' THEN json_extract(" + record + ",'$.cwd') END,''),NULLIF(CASE WHEN typeof(t.cwd)='text' THEN t.cwd END,''))"
		pathScope := "wt_inventory_path_allowed(" + cwd + ",?)=1"
		if len(s.AllowedPaths) > 0 {
			pathScope = "(" + cwd + " IS NULL OR " + pathScope + ")"
		}
		owner += " AND " + pathScope
		args = append(args, string(paths))
	}
	return owner, args
}

const inventoryFrom = " FROM tasks t LEFT JOIN agent_runs a ON a.id=t.id WHERE t.type='agent_run' AND "

// Optional JSON members omitted by the durable writer mean a known zero value.
// Explicit NULLs, wrong types and inconsistent projections mean unavailable
// evidence. IS/IS NOT keep SQL NULL from turning integrity failures into false.
func inventoryIntegritySQL(scope RunInventoryScope) string {
	bad := []string{
		"typeof(t.id)!='text' OR t.id='' OR t.type IS NOT 'agent_run'",
		"typeof(t.principal)!='text'",
		"typeof(t.agent)!='text' OR t.agent='' OR typeof(t.model)!='text' OR typeof(t.cwd)!='text' OR t.cwd='' OR typeof(t.isolation)!='text' OR t.isolation=''",
		"t.status IS NULL OR t.status NOT IN ('pending','running','done','failed','timeout','stopped')",
		"wt_inventory_time_valid(t.created_at)=0",
		"t.started_at IS NOT NULL AND wt_inventory_time_valid(t.started_at)=0",
		"t.finished_at IS NOT NULL AND wt_inventory_time_valid(t.finished_at)=0",
		"t.parent_id IS NOT NULL AND (typeof(t.parent_id)!='text' OR t.parent_id='')",
		"typeof(t.timeout_seconds)!='integer' OR t.timeout_seconds<0",
		"a.rowid IS NULL AND t.output IS NOT NULL AND typeof(t.output)!='text'",
	}
	modern := []string{
		"json_type(a.record) IS NOT 'object'",
		"typeof(a.id)!='text' OR a.id=''",
		"typeof(a.session_id)!='text' OR a.session_id='' OR typeof(a.principal)!='text'",
		"a.request_key IS NOT NULL AND (typeof(a.request_key)!='text' OR a.request_key='' OR length(a.request_key)>200)",
		"json_extract(a.record,'$.run_id') IS NOT t.id",
		"json_extract(a.record,'$.session_id') IS NOT a.session_id",
		"json_extract(a.record,'$.launch.Authority.Principal') IS NOT a.principal OR t.principal IS NOT a.principal",
		"COALESCE(json_extract(a.record,'$.idempotency_key'),'') IS NOT COALESCE(a.request_key,'')",
		"COALESCE(json_extract(a.record,'$.parent_id'),'') IS NOT COALESCE(t.parent_id,'')",
		"json_extract(a.record,'$.agent') IS NOT t.agent OR json_extract(a.record,'$.model') IS NOT t.model OR json_extract(a.record,'$.cwd') IS NOT t.cwd",
		// Admission normalizes an unspecified isolation to 'none'.
		"COALESCE(NULLIF(json_extract(a.record,'$.isolation'),''),'none') IS NOT t.isolation",
		"json_type(a.record,'$.timeout_seconds') IS NOT 'integer' OR json_extract(a.record,'$.timeout_seconds') IS NOT t.timeout_seconds",
		"json_type(a.record,'$.result') IS NOT 'object' OR json_extract(a.record,'$.result.status') IS NOT t.status",
		"t.status IN ('done','failed','timeout','stopped') AND t.finished_at IS NULL",
		"wt_inventory_time_valid(json_extract(a.record,'$.created_at'))=0 OR strftime('%Y-%m-%dT%H:%M:%SZ',json_extract(a.record,'$.created_at')) IS NOT strftime('%Y-%m-%dT%H:%M:%SZ',t.created_at)",
	}
	for _, path := range []string{"run_id", "session_id", "agent", "model", "cwd", "isolation", "phase", "created_at", "launch.Authority.Principal", "launch.Authority.UserID", "result.status", "result.text", "result.started_at", "result.ended_at"} {
		modern = append(modern, "json_type(a.record,'$."+path+"') IS NOT 'text'")
	}
	for _, path := range []string{"run_id", "session_id", "agent", "cwd", "phase"} {
		modern = append(modern, "json_extract(a.record,'$."+path+"')=''")
	}
	if scope.UserID != "" {
		modern = append(modern, "json_extract(a.record,'$.launch.Authority.UserID')=''")
	}
	for _, path := range []string{"label", "parent_id", "idempotency_key", "result.failure_kind", "result.containment_error"} {
		modern = append(modern, "json_type(a.record,'$."+path+"') IS NOT NULL AND json_type(a.record,'$."+path+"')!='text'")
	}
	modern = append(modern, "json_type(a.record,'$.result.surviving_descendants') IS NOT NULL AND json_type(a.record,'$.result.surviving_descendants')!='array'")
	for _, dates := range [][2]string{{"result.started_at", "started_at"}, {"result.ended_at", "finished_at"}} {
		// Go serializes a zero time even for an unstarted or unfinished run.
		date := "NULLIF(strftime('%Y-%m-%dT%H:%M:%SZ',json_extract(a.record,'$." + dates[0] + "')),'0001-01-01T00:00:00Z')"
		modern = append(modern, "(json_extract(a.record,'$."+dates[0]+"') IS NOT '0001-01-01T00:00:00Z' AND wt_inventory_time_valid(json_extract(a.record,'$."+dates[0]+"'))=0) OR "+date+" IS NOT strftime('%Y-%m-%dT%H:%M:%SZ',t."+dates[1]+")")
	}
	bad = append(bad, "a.rowid IS NOT NULL AND CASE WHEN json_valid(a.record)=0 THEN 1 ELSE ("+strings.Join(modern, " OR ")+") END")
	return "(" + strings.Join(bad, " OR ") + ")"
}

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
	eligible := scope + " AND t.type='agent_run'"
	if q.RequestKey != "" {
		// Validate the owner's admissions independently of this projection before
		// using it as an exact filter. A missing key cannot establish absence.
		eligible += " AND a.request_key=?"
		args = append(args, q.RequestKey)
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	// Candidates include unavailable projections and unknown retention dates.
	// Check available authority first: a denied corrupt row is not observable.
	var corrupt int
	integrityScope, integrityArgs := inventoryIntegrityScopeSQL(q.Scope)
	integrity := "SELECT count(*)" + from + "(" + integrityScope + ") AND " + inventoryIntegritySQL(q.Scope)
	if err = tx.QueryRowContext(ctx, prefix+integrity, append(append([]any{}, candidateArgs...), integrityArgs...)...).Scan(&corrupt); err != nil {
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
 COALESCE(NULLIF(json_extract(a.record,'$.isolation'),''),t.isolation),t.status,
 COALESCE(json_extract(a.record,'$.phase'),'terminal'), COALESCE(t.parent_id,''),COALESCE(a.request_key,''),
 strftime('%Y-%m-%dT%H:%M:%SZ',t.created_at),COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ',t.started_at),''),COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ',t.finished_at),''),t.timeout_seconds,
 COALESCE(json_extract(a.record,'$.result.failure_kind'),CASE WHEN t.status IN ('failed','timeout','stopped') THEN 'unknown_outcome' ELSE '' END),
 t.status IN ('done','failed','timeout','stopped'),a.rowid IS NULL,
 CASE WHEN COALESCE(json_array_length(json_extract(a.record,'$.result.surviving_descendants')),0)>0
 OR COALESCE(json_extract(a.record,'$.result.containment_error'),'')!='' THEN 'attention' ELSE 'unknown' END,
 CASE WHEN t.status NOT IN ('done','failed','timeout','stopped') THEN 'pending'
 WHEN t.status='done' AND (a.rowid IS NOT NULL AND json_type(a.record,'$.result.text')='text' OR a.rowid IS NULL AND t.output IS NOT NULL) THEN 'available'
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

// Candidates contain IDs only. Task indexes constrain active/recent history,
// including NULL date evidence. An additional principal-indexed metadata scan
// reconciles modern admissions independently of their task projection. It must
// inspect that owner's projection dates, but routine scans read no known expired
// durable JSON. Exact-key reconciliation validates archived admission metadata.
func inventorySelection(q RunInventoryQuery) (string, string, []any) {
	owner := "principal=?"
	if q.Scope.LegacyLocalDefault {
		owner = "(principal=? OR principal IN ('','default'))"
	}
	args := []any{q.Scope.Principal}
	var candidates string
	if q.RequestKey != "" {
		// Exact reconciliation covers archived admissions too. Scan metadata by
		// both owner projections, then check durable key consistency before the
		// filter. This also catches a lost agent_runs principal.
		candidates = "SELECT id FROM agent_runs WHERE " + owner
		taskOwner := strings.ReplaceAll(owner, "principal", "t.principal")
		candidates += " UNION SELECT a.id FROM tasks t INDEXED BY idx_tasks_run_inventory_active JOIN agent_runs a ON a.id=t.id WHERE t.type='agent_run' AND t.status NOT IN ('done','failed','timeout','stopped') AND " + taskOwner
		candidates += " UNION SELECT a.id FROM tasks t INDEXED BY idx_tasks_run_inventory_recent JOIN agent_runs a ON a.id=t.id WHERE t.type='agent_run' AND t.status IN ('done','failed','timeout','stopped') AND " + taskOwner
		args = append(args, q.Scope.Principal, q.Scope.Principal)
	} else {
		candidates = "SELECT id FROM tasks INDEXED BY idx_tasks_run_inventory_active WHERE type='agent_run' AND status NOT IN ('done','failed','timeout','stopped') AND " + owner
		candidates += " UNION SELECT id FROM tasks INDEXED BY idx_tasks_run_inventory_recent WHERE type='agent_run' AND status IN ('done','failed','timeout','stopped') AND " + owner + " AND julianday(COALESCE(finished_at,created_at))>=julianday(?)"
		candidates += " UNION SELECT id FROM tasks INDEXED BY idx_tasks_run_inventory_recent WHERE type='agent_run' AND status IN ('done','failed','timeout','stopped') AND " + owner + " AND julianday(COALESCE(finished_at,created_at)) IS NULL"
		candidates += " UNION SELECT id FROM tasks INDEXED BY idx_tasks_run_inventory_recent WHERE type='agent_run' AND status IN ('done','failed','timeout','stopped') AND " + owner + " AND wt_inventory_time_valid(COALESCE(finished_at,created_at))=0"
		durableOwner := "a.principal=?"
		if q.Scope.LegacyLocalDefault {
			durableOwner = "(a.principal=? OR a.principal IN ('','default'))"
		}
		candidates += " UNION SELECT a.id FROM agent_runs a LEFT JOIN tasks t ON t.id=a.id WHERE " + durableOwner + " AND (t.id IS NULL OR t.type IS NOT 'agent_run' OR t.status IS NULL OR t.status NOT IN ('done','failed','timeout','stopped') OR wt_inventory_time_valid(t.finished_at)=0 OR julianday(t.finished_at)>=julianday(?))"
		args = append(args, q.Scope.Principal, q.TerminalSince.UTC().Format(timeFmt), q.Scope.Principal, q.Scope.Principal, q.Scope.Principal, q.TerminalSince.UTC().Format(timeFmt))
	}
	return "WITH inventory_candidates AS (" + candidates + ") ", " FROM inventory_candidates c LEFT JOIN tasks t ON t.id IS c.id LEFT JOIN agent_runs a ON a.id IS c.id WHERE ", args
}
