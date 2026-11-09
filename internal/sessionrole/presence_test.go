package sessionrole

import (
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"github.com/ehrlich-b/wingthing/internal/testsocket"
	"testing"
	"time"
)

func TestCoordinatorPresenceRecordedStaleDeadAndUnknown(t *testing.T) {
	dir := testsocket.Dir(t)
	now := time.Now().UTC()
	if p := ReadPresence(dir, true, "", "", now); p.State != "unknown" || !p.Stale {
		t.Fatalf("invented heartbeat: %+v", p)
	}
	value := fleet.Presence{State: "asleep", LastWake: now.Add(-time.Minute).Format(time.RFC3339Nano), NextWake: now.Add(time.Hour).Format(time.RFC3339Nano)}
	if err := RecordPresence(dir, value, now); err != nil {
		t.Fatal(err)
	}
	p := ReadPresence(dir, true, "", "", now)
	if p.State != "asleep" || p.Stale || p.NextWake != value.NextWake {
		t.Fatalf("heartbeat: %+v", p)
	}
	p = ReadPresence(dir, true, "", "", now.Add(3*time.Minute))
	if p.State != "unknown" || !p.Stale || p.LastWake != value.LastWake {
		t.Fatalf("stale times lost: %+v", p)
	}
	p = ReadPresence(dir, false, "", "", now)
	if p.State != "dead" || p.NextWake != value.NextWake {
		t.Fatalf("dead: %+v", p)
	}
	value.NextWake = "bad"
	if err := RecordPresence(dir, value, now); err == nil {
		t.Fatal("bad date accepted")
	}
}

func TestCoordinatorPresenceNativeEvidenceDoesNotRefreshOnRead(t *testing.T) {
	dir := testsocket.Dir(t)
	now := time.Now().UTC()
	old := now.Add(-time.Hour).Format(time.RFC3339Nano)
	for _, readAt := range []time.Time{now, now.Add(time.Minute)} {
		p := ReadPresence(dir, true, "working", "codex_hook", readAt, old)
		if p.State != "unknown" || !p.Stale || p.ObservedAt != old || p.Source != "native_lifecycle" {
			t.Fatalf("old native hook refreshed: %+v", p)
		}
	}
	for _, timestamp := range []string{"", "invalid", now.Add(time.Minute).Format(time.RFC3339Nano)} {
		p := ReadPresence(dir, true, "idle", "claude_hook", now, timestamp)
		if p.State != "unknown" || !p.Stale {
			t.Fatalf("unproven native age became asleep: %+v", p)
		}
	}
	for _, state := range []string{"working", "starting", "idle"} {
		at := now.Add(-time.Second).Format(time.RFC3339Nano)
		p := ReadPresence(dir, true, state, "claude_hook", now, at)
		want := "awake"
		if state == "idle" {
			want = "asleep"
		}
		if p.State != want || p.Stale || p.ObservedAt != at {
			t.Fatalf("fresh native hook lost: %+v", p)
		}
	}
}
