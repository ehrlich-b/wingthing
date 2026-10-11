package runinspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Inspect returns a deterministic report of observed evidence, not live status.
// A hash mismatch before or after reading fails the entire copy's trust gate.
// Invalid individual records remain issues while unrelated runs are inspected.
func Inspect(ctx context.Context, path string, opts Options) (*Report, error) {
	if opts.PreviewBytes < 0 || opts.PreviewBytes > 4096 {
		return nil, errors.New("preview bytes must be between 0 and 4096")
	}
	s, err := openSnapshot(ctx, path, opts)
	if err != nil {
		return nil, err
	}
	defer s.root.Close()
	r := records{tables: map[string]bool{}}
	s.database(ctx, &r)
	for _, path := range s.paths() {
		parts := strings.Split(path, "/")
		if len(parts) != 3 || parts[0] != "eggs" || !strings.HasPrefix(parts[2], "run.") || !strings.HasSuffix(parts[2], ".json") {
			continue
		}
		data, err := s.read(path)
		if err != nil {
			return nil, err
		}
		e := eggRecord{source: Ref{Path: path}}
		e.valid = json.Unmarshal(data, &e) == nil && e.Version == 1 && e.Result.RunID != "" && e.Result.SessionID != ""
		if e.Result.SessionID != parts[1] || parts[2] != fmt.Sprintf("run.%s.json", digest([]byte(e.Result.RunID))) {
			e.valid = false
			r.issue("egg_artifact_identity_conflict", e.source, e.Result.RunID, e.Result.SessionID, parts[1])
		}
		if !e.valid {
			r.issue("partial_or_unsupported_egg_record", e.source, e.Result.RunID)
		}
		r.eggs = append(r.eggs, e)
	}
	report := &Report{Version: ReportVersion, CapturedAt: s.manifest.CapturedAt, InputFiles: append([]File{}, s.manifest.Files...), Runs: []Run{}, Conversations: []Conversation{}, Archives: []Archive{}, Edges: []Edge{}, Issues: r.issues, RequiredHostObservations: []string{
		"Verify the snapshot capture time and that no newer durable records exist.",
		"Verify the exact session PID, process identity, provider thread/turn and absolute deadline on the real host.",
		"Read the latest run receipt and complete output for the same run and execution IDs; never resend ambiguous input.",
		"Reload conversation revision and delivered cursor; wake delivery and output existence do not acknowledge events.",
		"Verify ownership, current execution, provider resume support, complete archive and policy before any manual recovery.",
	}}
	report.graph(&r, opts)
	report.identityGraph(&r)
	report.finalizeEvidence()
	if err := s.verify(ctx); err != nil {
		return nil, err
	}
	sort.Slice(report.InputFiles, func(i, j int) bool { return report.InputFiles[i].Path < report.InputFiles[j].Path })
	sort.Slice(report.Runs, func(i, j int) bool {
		if report.Runs[i].ID == report.Runs[j].ID {
			return report.Runs[i].SessionID < report.Runs[j].SessionID
		}
		return report.Runs[i].ID < report.Runs[j].ID
	})
	return report, nil
}

func refField(ref Ref, field string) Ref { ref.Field = field; return ref }
func terminal(status string) bool {
	return status == "done" || status == "failed" || status == "timeout" || status == "stopped"
}
func knownStatus(status string) string {
	if terminal(status) || status == "pending" || status == "running" {
		return status
	}
	return "unknown"
}
func knownPhase(phase string) string {
	switch phase {
	case "queued", "admitted", "spawning", "spawned", "submitting", "observing", "terminal":
		return phase
	}
	return "unknown"
}

func output(text *string, ref Ref, preview int) Output {
	o := Output{Source: ref}
	if text == nil {
		return o
	}
	o.Available = true
	o.Bytes = int64(len(*text))
	o.SHA256 = digest([]byte(*text))
	o.OmittedBytes = o.Bytes
	if preview > 0 {
		n := len(*text)
		if n > preview {
			n = preview
		}
		// QuoteToASCII escapes controls, ANSI, bidi, invalid UTF-8 and newlines.
		o.Preview = strconv.QuoteToASCII((*text)[:n])
		o.OmittedBytes -= int64(n)
	}
	return o
}

func (report *Report) edge(kind, from, to, state string, refs ...Ref) {
	report.Edges = append(report.Edges, Edge{Kind: kind, From: from, To: to, State: state, Sources: refs})
}
func (run *Run) issue(code string, ids []string, refs ...Ref) {
	run.Issues = append(run.Issues, Issue{Code: code, IDs: ids, Sources: refs})
}

func (report *Report) graph(r *records, opts Options) {
	byID := map[string][]wingRow{}
	byEgg := map[string][]eggRecord{}
	ids := map[string]bool{}
	for _, w := range r.wings {
		byID[w.id] = append(byID[w.id], w)
		ids[w.id] = true
	}
	for _, e := range r.eggs {
		if e.Result.RunID != "" {
			byEgg[e.Result.RunID] = append(byEgg[e.Result.RunID], e)
			ids[e.Result.RunID] = true
		}
	}
	order := make([]string, 0, len(ids))
	for id := range ids {
		order = append(order, id)
	}
	sort.Strings(order)
	for _, id := range order {
		ws, es := byID[id], byEgg[id]
		run := Run{ID: id, Phase: "unknown", WingStatus: "absent", EggStatus: "absent", Diagnosis: "missing_evidence", Acknowledgement: "unknown", Output: []Output{}, Sources: []Ref{}, Issues: []Issue{}, Actions: []Action{}}
		if len(ws) > 1 || len(es) > 1 {
			run.issue("conflicting_run_records", []string{id})
			for _, w := range ws {
				run.Sources = append(run.Sources, w.source)
			}
			for _, e := range es {
				run.Sources = append(run.Sources, e.source)
			}
		} else {
			var w *wingRow
			var e *eggRecord
			if len(ws) == 1 {
				w = &ws[0]
				run.SessionID = w.session
				run.ParentID = w.record.ParentID
				run.Sources = append(run.Sources, w.source)
				if !w.valid {
					run.issue("partial_run_record", []string{id}, w.source)
				} else {
					run.Phase = knownPhase(w.record.Phase)
					run.WingStatus = knownStatus(w.record.Result.Status)
					if w.record.ID != w.id || w.record.SessionID != w.session || w.record.Result.RunID != w.id || w.record.Result.SessionID != w.session || w.record.Launch.Authority.Principal != w.principal || w.revision < 1 {
						run.issue("wing_identity_or_owner_conflict", []string{w.id, w.record.ID, w.session, w.record.SessionID, w.record.Result.RunID, w.record.Result.SessionID}, w.source)
					}
					run.Output = append(run.Output, output(w.record.Result.Text, refField(w.source, "result.text"), opts.PreviewBytes))
				}
			}
			if len(es) == 1 {
				e = &es[0]
				run.Sources = append(run.Sources, e.source)
				if run.SessionID == "" {
					run.SessionID = e.Result.SessionID
				}
				run.EggStatus = knownStatus(e.Result.Status)
				if !e.valid {
					run.issue("partial_or_unsupported_egg_record", []string{id}, e.source)
				} else {
					run.Output = append(run.Output, output(e.Result.Text, refField(e.source, "result.text"), opts.PreviewBytes))
				}
			}
			if w != nil && e != nil && (w.session != e.Result.SessionID) {
				run.issue("run_execution_conflict", []string{id, w.session, e.Result.SessionID}, w.source, e.source)
			}
			if w != nil && e != nil && w.valid && e.valid && terminal(w.record.Result.Status) && terminal(e.Result.Status) {
				a, b := w.record.Result, e.Result
				if a.Status != b.Status || a.ProviderSessionID != b.ProviderSessionID || a.TurnID != b.TurnID || (a.Text != nil && b.Text != nil && *a.Text != *b.Text) {
					run.issue("terminal_receipts_conflict", []string{id, w.session, e.Result.SessionID}, w.source, e.source)
				}
			}
			state := "absent"
			if e != nil && e.valid {
				state = "observed"
			}
			if len(run.Issues) > 0 {
				state = "inconsistent"
			}
			report.edge("run_execution", id, run.SessionID, state, run.Sources...)
			if run.ParentID != "" {
				state := "observed"
				if len(byID[run.ParentID]) == 0 && len(byEgg[run.ParentID]) == 0 {
					state = "absent"
					run.issue("missing_parent", []string{id, run.ParentID}, refField(w.source, "parent_id"))
				}
				report.edge("run_parent", id, run.ParentID, state, refField(w.source, "parent_id"))
			}
			if w == nil {
				run.Acknowledgement = "wing_record_absent"
			} else if e != nil && e.valid && terminal(e.Result.Status) && !terminal(w.record.Result.Status) {
				run.Acknowledgement = "terminal_receipt_not_in_wing"
			} else if run.Phase == "submitting" {
				run.Acknowledgement = "submission_intent_only"
			} else if terminal(run.WingStatus) {
				run.Acknowledgement = "terminal_in_wing"
			} else {
				run.Acknowledgement = "no_terminal_acknowledgement"
			}
			report.diagnose(&run, w, e, r)
		}

		run.Actions = append(run.Actions, Action{Kind: "inspect", Basis: run.Sources, Requires: []string{"Review the cited records and snapshot capture time."}})
		if run.Diagnosis == "running_at_snapshot" {
			run.Actions = append(run.Actions, Action{Kind: "wait_for_known_completion", Basis: run.Sources, Requires: []string{"Verify the same execution is still alive on the host; a snapshot cannot establish current liveness."}})
		}
		if run.Diagnosis == "missing_evidence" || run.Diagnosis == "admitted_outcome_unknown" || run.Diagnosis == "inconsistent_evidence" {
			run.Actions = append(run.Actions, Action{Kind: "missing_evidence", Basis: run.Sources, Requires: []string{"Obtain the missing or conflicting exact-identity records before choosing a recovery action."}})
		}
		report.Runs = append(report.Runs, run)
	}
}

func (report *Report) diagnose(run *Run, w *wingRow, e *eggRecord, r *records) {
	var receipts []resultRecord
	if w != nil && w.valid {
		receipts = append(receipts, w.record.Result)
	}
	if e != nil && e.valid {
		receipts = append(receipts, e.Result)
	}
	for _, receipt := range receipts {
		if terminal(receipt.Status) {
			if receipt.Status == "done" {
				run.Diagnosis = "completed_output_unavailable"
				for _, out := range run.Output {
					if out.Available {
						run.Diagnosis = "completed_with_output"
					}
				}
			} else {
				run.Diagnosis = receipt.Status
			}
			return
		}
	}
	for _, receipt := range receipts {
		started, startErr := time.Parse(time.RFC3339Nano, receipt.StartedAt)
		deadline, deadlineErr := time.Parse(time.RFC3339Nano, receipt.Deadline)
		if e != nil && e.valid && receipt.Status == "running" && startErr == nil && !started.IsZero() && deadlineErr == nil && deadline.After(report.CapturedAt) {
			run.Diagnosis = "running_at_snapshot"
			return
		}
	}
	if len(receipts) > 0 {
		run.Diagnosis = "admitted_outcome_unknown"
	}
}

// Output paths are computed only from observed receipt identities. No path
// embedded in provider content or launch metadata is ever opened.
func outputPath(session, id string) string {
	return filepath.ToSlash(filepath.Join("eggs", session, "run."+digest([]byte(id))+".json"))
}
