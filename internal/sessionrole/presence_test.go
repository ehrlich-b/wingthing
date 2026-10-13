package sessionrole

import (
	"github.com/ehrlich-b/wingthing/internal/fleet"
	"testing"
	"time"
)

func TestCoordinatorPresenceRecordedStaleDeadAndUnknown(t *testing.T) {
	dir := t.TempDir()
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
