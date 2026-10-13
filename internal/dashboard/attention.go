package dashboard

import (
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
)

// A saved name can later identify a different host, executable or state tree.
// Snapshot the complete profile in rows so old views cannot acknowledge the
// replacement's results. Read cursors persist per OS viewer; bells live only in this instance.
type attentionIdentity struct {
	machine, session, localDir string
	profile                    config.Remote
	generation                 uint64
}

type attentionProfile struct {
	identity   attentionIdentity
	generation uint64
	active     bool
}

type attentionRecord struct {
	completion, seen, blocked, cursor int64
}

func (s *state) attentionIdentity(name, id string) attentionIdentity {
	identity := attentionIdentity{machine: name, session: id, generation: s.attentionProfiles[name].generation}
	if name == "local" {
		identity.localDir = s.localDir
	} else {
		identity.profile = s.remotes[name]
	}
	return identity
}

func nativeSource(source string) bool { return source == "claude_hook" || source == "codex_hook" }

// Called by reconciliation after inventory application. Failed queries retain
// their visual snapshot but cannot advance cursors or generate alerts.
func (s *state) observeAttention() {
	if s.attention == nil {
		s.attention = make(map[attentionIdentity]attentionRecord)
	}
	if s.attentionProfiles == nil {
		s.attentionProfiles = make(map[string]attentionProfile)
	}
	for name, profile := range s.attentionProfiles {
		if _, ok := s.machines[name]; !ok {
			profile.active = false
			s.attentionProfiles[name] = profile
		}
	}
	for _, name := range s.machineNames() {
		identity := s.attentionIdentity(name, "")
		identity.generation = 0
		profile := s.attentionProfiles[name]
		if !profile.active || profile.identity != identity {
			profile = attentionProfile{identity: identity, generation: profile.generation + 1, active: true}
			s.attentionProfiles[name] = profile
		}
		m := s.machines[name]
		if m.err != "" {
			continue
		}
		for _, session := range m.sessions {
			key := s.attentionIdentity(name, session.ID)
			record, known := s.attention[key]
			if session.StateCursor < record.cursor {
				continue
			}
			qualifying := false
			message := ""
			if nativeSource(session.StateSource) && session.StateCursor > 0 {
				if session.State == "needs_input" && session.StateCursor > record.blocked {
					record.blocked = session.StateCursor
					qualifying, message = true, "Native provider requested input."
				}
				if session.CompletionCursor > record.completion && session.CompletionCursor <= session.StateCursor {
					record.completion = session.CompletionCursor
					qualifying, message = true, "New foreground response; provider or descendants may continue."
				}
			}
			record.cursor = max(record.cursor, session.StateCursor)
			if !known {
				// Baseline historical completions; known input requests remain
				// visibly blocked without a startup storm of audible alerts.
				record.seen = record.completion
				record.seen = s.baselineReadCursor(key, record.completion)
			}
			attached := s.attached != nil && *s.attached == key
			if attached {
				record.seen = record.completion
			}
			s.attention[key] = record
			if known && qualifying && !attached {
				if s.bell {
					s.pendingBells++
				}
				if !s.busy {
					s.message = message
				}
			}
		}
	}
}

func (s *state) attentionFor(r row) string {
	// Older compatible inventories omit the additive lifecycle fields. Their
	// blocked/failed status remains useful visual evidence, but supplies no
	// cursor for a bell or a completion inference.
	if r.StateSource == "" && r.State == "" && (r.Status == "blocked" || r.Status == "failed") {
		return r.Status
	}
	if !nativeSource(r.StateSource) || r.StateCursor <= 0 || r.State == "unknown" || r.State == "" {
		if r.StateSource == "egg_process" && (r.State == "failed" || r.State == "stopped") {
			return "failed"
		}
		return "unknown"
	}
	switch r.State {
	case "needs_input":
		return "blocked"
	case "failed", "stopped":
		return "failed"
	}
	record := s.attention[r.identity]
	if record.completion > record.seen {
		return "unread"
	}
	if r.State == "working" || r.State == "starting" {
		return "active"
	}
	return "none"
}

func (s *state) acknowledge(r row) {
	s.selectionTouched = true
	record, ok := s.attention[r.identity]
	if ok {
		// The captured view acknowledges exactly what it showed, even if a
		// newer completion arrived before this key/action was processed.
		record.seen = max(record.seen, min(r.completion, record.completion))
		s.attention[r.identity] = record
		s.saveReadCursor(r.identity, record.seen)
	}
}

func (s *state) takeBells() string {
	bells := strings.Repeat("\a", s.pendingBells)
	s.pendingBells = 0
	return bells
}

func (s *state) bellSetting() string {
	if s.bell {
		return "on"
	}
	return "off"
}
