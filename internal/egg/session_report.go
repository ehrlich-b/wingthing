package egg

import (
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// ShellReport is an opt-in task signal scoped to an immutable shell session.
type ShellReport struct {
	SessionID string `json:"session_id"`
	Revision  int64  `json:"revision"`
	After     int64  `json:"after"`
	State     string `json:"state"`
}

// SessionReport describes intent and optional reported shell state, never native
// provider evidence or scheduling authority.
type SessionReport struct {
	Shell      *ShellReport     `json:"shell,omitempty"`
	Goal       string           `json:"goal"`
	Update     string           `json:"update"`
	NextAction string           `json:"next_action"`
	Presence   string           `json:"presence,omitempty"`
	LastWake   string           `json:"last_wake,omitempty"`
	NextWake   string           `json:"next_wake,omitempty"`
	Artifacts  []ReportArtifact `json:"artifacts,omitempty"`
	Links      []ReportLink     `json:"links,omitempty"`
}

type ReportArtifact struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

type ReportLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

func reportText(text string, max int) bool {
	return utf8.ValidString(text) && len(text) <= max && !strings.ContainsRune(text, 0) && strings.TrimSpace(text) != ""
}

func ValidateSessionReport(r SessionReport) error {
	if r.Shell != nil {
		if !validLifecycleID(r.Shell.SessionID) || r.Shell.Revision < 1 || r.Shell.After < 0 {
			return errors.New("shell report requires exact session_id, positive revision and nonnegative after")
		}
		switch r.Shell.State {
		case "working", "waiting", "continue", "finished":
		default:
			return errors.New("shell state must be working, waiting, continue or finished")
		}
	}
	if !reportText(r.Goal, 2048) || !reportText(r.Update, 4096) || !reportText(r.NextAction, 2048) {
		return errors.New("goal, update and next_action must be nonempty UTF-8 text, at most 2048, 4096 and 2048 bytes respectively")
	}
	if r.Presence != "" && r.Presence != "awake" && r.Presence != "asleep" {
		return errors.New("presence must be awake or asleep")
	}
	for _, stamp := range []string{r.LastWake, r.NextWake} {
		if stamp != "" {
			if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
				return errors.New("wake times must use RFC3339")
			}
		}
	}
	if r.LastWake != "" && r.NextWake != "" {
		last, _ := time.Parse(time.RFC3339Nano, r.LastWake)
		next, _ := time.Parse(time.RFC3339Nano, r.NextWake)
		if !next.After(last) {
			return errors.New("next_wake must follow last_wake")
		}
	}
	if len(r.Artifacts) > 8 || len(r.Links) > 8 {
		return errors.New("at most 8 artifacts and 8 links per report")
	}
	for _, artifact := range r.Artifacts {
		if !reportText(artifact.Label, 128) || !reportText(artifact.Path, 1024) {
			return errors.New("artifact requires a label (128 bytes) and path (1024 bytes)")
		}
	}
	for _, link := range r.Links {
		u, err := url.Parse(link.URL)
		if !reportText(link.Label, 128) || !reportText(link.URL, 2048) || err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("link requires a label (128 bytes) and an HTTP(S) URL without credentials (2048 bytes)")
		}
	}
	data, _ := json.Marshal(r)
	if len(data) > 16*1024 {
		return errors.New("encoded report exceeds 16 KiB")
	}
	return nil
}

// Reports share cursor replay and durability with native session activity.
// Optional shell signals are explicitly reported evidence, never native hooks.
func RecordSessionReport(dir string, report SessionReport) (SessionEvent, error) {
	if err := ValidateSessionReport(report); err != nil {
		return SessionEvent{}, err
	}
	j, err := openLifecycleJournal(dir)
	if err != nil {
		return SessionEvent{}, err
	}
	defer j.close()
	event := SessionEvent{Type: "session_report", Source: "wing_report", Report: &report}
	if shell := report.Shell; shell != nil {
		if shell.SessionID != filepath.Base(dir) {
			return SessionEvent{}, errors.New("shell report session does not match exact immutable session")
		}
		meta, err := j.root.ReadFile("egg.meta")
		if err != nil {
			return SessionEvent{}, err
		}
		agent := "missing"
		for _, line := range strings.Split(string(meta), "\n") {
			if strings.HasPrefix(line, "agent=") {
				agent = strings.TrimPrefix(line, "agent=")
			}
		}
		if agent != "" && agent != "shell" {
			return SessionEvent{}, errors.New("shell reports require a plain shell session")
		}
		var previous SessionEvent
		for _, old := range j.events {
			if old.Type == "session_exit" || old.Type == "session_failed" {
				return SessionEvent{}, errors.New("shell process has exited")
			}
			if old.Report != nil && old.Report.Shell != nil {
				previous = old
			}
		}
		if previous.Report != nil && reflect.DeepEqual(*previous.Report, report) {
			return previous, nil
		}
		revision := int64(0)
		state := ""
		if previous.Report != nil {
			revision = previous.Report.Shell.Revision
			state = previous.Report.Shell.State
		}
		if shell.Revision != revision+1 || shell.After != previous.Sequence {
			return SessionEvent{}, errors.New("stale shell revision or acknowledgement cursor")
		}
		if state == "finished" {
			return SessionEvent{}, errors.New("shell task already finished")
		}
		if (shell.State == "continue" && state != "waiting") || (state == "waiting" && shell.State != "continue" && shell.State != "finished") {
			return SessionEvent{}, errors.New("waiting requires an exact continue acknowledgement or finish")
		}
		if previous.Sequence == 0 && shell.State != "working" {
			return SessionEvent{}, errors.New("shell task must start working")
		}
		event.Source = "shell_report"
		event.State = map[string]string{"working": "working", "waiting": "needs_input", "continue": "working", "finished": "completed"}[shell.State]
	}
	if err := j.append(event); err != nil {
		return SessionEvent{}, err
	}
	return j.events[len(j.events)-1], nil
}
