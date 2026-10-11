package runinspect

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"sort"
	"strings"
)

const maxArchiveDecodedBytes = 32 << 20

func metadata(data []byte) (map[string]string, bool) {
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, false
		}
		if _, exists := values[key]; exists {
			return nil, false
		}
		values[key] = value
	}
	return values, true
}

func (report *Report) archives(s *snapshot, r *records) error {
	sessions := map[string]bool{}
	for _, path := range s.paths() {
		parts := strings.Split(path, "/")
		if len(parts) == 3 && parts[0] == "eggs" {
			sessions[parts[1]] = true
		}
	}
	for _, run := range report.Runs {
		if run.SessionID != "" {
			sessions[run.SessionID] = true
		}
	}
	order := make([]string, 0, len(sessions))
	for session := range sessions {
		order = append(order, session)
	}
	sort.Strings(order)
	for _, session := range order {
		prefix := "eggs/" + session + "/"
		metaPath, chatPath := prefix+"chat.meta", prefix+"chat.jsonl.gz"
		archive := Archive{SessionID: session, State: "absent", Sources: []Ref{{Path: metaPath}, {Path: chatPath}}}
		_, hasMeta := s.files[metaPath]
		chat, hasChat := s.files[chatPath]
		archive.CompressedBytes = chat.Bytes
		for _, path := range s.paths() {
			if strings.HasPrefix(path, prefix+".chat-") && strings.HasSuffix(path, ".tmp") {
				archive.RotationPending = true
				archive.Sources = append(archive.Sources, Ref{Path: path})
			}
		}
		if hasMeta != hasChat {
			archive.State = "interrupted_rotation"
		}
		if hasMeta && hasChat {
			data, err := s.read(metaPath)
			if err != nil {
				return err
			}
			meta, ok := metadata(data)
			archive.Agent, archive.ProviderSessionID = meta["agent"], meta["agent_session_id"]
			if !ok || meta["format"] != "jsonl" || archive.ProviderSessionID == "" {
				archive.State = "invalid_metadata"
			} else {
				compressed, err := s.read(chatPath)
				if err != nil {
					return err
				}
				reader, err := gzip.NewReader(bytes.NewReader(compressed))
				if err != nil {
					archive.State = "incomplete_archive"
				} else {
					decoded, readErr := io.ReadAll(io.LimitReader(reader, maxArchiveDecodedBytes+1))
					_ = reader.Close()
					archive.DecodedBytes = int64(len(decoded))
					switch {
					case len(decoded) > maxArchiveDecodedBytes:
						archive.State = "decoded_limit_exceeded"
					case readErr != nil:
						archive.State = "incomplete_archive"
					default:
						archive.State = archiveIdentity(decoded, archive.Agent, archive.ProviderSessionID)
					}
				}
			}
			if archive.State == "complete_bound_archive" {
				for _, e := range r.eggs {
					if e.valid && e.Result.SessionID == session && e.Result.ProviderSessionID != "" && e.Result.ProviderSessionID != archive.ProviderSessionID {
						archive.State = "receipt_identity_conflict"
					}
				}
				for _, w := range r.wings {
					if w.valid && w.session == session && ((w.record.Result.ProviderSessionID != "" && w.record.Result.ProviderSessionID != archive.ProviderSessionID) || (w.record.Agent != "" && w.record.Agent != archive.Agent) || (w.record.CWD != "" && w.record.CWD != meta["cwd"])) {
						archive.State = "receipt_identity_conflict"
					}
				}
			}
		}
		// An incomplete temporary capture does not invalidate the last committed
		// archive, but it prevents an offline resume recommendation.
		if archive.RotationPending && archive.State == "absent" {
			archive.State = "interrupted_rotation"
		}
		report.Archives = append(report.Archives, archive)
	}
	return nil
}

func archiveIdentity(data []byte, agent, id string) string {
	found := false
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record struct {
			SessionID string `json:"sessionId"`
			Type      string `json:"type"`
			Payload   struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			return "partial_transcript"
		}
		observed := ""
		switch agent {
		case "claude":
			observed = record.SessionID
		case "codex":
			if record.Type == "session_meta" {
				observed = record.Payload.ID
			}
		default:
			return "unsupported_archive_schema"
		}
		if observed != "" {
			if observed != id {
				return "archive_identity_conflict"
			}
			found = true
		}
	}
	if !found {
		return "archive_identity_unverified"
	}
	return "complete_bound_archive"
}

func (report *Report) recommendations() {
	for i := range report.Runs {
		run := &report.Runs[i]
		if run.EvidenceState != "consistent" || run.SessionID != run.CurrentSessionID || run.ConversationID == "" {
			continue
		}
		if run.Diagnosis != "failed" && run.Diagnosis != "timeout" && run.Diagnosis != "stopped" {
			continue
		}
		var cp *Conversation
		var archive *Archive
		for j := range report.Conversations {
			if report.Conversations[j].ID == run.ConversationID {
				cp = &report.Conversations[j]
			}
		}
		for j := range report.Archives {
			if report.Archives[j].SessionID == run.SessionID {
				archive = &report.Archives[j]
			}
		}
		if cp == nil || archive == nil || len(cp.Issues) > 0 || cp.Checkpoint.State != "current_at_snapshot" || archive.State != "complete_bound_archive" || archive.RotationPending {
			continue
		}
		basis := append([]Ref{}, run.Sources...)
		basis = append(basis, cp.Sources...)
		basis = append(basis, archive.Sources...)
		run.Actions = append(run.Actions, Action{Kind: "manual_resume_eligible", Basis: basis, Requires: append([]string{}, report.RequiredHostObservations...)})
	}
}
