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
		for _, out := range run.Output {
			if err := write("  output %s %s: available=%t bytes=%d omitted=%d sha256=%s\n", quote(out.Source.Path), quote(out.Source.Record), out.Available, out.Bytes, out.OmittedBytes, out.SHA256); err != nil {
				return err
			}
			if out.Preview != "" {
				if err := write("    untrusted output preview: %s\n", out.Preview); err != nil {
					return err
				}
			}
		}
		for _, issue := range run.Issues {
			if err := write("  issue=%s ids=%s\n", issue.Code, quoteList(issue.IDs)); err != nil {
				return err
			}
		}
		for _, action := range run.Actions {
			if err := write("  suggested_action=%s (manual review required)\n", action.Kind); err != nil {
				return err
			}
			for _, ref := range action.Basis {
				if err := write("    basis: %s %s %s\n", quote(ref.Path), quote(ref.Record), quote(ref.Field)); err != nil {
					return err
				}
			}
		}
	}
	for _, c := range report.Conversations {
		if err := write("Conversation %s checkpoint=%s\n", quote(c.ID), c.Checkpoint.State); err != nil {
			return err
		}
		for _, issue := range c.Issues {
			if err := write("  issue=%s ids=%s\n", issue.Code, quoteList(issue.IDs)); err != nil {
				return err
			}
		}
	}
	for _, a := range report.Archives {
		if err := write("Archive execution %s: %s rotation_pending=%t\n", quote(a.SessionID), a.State, a.RotationPending); err != nil {
			return err
		}
	}
	for _, a := range report.CheckpointAttempts {
		if err := write("Checkpoint request %s conversation %s: %s; %s\n", quote(a.RequestID), quote(a.ConversationID), a.FenceState, a.Acknowledgement); err != nil {
			return err
		}
	}
	for _, issue := range report.Issues {
		if err := write("Snapshot issue=%s\n", issue.Code); err != nil {
			return err
		}
		for _, ref := range issue.Sources {
			if err := write("  source: %s %s\n", quote(ref.Path), quote(ref.Record)); err != nil {
				return err
			}
		}
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
