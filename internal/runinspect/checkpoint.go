package runinspect

import (
	"encoding/json"
	"strings"
)

// CheckpointAttempt describes retained mailbox intent. A matching durable
// checkpoint covers its cursor, but does not prove which writer committed it.
type CheckpointAttempt struct {
	RequestID        string `json:"request_id"`
	ConversationID   string `json:"conversation_id"`
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	AfterCursor      *int64 `json:"after_cursor,omitempty"`
	FenceState       string `json:"fence_state"`
	Acknowledgement  string `json:"acknowledgement"`
	Source           Ref    `json:"source"`
}

func (report *Report) checkpoints(s *snapshot, r *records) error {
	for i := range report.Conversations {
		view := &report.Conversations[i]
		var row *conversationRow
		for j := range r.conversations {
			if r.conversations[j].id == view.ID {
				row = &r.conversations[j]
				break
			}
		}
		if row == nil {
			continue
		}
		cp := Checkpoint{State: "unknown", Revision: row.revision, AcknowledgedCursor: row.delivered, Content: output(&row.checkpoint, refField(row.source, "checkpoint"), 0)}
		if row.revision != nil && row.delivered != nil && *row.revision >= 0 && *row.delivered >= 0 && r.tables["conversation_events"] {
			var head, pending int64
			for _, event := range r.events {
				if event.root == row.root {
					if event.sequence > head {
						head = event.sequence
					}
					if event.sequence > *row.delivered {
						pending++
					}
				}
			}
			cp.EventHead = &head
			cp.PendingEvents = &pending
			switch {
			case *row.delivered > head:
				cp.State = "inconsistent_cursor"
			case row.checkpoint == "":
				cp.State = "absent"
			case pending > 0:
				cp.State = "unacknowledged_events"
			default:
				cp.State = "current_at_snapshot"
			}
		}
		for _, wake := range r.wakes {
			if wake.root == row.root {
				delivery := wake.delivery
				cp.WakeDeliveryCursor = &delivery
				view.Sources = append(view.Sources, wake.source)
			}
		}
		view.Checkpoint = cp
		if cp.State == "inconsistent_cursor" {
			view.Issues = append(view.Issues, Issue{Code: "checkpoint_cursor_past_evidence", IDs: []string{view.ID, view.RootID}, Sources: []Ref{row.source, {Path: "wt.db", Record: "conversation_events", Field: "sequence"}}})
		}
	}
	for _, path := range s.paths() {
		parts := strings.Split(path, "/")
		if len(parts) != 4 || parts[0] != "conversation-brokers" || parts[2] != "journal" || !strings.HasSuffix(parts[3], ".json") {
			continue
		}
		data, err := s.read(path)
		if err != nil {
			return err
		}
		var entry struct {
			Version int             `json:"version"`
			ID      string          `json:"request_id"`
			Tool    string          `json:"tool"`
			Phase   string          `json:"phase"`
			Outcome string          `json:"outcome"`
			Digest  string          `json:"payload_sha256"`
			Payload json.RawMessage `json:"payload"`
		}
		ref := Ref{Path: path}
		if json.Unmarshal(data, &entry) != nil || entry.Version != 1 {
			report.Issues = append(report.Issues, Issue{Code: "partial_or_unsupported_broker_journal", Sources: []Ref{ref}})
			continue
		}
		if entry.Tool != "conversation_checkpoint" {
			continue
		}
		var call struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					ConversationID   string  `json:"conversation_id"`
					ExpectedRevision *int64  `json:"expected_revision"`
					AfterCursor      *int64  `json:"after_cursor"`
					Checkpoint       *string `json:"checkpoint"`
				} `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(entry.Payload, &call) != nil || call.Method != "tools/call" || call.Params.Name != "conversation_checkpoint" || entry.Digest != digest(entry.Payload) || entry.ID+".json" != parts[3] {
			report.Issues = append(report.Issues, Issue{Code: "invalid_checkpoint_journal", Sources: []Ref{ref}})
			continue
		}
		a := call.Params.Arguments
		attempt := CheckpointAttempt{RequestID: entry.ID, ConversationID: a.ConversationID, ExpectedRevision: a.ExpectedRevision, AfterCursor: a.AfterCursor, FenceState: "unknown", Acknowledgement: "request_outcome_unconfirmed", Source: ref}
		for _, view := range report.Conversations {
			if view.ID != a.ConversationID {
				continue
			}
			if a.ExpectedRevision != nil && view.Checkpoint.Revision != nil {
				switch {
				case *a.ExpectedRevision < *view.Checkpoint.Revision:
					attempt.FenceState = "stale_writer"
				case *a.ExpectedRevision == *view.Checkpoint.Revision:
					attempt.FenceState = "matches_snapshot_revision"
				default:
					attempt.FenceState = "revision_ahead_of_snapshot"
				}
			}
			if a.AfterCursor != nil && view.Checkpoint.AcknowledgedCursor != nil && *a.AfterCursor <= *view.Checkpoint.AcknowledgedCursor {
				attempt.Acknowledgement = "cursor_covered_by_durable_checkpoint"
			}
		}
		// A journal response is an observation, not a substitute for DB ACK.
		if entry.Phase == "completed" && entry.Outcome == "not_dispatched" {
			attempt.Acknowledgement = "request_not_dispatched"
		}
		report.CheckpointAttempts = append(report.CheckpointAttempts, attempt)
	}
	return nil
}
