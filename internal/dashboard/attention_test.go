package dashboard

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func attentionSession(state string, cursor, completion int64) eggclient.LocalSession {
	status := state
	if state == "needs_input" {
		status = "blocked"
	}
	if state == "completed" {
		status = "idle"
	}
	r := session("same", "agent", "/work", status)
	r.State, r.StateCursor, r.CompletionCursor = state, cursor, completion
	return r
}

func attentionApply(s *state, machine string, r eggclient.LocalSession) {
	newInventory(nil).apply(s, inventoryResult{name: machine, sessions: []eggclient.LocalSession{r}, updated: time.Now()})
}

func TestAttentionNativeTransitionsAndExactAcknowledgement(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {}}}
	attentionApply(s, "local", attentionSession("completed", 7, 7))
	if s.bell || s.rows()[0].attention != "none" || s.takeBells() != "" {
		t.Fatal("historical completion alerted on startup")
	}
	drive(s, "b")
	attentionApply(s, "local", attentionSession("working", 8, 0))
	attentionApply(s, "local", attentionSession("needs_input", 9, 0))
	if s.rows()[0].attention != "blocked" || s.takeBells() != "\a" {
		t.Fatal("native input request did not alert")
	}
	drive(s, "i")
	if s.rows()[0].attention != "blocked" {
		t.Fatal("acknowledgement cleared native blocking")
	}
	drive(s, "escape")
	attentionApply(s, "local", attentionSession("needs_input", 9, 0))
	if s.takeBells() != "" {
		t.Fatal("repeated input cursor rang twice")
	}
	attentionApply(s, "local", attentionSession("completed", 10, 10))
	if s.rows()[0].Status != "idle" || s.rows()[0].attention != "unread" || s.takeBells() != "\a" {
		t.Fatal("foreground Stop did not stay idle plus unread")
	}
	shown := s.rows()[0]
	attentionApply(s, "local", attentionSession("completed", 11, 11))
	s.acknowledge(shown)
	if s.rows()[0].attention != "unread" {
		t.Fatal("old view acknowledged a newer completion")
	}
	s.acknowledge(s.rows()[0])
	if s.rows()[0].attention != "none" {
		t.Fatal("exact current completion was not acknowledged")
	}
	s.takeBells()
	attentionApply(s, "local", attentionSession("completed", 11, 11))
	if s.takeBells() != "" || s.rows()[0].attention != "none" {
		t.Fatal("polling recreated an acknowledged result")
	}
}

func TestAttentionMachineProfilesStaleReconnectAndAttachment(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {}, "devbox": {}}, remotes: map[string]config.Remote{"devbox": {SSHTarget: "first", WingthingDir: "/first"}}, bell: true}
	for _, name := range []string{"local", "devbox"} {
		attentionApply(s, name, attentionSession("working", 1, 0))
	}
	for _, name := range []string{"local", "devbox"} {
		attentionApply(s, name, attentionSession("completed", 2, 2))
	}
	if s.takeBells() != "\a\a" {
		t.Fatal("identical session IDs collided across machines")
	}
	local, devbox := s.rows()[0], s.rows()[1]
	s.acknowledge(local)
	if s.rows()[0].attention != "none" || s.rows()[1].attention != "unread" {
		t.Fatal("local acknowledgement cleared remote result")
	}
	newInventory(nil).apply(s, inventoryResult{name: "devbox", err: errors.New("fixture offline")})
	if s.takeBells() != "" || s.rows()[1].attention != "unread" {
		t.Fatal("stale machine altered attention")
	}
	attentionApply(s, "devbox", attentionSession("completed", 2, 2))
	if s.takeBells() != "" {
		t.Fatal("reconnect duplicated the alert")
	}
	s.remotes["devbox"] = config.Remote{SSHTarget: "second", WingthingDir: "/second"}
	attentionApply(s, "devbox", attentionSession("completed", 2, 2))
	attentionApply(s, "devbox", attentionSession("completed", 3, 3))
	s.acknowledge(devbox)
	if s.rows()[1].attention != "unread" {
		t.Fatal("old profile acknowledged new machine")
	}
	s.takeBells()
	key := s.rows()[1].identity
	s.attached = &key
	attentionApply(s, "devbox", attentionSession("completed", 4, 4))
	if s.takeBells() != "" || s.rows()[1].attention != "none" {
		t.Fatal("attached target rang or retained viewed completion")
	}
	attentionApply(s, "local", attentionSession("completed", 3, 3))
	if s.takeBells() != "\a" {
		t.Fatal("attachment suppressed another machine")
	}
	s.attached = nil
	attentionApply(s, "devbox", attentionSession("needs_input", 5, 0))
	s.acknowledge(s.rows()[1])
	if s.rows()[1].attention != "blocked" {
		t.Fatal("attachment/acknowledgement hid input request")
	}
}

func TestAttentionUnknownIdleAndBellToggle(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {}}}
	attentionApply(s, "local", attentionSession("working", 1, 0))
	for _, r := range []eggclient.LocalSession{attentionSession("unknown", 2, 0), {ID: "same", Status: "unknown"}, attentionSession("idle", 3, 0), attentionSession("completed", 4, 0)} {
		attentionApply(s, "local", r)
		if needsAttention(s.rows()[0].attention) || s.takeBells() != "" {
			t.Fatal("missing/idle/SessionEnd evidence inferred a result")
		}
	}
	attentionApply(s, "local", attentionSession("completed", 5, 5))
	if s.rows()[0].attention != "unread" || s.takeBells() != "" {
		t.Fatal("visual attention depended on bell support")
	}
	drive(s, "b")
	attentionApply(s, "local", attentionSession("completed", 5, 5))
	if s.takeBells() != "" {
		t.Fatal("enabling bell replayed an old completion")
	}
	drive(s, "!")
	if len(s.rows()) != 1 {
		t.Fatal("attention filter omitted unread result")
	}
	attentionApply(s, "local", attentionSession("unknown", 6, 0))
	if len(s.rows()) != 0 || strings.Contains(s.message, "PRIVATE") {
		t.Fatal("unknown became evidence of completion")
	}
	drive(s, "!")
	attentionApply(s, "local", attentionSession("working", 7, 0))
	if s.rows()[0].attention != "unread" {
		t.Fatal("unknown polling erased a trusted unread response")
	}
}

func TestAttentionProfileGenerationDoesNotReuseSeenState(t *testing.T) {
	first := config.Remote{SSHTarget: "first", WingthingDir: "/first"}
	s := &state{machines: map[string]machine{"devbox": {}}, remotes: map[string]config.Remote{"devbox": first}}
	attentionApply(s, "devbox", attentionSession("working", 1, 0))
	attentionApply(s, "devbox", attentionSession("completed", 2, 2))
	old := s.rows()[0]
	s.remotes["devbox"] = config.Remote{SSHTarget: "second", WingthingDir: "/second"}
	attentionApply(s, "devbox", attentionSession("working", 1, 0))
	s.remotes["devbox"] = first
	attentionApply(s, "devbox", attentionSession("working", 1, 0))
	attentionApply(s, "devbox", attentionSession("completed", 2, 2))
	current := s.rows()[0]
	if current.identity == old.identity || current.attention != "unread" {
		t.Fatal("recreated profile inherited old seen state")
	}
	s.acknowledge(old)
	if s.rows()[0].attention != "unread" {
		t.Fatal("previous generation acknowledged the replacement")
	}
	delete(s.machines, "devbox")
	s.reconcile()
	s.machines["devbox"] = machine{}
	attentionApply(s, "devbox", attentionSession("completed", 2, 2))
	if s.rows()[0].attention != "none" || s.rows()[0].identity == current.identity {
		t.Fatal("removed/readded profile did not establish a fresh baseline")
	}
}

func TestAttentionOptionalMetadataPreservesLegacyBlocked(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {}}, bell: true}
	attentionApply(s, "local", eggclient.LocalSession{ID: "same", Status: "blocked"})
	if s.rows()[0].attention != "blocked" || s.takeBells() != "" {
		t.Fatal("optional metadata hid legacy blocked or invented a cursor")
	}
	attentionApply(s, "local", eggclient.LocalSession{ID: "same", Status: "unknown"})
	if s.rows()[0].attention != "unknown" {
		t.Fatal("legacy unknown became idle/done")
	}
	attentionApply(s, "local", eggclient.LocalSession{ID: "same", Status: "done"})
	if needsAttention(s.rows()[0].attention) || s.takeBells() != "" {
		t.Fatal("legacy status inferred a native completion")
	}
}
