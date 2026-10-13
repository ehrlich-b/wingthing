package sessionrole

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"github.com/ehrlich-b/wingthing/internal/fleet"
)

const presenceFile = "session.presence.json"

// RecordPresence is called by an explicitly authorized coordinator/waker. It
// records a schedule as data; it never installs or arms a timer.
func RecordPresence(dir string, p fleet.Presence, now time.Time) error {
	if p.State != "awake" && p.State != "asleep" {
		return errors.New("state must be awake or asleep")
	}
	for _, value := range []string{p.LastWake, p.NextWake, p.LastNote} {
		if value != "" {
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return errors.New("wake/note times must be RFC3339")
			}
		}
	}
	p.ObservedAt = now.UTC().Format(time.RFC3339Nano)
	p.Source = "coordinator_heartbeat"
	p.Stale = false
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return daemonctl.WriteAtomicMetadataFile(filepath.Join(dir, presenceFile), data, 0600)
}
func ReadPresence(dir string, live bool, state, source string, now time.Time) *fleet.Presence {
	p := &fleet.Presence{State: "unknown", Source: "missing", Stale: true}
	f, err := openRegular(filepath.Join(dir, presenceFile))
	if err == nil {
		data, e := io.ReadAll(io.LimitReader(f, 4097))
		_ = f.Close()
		if e == nil && len(data) <= 4096 && json.Unmarshal(data, p) == nil && (p.State == "awake" || p.State == "asleep") {
			at, e := time.Parse(time.RFC3339Nano, p.ObservedAt)
			p.Stale = e != nil || at.After(now) || now.Sub(at) > 2*time.Minute
			if p.Stale {
				p.State = "unknown"
			}
			p.Source = "coordinator_heartbeat"
		} else {
			p = &fleet.Presence{State: "unknown", Source: "invalid", Stale: true}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		p.Source = "unavailable"
	}
	if !live {
		p.State = "dead"
		p.Source = "egg_process"
		return p
	}
	if p.Source == "missing" && (source == "claude_hook" || source == "codex_hook") {
		if state == "working" || state == "starting" {
			p.State = "awake"
		} else if state == "idle" {
			p.State = "asleep"
		}
		p.Source = "native_lifecycle"
		p.ObservedAt = now.UTC().Format(time.RFC3339Nano)
		p.Stale = false
	}
	return p
}
