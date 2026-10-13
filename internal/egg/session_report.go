package egg

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// SessionReport describes intent, not native task state or scheduling authority.
type SessionReport struct {
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

// Reports share cursor replay and durability with native session activity, but
// have no State and cannot change readiness, permissions or wake delivery.
func RecordSessionReport(dir string, report SessionReport) (SessionEvent, error) {
	if err := ValidateSessionReport(report); err != nil {
		return SessionEvent{}, err
	}
	j, err := openLifecycleJournal(dir)
	if err != nil {
		return SessionEvent{}, err
	}
	defer j.close()
	if err := j.append(SessionEvent{Type: "session_report", Source: "wing_report", Report: &report}); err != nil {
		return SessionEvent{}, err
	}
	return j.events[len(j.events)-1], nil
}
