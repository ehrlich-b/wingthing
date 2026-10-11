package runinspect

import (
	"fmt"
	"io"
	"strconv"
)

// RenderText renders only the typed report. Arbitrary provider text can appear
// solely in the separately labelled, explicitly requested escaped preview.
func RenderText(w io.Writer, report *Report) error {
	write := func(format string, args ...any) error { _, err := fmt.Fprintf(w, format, args...); return err }
	quote := strconv.QuoteToASCII
	refs := func(label string, sources []Ref) error {
		for _, ref := range sources {
			if err := write("    %s: %s %s %s\n", label, quote(ref.Path), quote(ref.Record), quote(ref.Field)); err != nil {
				return err
			}
		}
		return nil
	}
	issues := func(values []Issue) error {
		for _, issue := range values {
			if err := write("  issue=%s ids=%s\n", issue.Code, quoteList(issue.IDs)); err != nil {
				return err
			}
			if err := refs("source", issue.Sources); err != nil {
				return err
			}
		}
		return nil
	}
	if err := write("Offline run inspection v%d; captured %s\n", report.Version, report.CapturedAt.UTC().Format("2006-01-02T15:04:05Z")); err != nil {
		return err
	}
	for _, run := range report.Runs {
		if err := write("Run %s execution %s: %s (evidence: %s)\n  phase=%s wing=%s egg=%s acknowledgement=%s\n", quote(run.ID), quote(run.SessionID), run.Diagnosis, run.EvidenceState, run.Phase, run.WingStatus, run.EggStatus, run.Acknowledgement); err != nil {
			return err
		}
		if run.ParentID != "" {
			if err := write("  parent=%s\n", quote(run.ParentID)); err != nil {
				return err
			}
		}
		if run.ConversationID != "" {
			if err := write("  conversation=%s current_execution=%s\n", quote(run.ConversationID), quote(run.CurrentSessionID)); err != nil {
				return err
			}
		}
		if err := refs("source", run.Sources); err != nil {
			return err
		}
		for _, receipt := range run.Receipts {
			if err := write("  receipt %s native_session=%s turn=%s failure=%s\n", quote(receipt.Source.Path), quote(receipt.ProviderSessionID), quote(receipt.TurnID), receipt.FailureKind); err != nil {
				return err
			}
			if err := refs("source", []Ref{receipt.Source}); err != nil {
				return err
			}
		}
		for _, out := range run.Output {
			if err := write("  output %s %s: available=%t bytes=%d omitted=%d sha256=%s\n", quote(out.Source.Path), quote(out.Source.Record), out.Available, out.Bytes, out.OmittedBytes, out.SHA256); err != nil {
				return err
			}
			if out.Preview != "" {
				if err := write("    untrusted output preview: %s\n", out.Preview); err != nil {
					return err
				}
			}
			if err := refs("source", []Ref{out.Source}); err != nil {
				return err
			}
		}
		if err := issues(run.Issues); err != nil {
			return err
		}
		for _, action := range run.Actions {
			if err := write("  suggested_action=%s (manual review required)\n", action.Kind); err != nil {
				return err
			}
			if err := refs("basis", action.Basis); err != nil {
				return err
			}
			for _, requirement := range action.Requires {
				if err := write("    requires: %s\n", requirement); err != nil {
					return err
				}
			}
		}
	}
	for _, task := range report.LegacyTasks {
		if err := write("Legacy task %s: %s; execution identity unavailable\n", quote(task.ID), task.Diagnosis); err != nil {
			return err
		}
		if err := write("  parent=%s status=%s output_available=%t bytes=%d omitted=%d sha256=%s\n", quote(task.ParentID), task.Status, task.Output.Available, task.Output.Bytes, task.Output.OmittedBytes, task.Output.SHA256); err != nil {
			return err
		}
		if task.Output.Preview != "" {
			if err := write("    untrusted output preview: %s\n", task.Output.Preview); err != nil {
				return err
			}
		}
		if err := refs("source", []Ref{task.Source, task.Output.Source}); err != nil {
			return err
		}
		if err := issues(task.Issues); err != nil {
			return err
		}
	}
	for _, c := range report.Conversations {
		if err := write("Conversation %s checkpoint=%s\n", quote(c.ID), c.Checkpoint.State); err != nil {
			return err
		}
		if err := write("  current_execution=%s root=%s parent=%s executions=%s\n", quote(c.SessionID), quote(c.RootID), quote(c.ParentID), quoteList(c.Executions)); err != nil {
			return err
		}
		if err := refs("source", append(append([]Ref{}, c.Sources...), c.Checkpoint.Content.Source)); err != nil {
			return err
		}
		if err := issues(c.Issues); err != nil {
			return err
		}
	}
	for _, a := range report.Archives {
		if err := write("Archive execution %s: %s rotation_pending=%t\n", quote(a.SessionID), a.State, a.RotationPending); err != nil {
			return err
		}
		if err := write("  agent=%s native_session=%s\n", quote(a.Agent), quote(a.ProviderSessionID)); err != nil {
			return err
		}
		if err := refs("source", a.Sources); err != nil {
			return err
		}
	}
	for _, a := range report.CheckpointAttempts {
		if err := write("Checkpoint request %s conversation %s: %s; %s\n", quote(a.RequestID), quote(a.ConversationID), a.FenceState, a.Acknowledgement); err != nil {
			return err
		}
		if err := write("  execution=%s\n", quote(a.SessionID)); err != nil {
			return err
		}
		if err := refs("source", []Ref{a.Source}); err != nil {
			return err
		}
	}
	for _, edge := range report.Edges {
		if err := write("Edge %s %s -> %s: %s\n", edge.Kind, quote(edge.From), quote(edge.To), edge.State); err != nil {
			return err
		}
		if err := refs("source", edge.Sources); err != nil {
			return err
		}
	}
	if err := issues(report.Issues); err != nil {
		return err
	}
	if err := write("Required host observations before any actual recovery:\n"); err != nil {
		return err
	}
	for _, observation := range report.RequiredHostObservations {
		if err := write("  %s\n", observation); err != nil {
			return err
		}
	}
	return nil
}

func quoteList(ids []string) string {
	out := "["
	for i, id := range ids {
		if i > 0 {
			out += ", "
		}
		out += strconv.QuoteToASCII(id)
	}
	return out + "]"
}
