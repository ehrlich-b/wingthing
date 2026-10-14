package eggclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"github.com/ehrlich-b/wingthing/internal/sessionsearch"
)

// SessionCatalog returns content-free search results after authority filtering.
func SessionCatalog(ctx context.Context, cfg *config.Config, wingID, label, query string, owns func(LocalSession) bool) ([]fleet.Session, bool, error) {
	return sessionCatalog(ctx, cfg, wingID, label, query, owns, false)
}

// SessionSearchCatalog adds bounded excerpts after ownership filtering. The
// protocol caller must also require the existing session_read capability.
func SessionSearchCatalog(ctx context.Context, cfg *config.Config, wingID, label, query string, owns func(LocalSession) bool) ([]fleet.Session, bool, error) {
	return sessionCatalog(ctx, cfg, wingID, label, query, owns, true)
}
func sessionCatalog(ctx context.Context, cfg *config.Config, wingID, label, query string, owns func(LocalSession) bool, excerpts bool) ([]fleet.Session, bool, error) {
	if len(query) > 256 || len(strings.Fields(query)) > 8 {
		return nil, false, errors.New("query must be at most 256 bytes and 8 words")
	}
	eggs, err := os.Open(filepath.Join(cfg.Dir, "eggs"))
	if errors.Is(err, os.ErrNotExist) {
		return []fleet.Session{}, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	entries, err := eggs.ReadDir(1001)
	_ = eggs.Close()
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	directoryComplete := len(entries) <= 1000
	if !directoryComplete {
		entries = entries[:1000]
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	rows := []fleet.Session{}
	complete := directoryComplete
	statusBudget := int64(sessionsearch.ReadBudget)
	budget := int64(sessionsearch.ReadBudget)
	var index *sessionsearch.Index
	if strings.TrimSpace(query) != "" {
		index, err = sessionsearch.Open(cfg.Dir)
		if err != nil {
			complete = false
		} else {
			defer index.Close()
			if directoryComplete {
				allIDs := []string{}
				for _, entry := range entries {
					if entry.IsDir() {
						allIDs = append(allIDs, entry.Name())
					}
				}
				if err := index.Prune(ctx, allIDs); err != nil {
					complete = false
				}
			}
		}
	}
	words := strings.Fields(strings.ToLower(query))
	type candidate struct {
		row    fleet.Session
		found  []bool
		fields map[string]bool
	}
	candidates := []candidate{}
	ids := []string{}
	now := time.Now()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return rows, false, err
		}
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(cfg.Dir, "eggs", entry.Name())
		meta := ReadEggMetaValues(dir)
		if meta["agent"] == "" {
			if data, e := os.ReadFile(filepath.Join(dir, "chat.meta")); e == nil {
				for k, v := range egg.ParseChatMeta(string(data)) {
					meta[k] = v
				}
			}
		}
		pid, live := ReadAliveEggPID(dir)
		s := LocalSession{ID: entry.Name(), Name: ReadSessionName(dir), Principal: ReadSessionPrincipal(dir), Agent: meta["agent"], Kind: meta["kind"], CWD: meta["cwd"], PID: pid}
		// Ownership/path filtering precedes role, native import and content reads.
		if owns != nil && !owns(s) {
			continue
		}
		s.Metadata, err = sessionrole.Read(dir)
		if err != nil {
			return nil, false, err
		}
		s.Status = "unknown"
		importIncomplete := false
		// Lifecycle projection imports native tails through the existing owner
		// contract. Bound the journal it loads before invoking that importer.
		canProject := true
		if info, e := os.Lstat(filepath.Join(dir, "lifecycle.jsonl")); e == nil {
			canProject = info.Mode().IsRegular() && info.Size() <= int64(sessionsearch.SourceBudget) && info.Size() <= statusBudget

		} else if !errors.Is(e, os.ErrNotExist) {
			canProject = false
		}
		if canProject {
			if view, e := tryCatalogLifecycleView(ctx, cfg, s, &statusBudget); e == nil {
				projectSessionLifecycle(&s, view)
				importIncomplete = !view.SourceComplete
			} else {
				importIncomplete = true
			}
		} else {
			importIncomplete = true
		}

		row := fleet.Session{Identity: fleet.Identity{OriginWingID: wingID, SessionID: s.ID}, MachineLabel: label, Name: s.Name, Agent: s.Agent, CWD: s.CWD, Status: s.Status, Live: live, Pinned: s.Pinned, Role: s.Role, NeedsAttention: s.State == "needs_input", Routes: []fleet.Route{}}
		row.Agent = sessionsearch.RedactMetadata(row.Agent)
		row.Name = sessionsearch.RedactMetadata(row.Name)
		row.CWD = sessionsearch.RedactMetadata(row.CWD)
		row.MachineLabel = sessionsearch.RedactMetadata(row.MachineLabel)
		if s.Role == "coordinator" {
			row.Presence = sessionrole.ReadPresence(dir, live, s.State, s.StateSource, now, s.StateObservedAt)
		}
		found := make([]bool, len(words))
		fields := map[string]bool{}
		match := func(value, field string) {
			lower := strings.ToLower(sessionsearch.RedactMetadata(value))
			for i, w := range words {
				if strings.Contains(lower, w) {
					found[i] = true
					fields[field] = true
				}
			}
		}
		match(s.ID, "session_id")
		match(s.Name, "name")
		match(s.Agent, "agent")
		match(s.CWD, "repo")
		match(label, "machine")
		matchable := index != nil
		if len(words) > 0 {
			row.SearchIncomplete = importIncomplete
			if importIncomplete {
				complete = false
			}
			if index == nil {
				row.SearchIncomplete = true
			} else {
				for _, name := range []string{"lifecycle.jsonl", "chat.jsonl.gz"} {
					full, e := index.Refresh(ctx, s.ID, dir, name, &budget, func(record []byte) (string, bool) {
						if name == "lifecycle.jsonl" {
							var event egg.SessionEvent
							if json.Unmarshal(record, &event) != nil {
								return "", false
							}
							if event.Truncated {
								return "", false
							}
							if (event.Type == "message" || event.Type == "prompt_submitted") && (event.Role == "" || event.Role == "user" || event.Role == "assistant") {
								return event.Text, true
							}
							return "", true
						}
						var value map[string]any
						if json.Unmarshal(record, &value) != nil {
							return "", false
						}
						return transcriptText(value), true
					})
					if e != nil || !full {
						row.SearchIncomplete = true
						if e != nil {
							matchable = false
							_ = index.Invalidate(ctx, s.ID, name)
						}
					}
				}

			}
			if row.SearchIncomplete {
				complete = false
			}
		}
		candidates = append(candidates, candidate{row: row, found: found, fields: fields})
		if matchable {
			ids = append(ids, s.ID)
		}
	}
	matches := map[string][]bool{}
	matchError := false
	if index != nil {
		matches, err = index.MatchesMany(ctx, ids, words)
		if err != nil {
			complete = false
			matchError = true
		}
	}
	for _, candidate := range candidates {
		row, found, fields := candidate.row, candidate.found, candidate.fields
		if matchError {
			row.SearchIncomplete = true
		}
		for n, hit := range matches[row.SessionID] {
			if hit {
				found[n] = true
				fields["transcript"] = true
			}
		}

		all := true
		for _, v := range found {
			all = all && v
		}
		if !all {
			continue
		}
		for _, field := range []string{"session_id", "name", "agent", "repo", "machine", "transcript"} {
			if fields[field] {
				row.MatchFields = append(row.MatchFields, field)
			}
		}
		if excerpts && fields["transcript"] && index != nil {
			excerpt, err := index.Excerpt(ctx, row.SessionID, words)
			if err != nil {
				row.SearchIncomplete = true
				complete = false
			} else {
				row.MatchExcerpt = excerpt
			}
		}
		if len(rows) < 200 {
			rows = append(rows, row)
		} else {
			complete = false
			if row.Pinned && row.Role == "coordinator" {
				for i := len(rows) - 1; i >= 0; i-- {
					if !rows[i].Pinned || rows[i].Role != "coordinator" {
						rows[i] = row
						break
					}
				}
			}
		}
	}
	return rows, complete, nil
}

// Only human/assistant text fields from archived provider records are searched;
// tool payloads, credentials and terminal escape bytes are not searchable text.
func transcriptText(v map[string]any) string {
	typeName, _ := v["type"].(string)
	if typeName != "user" && typeName != "assistant" && typeName != "message" && typeName != "response_item" {
		return ""
	}
	if text, ok := v["text"].(string); ok {
		return text
	}
	if msg, ok := v["message"].(map[string]any); ok {
		return contentText(msg["content"])
	}
	if payload, ok := v["payload"].(map[string]any); ok {
		return contentText(payload["content"])
	}
	return ""
}
func contentText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var b strings.Builder
	if items, ok := v.([]any); ok {
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				if kind, _ := m["type"].(string); kind != "text" && kind != "input_text" && kind != "output_text" {
					continue
				}
				if text, ok := m["text"].(string); ok {
					b.WriteString(text)
					b.WriteByte('\n')
				}
			}
		}
	}
	return b.String()
}
