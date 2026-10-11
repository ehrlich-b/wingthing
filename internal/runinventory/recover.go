// Package runinventory is a bounded first consumer of the wing control API.
// It starts without known IDs and has no store, runtime or transport authority.
package runinventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type Call func(context.Context, string, json.RawMessage) (map[string]any, error)

type Options struct {
	Limit                 int    `json:"limit,omitempty"`
	MaxPages              int    `json:"-"`
	RequestKey            string `json:"idempotency_key,omitempty"`
	TerminalWindowSeconds int    `json:"terminal_window_seconds,omitempty"`
}

type RecoveredRun struct {
	WingID    string `json:"wing_id"`
	ID        string `json:"run_id"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	Status    string `json:"status"`
	Cleanup   string `json:"cleanup"`
	Output    string `json:"output,omitempty"`
	ReadError string `json:"read_error,omitempty"`
}

type Counts struct {
	Total             int `json:"total"`
	Completed         int `json:"completed"`
	Running           int `json:"running"`
	Interrupted       int `json:"interrupted"`
	UnavailableOutput int `json:"unavailable_output"`
	TerminalOther     int `json:"terminal_other"`
	UnavailableRun    int `json:"unavailable_run"`
	UnknownCleanup    int `json:"unknown_cleanup"`
}

type Recovery struct {
	Runs             []RecoveredRun `json:"runs"`
	Counts           Counts         `json:"counts"`
	Pages            int            `json:"pages"`
	Complete         bool           `json:"complete"`
	SnapshotRevision string         `json:"snapshot_revision"`
}

type inventoryRow struct {
	ID                 string `json:"run_id"`
	WingID             string `json:"wing_id"`
	SessionID          string `json:"session_id"`
	Terminal           bool   `json:"terminal"`
	OutputAvailability string `json:"output_availability"`
	Cleanup            string `json:"cleanup"`
}

type inventoryPage struct {
	Runs             []inventoryRow `json:"runs"`
	Count            int            `json:"count"`
	WingID           string         `json:"wing_id"`
	NextCursor       string         `json:"next_cursor"`
	SnapshotRevision string         `json:"snapshot_revision"`
}

type runState struct {
	ID          string  `json:"run_id"`
	WingID      string  `json:"wing_id"`
	Status      string  `json:"status"`
	FailureKind string  `json:"failure_kind"`
	Ready       bool    `json:"ready"`
	Output      *string `json:"output"`
}

func (s runState) terminal() bool {
	return s.Status == "done" || s.Status == "failed" || s.Status == "timeout" || s.Status == "stopped"
}

func decode(data map[string]any, out any) error {
	if data == nil {
		return errors.New("missing control response")
	}
	if message, ok := data["error"].(string); ok && message != "" {
		// agent_result can contain semantic error text alongside a valid identity.
		if data["run_id"] == nil {
			return fmt.Errorf("control failed: %s", message)
		}
	}
	wire, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if len(wire) > 1024*1024 {
		return errors.New("control response exceeds 1 MiB")
	}
	return json.Unmarshal(wire, out)
}

// Recover finishes discovery before interpreting absence. It reads status for
// each discovered ID and reads a terminal result once. It never launches,
// steers, stops, waits, polls or reconstructs archived output.
func Recover(ctx context.Context, call Call, opts Options) (Recovery, error) {
	result := Recovery{Runs: []RecoveredRun{}}
	if opts.Limit == 0 {
		opts.Limit = 100
	}
	if opts.MaxPages == 0 {
		opts.MaxPages = 20
	}
	if call == nil || opts.Limit < 1 || opts.Limit > 200 || opts.MaxPages < 1 || opts.MaxPages > 100 {
		return result, errors.New("recovery requires page size 1-200 and max_pages 1-100")
	}
	rows := []inventoryRow{}
	seen := map[string]bool{}
	cursors := map[string]bool{}
	cursor := ""
	wing := ""
	for pageIndex := 0; pageIndex < opts.MaxPages; pageIndex++ {
		args := map[string]any{"limit": opts.Limit, "cursor": cursor}
		if opts.RequestKey != "" {
			args["idempotency_key"] = opts.RequestKey
		}
		if opts.TerminalWindowSeconds != 0 {
			args["terminal_window_seconds"] = opts.TerminalWindowSeconds
		}
		wire, _ := json.Marshal(args)
		data, err := call(ctx, "agent_list", wire)
		if err != nil {
			return result, fmt.Errorf("discovery page %d: %w", pageIndex+1, err)
		}
		var page inventoryPage
		if err = decode(data, &page); err != nil {
			return result, err
		}
		if page.Runs == nil || page.Count != len(page.Runs) || page.Count > opts.Limit || page.WingID == "" || page.SnapshotRevision == "" {
			return result, errors.New("incomplete inventory response")
		}
		if wing == "" {
			wing = page.WingID
			result.SnapshotRevision = page.SnapshotRevision
		}
		if page.WingID != wing || result.SnapshotRevision != page.SnapshotRevision {
			return result, errors.New("inventory scope changed between pages")
		}
		for _, row := range page.Runs {
			key := row.WingID + "\x00" + row.ID
			if row.ID == "" || row.WingID != wing || seen[key] {
				return result, errors.New("invalid, foreign or duplicate inventory identity")
			}
			seen[key] = true
			rows = append(rows, row)
			if len(rows) > 10000 {
				return result, errors.New("recovery exceeds 10000 run bound")
			}
		}
		result.Pages++
		if page.NextCursor == "" {
			result.Complete = true
			break
		}
		if page.Count == 0 || len(page.NextCursor) > 4096 || cursors[page.NextCursor] {
			return result, errors.New("inventory continuation made no progress")
		}
		cursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	if !result.Complete {
		return result, errors.New("recovery page bound reached; scan incomplete")
	}
	for _, row := range rows {
		r := RecoveredRun{ID: row.ID, WingID: row.WingID, SessionID: row.SessionID, Cleanup: row.Cleanup}
		if r.Cleanup == "" {
			r.Cleanup = "unknown"
		}
		if r.Cleanup == "unknown" {
			result.Counts.UnknownCleanup++
		}
		wire, _ := json.Marshal(map[string]any{"run_id": row.ID})
		data, err := call(ctx, "agent_status", wire)
		var state runState
		if err == nil {
			err = decode(data, &state)
		}
		if err == nil && (state.ID != row.ID || state.WingID != "" && state.WingID != row.WingID) {
			err = errors.New("status identity mismatch")
		}
		if err != nil {
			r.State = "unavailable"
			r.ReadError = err.Error()
			result.Counts.UnavailableRun++
		} else {
			r.Status = state.Status
			if !state.terminal() {
				if state.Status == "pending" || state.Status == "running" {
					r.State = "running"
					result.Counts.Running++
				} else {
					r.State = "unavailable"
					r.ReadError = "unknown foreground lifecycle"
					result.Counts.UnavailableRun++
				}
			} else {
				resultArgs, _ := json.Marshal(map[string]any{"run_id": row.ID, "max_chars": 8192})
				data, readErr := call(ctx, "agent_result", resultArgs)
				var outcome runState
				if readErr == nil {
					readErr = decode(data, &outcome)
				}
				if readErr == nil && (outcome.ID != row.ID || outcome.WingID != "" && outcome.WingID != row.WingID || !outcome.Ready) {
					readErr = errors.New("result identity or readiness mismatch")
				}
				if readErr == nil && !outcome.terminal() {
					readErr = errors.New("result has no terminal lifecycle")
				}
				// The returned result is the caller-visible terminal receipt. MCP
				// cancellation can freeze it before the underlying run finishes,
				// so its lifecycle takes precedence over an earlier status read.
				if readErr == nil {
					state = outcome
					r.Status = outcome.Status
				}
				switch {
				case state.FailureKind == "unknown_outcome" || state.FailureKind == "provider_exit" || state.FailureKind == "stopped" || state.Status == "stopped":
					r.State = "interrupted"
					result.Counts.Interrupted++
				case state.FailureKind != "":
					r.State = "terminal_other"
					result.Counts.TerminalOther++
				case state.Status == "done" && (readErr != nil || outcome.Output == nil || row.OutputAvailability == "unavailable"):
					r.State = "unavailable_output"
					result.Counts.UnavailableOutput++
				case state.Status == "done":
					r.State = "completed"
					r.Output = *outcome.Output
					result.Counts.Completed++
				default:
					r.State = "terminal_other"
					result.Counts.TerminalOther++
				}
				if readErr != nil {
					r.ReadError = readErr.Error()
				}
			}
		}
		result.Runs = append(result.Runs, r)
		result.Counts.Total++
	}
	return result, nil
}
