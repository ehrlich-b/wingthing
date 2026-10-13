package eggclient

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/sessionrole"
	"golang.org/x/sys/unix"
)

// SessionCatalog returns content-free search results after authority filtering.
const catalogReadBudget = 32 << 20

func SessionCatalog(ctx context.Context, cfg *config.Config, wingID, label, query string, owns func(LocalSession) bool) ([]fleet.Session, bool, error) {
	if len(query) > 256 || len(strings.Fields(query)) > 8 {
		return nil, false, errors.New("query must be at most 256 bytes and 8 words")
	}
	entries, err := os.ReadDir(filepath.Join(cfg.Dir, "eggs"))
	if errors.Is(err, os.ErrNotExist) {
		return []fleet.Session{}, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	rows := []fleet.Session{}
	complete := true
	budget := int64(catalogReadBudget)
	words := strings.Fields(strings.ToLower(query))
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
		if view, e := tryLifecycleViewForSession(cfg, s); e == nil {
			projectSessionLifecycle(&s, view)
			importIncomplete = !view.SourceComplete
		} else {
			importIncomplete = true
		}
		row := fleet.Session{Identity: fleet.Identity{OriginWingID: wingID, SessionID: s.ID}, MachineLabel: label, Name: s.Name, Agent: s.Agent, CWD: s.CWD, Status: s.Status, Live: live, Pinned: s.Pinned, Role: s.Role, NeedsAttention: s.State == "needs_input", Routes: []fleet.Route{}}
		if s.Role == "coordinator" {
			row.Presence = sessionrole.ReadPresence(dir, live, s.State, s.StateSource, now)
		}
		found := make([]bool, len(words))
		fields := map[string]bool{}
		match := func(value, field string) {
			lower := strings.ToLower(value)
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
		if len(words) > 0 {
			row.SearchIncomplete = importIncomplete
			if importIncomplete {
				complete = false
			}
			for _, name := range []string{"lifecycle.jsonl", "chat.jsonl.gz"} {
				remaining := budget
				if remaining <= 0 {
					row.SearchIncomplete = true
					complete = false
					break
				}
				// Bound decompressed bytes as well as individual native records. Read only
				// wing-owned journals/archives, never arbitrary provider home directories.
				malformed := false
				read, full, e := searchSessionFile(ctx, dir, name, min(remaining, int64(8<<20)), func(record []byte) {
					var event egg.SessionEvent
					if name == "lifecycle.jsonl" {
						if json.Unmarshal(record, &event) == nil {
							match(event.Text, "transcript")
						} else {
							malformed = true
						}
					} else {
						var value map[string]any
						if json.Unmarshal(record, &value) == nil {
							match(transcriptText(value), "transcript")
						} else {
							malformed = true
						}
					}
				})
				budget -= read
				if e != nil || !full || malformed {
					row.SearchIncomplete = true
					complete = false
				}
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
func searchSessionFile(ctx context.Context, dir, name string, limit int64, consume func([]byte)) (int64, bool, error) {
	info, err := os.Lstat(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !info.Mode().IsRegular() {
		return 0, false, errors.New("transcript is not a regular file")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, false, err
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	actual, e := f.Stat()
	if e != nil || !actual.Mode().IsRegular() {
		return 0, false, errors.New("transcript is not a regular file")
	}
	var reader io.Reader = f
	if strings.HasSuffix(name, ".gz") {
		gz, e := gzip.NewReader(f)
		if e != nil {
			return 0, false, e
		}
		defer gz.Close()
		reader = gz
	}
	limited := &io.LimitedReader{R: reader, N: limit + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return limit + 1 - limited.N, false, err
		}
		consume(scanner.Bytes())
	}
	read := limit + 1 - limited.N
	return read, read <= limit && scanner.Err() == nil, scanner.Err()
}

// Only human/assistant text fields from archived provider records are searched;
// tool payloads, credentials and terminal escape bytes are not searchable text.
func transcriptText(v map[string]any) string {
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
				if text, ok := m["text"].(string); ok {
					b.WriteString(text)
					b.WriteByte('\n')
				}
			}
		}
	}
	return b.String()
}
