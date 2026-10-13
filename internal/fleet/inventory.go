// Package fleet defines the qualified inventory exchanged by personal wings.
package fleet

import (
	"errors"
	"sort"
	"time"
)

const Version = "wingthing.session-inventory.v1"

type Identity struct {
	OriginWingID string `json:"origin_wing_id"`
	SessionID    string `json:"session_id"`
}
type Route struct {
	WingID       string `json:"wing_id"` // paired endpoint, not execution origin
	MachineLabel string `json:"machine_label"`
	Kind         string `json:"kind"` // direct or via
	Remote       string `json:"remote,omitempty"`
	Healthy      bool   `json:"healthy"`
	ObservedAt   string `json:"observed_at"`
	Error        string `json:"error,omitempty"`
}
type Presence struct {
	State      string `json:"state"` // awake, asleep, dead, unknown
	ObservedAt string `json:"observed_at,omitempty"`
	LastWake   string `json:"last_wake,omitempty"`
	NextWake   string `json:"next_wake,omitempty"`
	LastNote   string `json:"last_note,omitempty"`
	Source     string `json:"source"`
	Stale      bool   `json:"stale"`
}
type Session struct {
	Identity
	MachineLabel      string    `json:"machine_label"`
	Name              string    `json:"name,omitempty"`
	Agent             string    `json:"agent,omitempty"`
	CWD               string    `json:"cwd,omitempty"`
	Status            string    `json:"status"`
	Live              bool      `json:"live"`
	Pinned            bool      `json:"pinned,omitempty"`
	Role              string    `json:"role,omitempty"`
	NeedsAttention    bool      `json:"needs_attention,omitempty"`
	Presence          *Presence `json:"presence,omitempty"`
	Routes            []Route   `json:"routes"`
	PossibleDuplicate bool      `json:"possible_duplicate,omitempty"`
	MatchFields       []string  `json:"match_fields,omitempty"`
	SearchIncomplete  bool      `json:"search_incomplete,omitempty"`
}
type Source struct {
	WingID       string `json:"wing_id,omitempty"`
	MachineLabel string `json:"machine_label"`
	Healthy      bool   `json:"healthy"`
	ObservedAt   string `json:"observed_at"`
	Error        string `json:"error,omitempty"`
}
type Inventory struct {
	Version      string    `json:"version"`
	WingID       string    `json:"wing_id"`
	MachineLabel string    `json:"machine_label"`
	GeneratedAt  string    `json:"generated_at"`
	Sessions     []Session `json:"sessions"`
	Sources      []Source  `json:"sources"`
	Complete     bool      `json:"complete"`
	Query        string    `json:"query,omitempty"`
}

// Merge receives inventories only from separately authenticated paired wings.
// Names, short IDs, paths and machine labels never establish origin equality.
func Merge(inventories []Inventory) Inventory {
	out := Inventory{Version: Version, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Sessions: []Session{}, Sources: []Source{}, Complete: true}
	index := map[Identity]int{}
	for _, inv := range inventories {
		out.Sources = append(out.Sources, inv.Sources...)
		out.Complete = out.Complete && inv.Complete
		for _, row := range inv.Sessions {
			row.Routes = append([]Route(nil), row.Routes...)
			if row.OriginWingID == "" {
				row.PossibleDuplicate = true
				out.Sessions = append(out.Sessions, row)
				continue
			}
			if i, ok := index[row.Identity]; ok {
				prior := out.Sessions[i]
				if better(row.Routes, prior.Routes) {
					out.Sessions[i] = row
				}
				merged := mergeRoutes(prior.Routes, row.Routes)
				out.Sessions[i].Routes = merged
				out.Sessions[i].SearchIncomplete = prior.SearchIncomplete || row.SearchIncomplete
			} else {
				index[row.Identity] = len(out.Sessions)
				out.Sessions = append(out.Sessions, row)
			}
		}
	}
	for i := range out.Sessions {
		a := &out.Sessions[i]
		for j := range out.Sessions {
			b := out.Sessions[j]
			if i != j && a.SessionID == b.SessionID && (a.OriginWingID == "" || b.OriginWingID == "") {
				a.PossibleDuplicate = true
			}
		}
	}
	sort.SliceStable(out.Sessions, func(i, j int) bool {
		a, b := out.Sessions[i], out.Sessions[j]
		return a.Pinned && a.Role == "coordinator" && !(b.Pinned && b.Role == "coordinator")
	})
	return out
}
func routeRank(r Route) int {
	if !r.Healthy {
		return 2
	}
	if r.Kind == "direct" {
		return 0
	}
	return 1
}
func bestRank(routes []Route) int {
	n := 3
	for _, r := range routes {
		n = min(n, routeRank(r))
	}
	return n
}
func newest(routes []Route) time.Time {
	var latest time.Time
	for _, route := range routes {
		at, e := time.Parse(time.RFC3339Nano, route.ObservedAt)
		if e == nil && at.After(latest) {
			latest = at
		}
	}
	return latest
}
func better(a, b []Route) bool {
	if bestRank(a) != bestRank(b) {
		return bestRank(a) < bestRank(b)
	}
	return newest(a).After(newest(b))
}
func mergeRoutes(a, b []Route) []Route {
	out := append([]Route(nil), a...)
	for _, r := range b {
		found := false
		for i, p := range out {
			if p.WingID == r.WingID && p.Remote == r.Remote && p.Kind == r.Kind {
				newAt, newErr := time.Parse(time.RFC3339Nano, r.ObservedAt)
				oldAt, oldErr := time.Parse(time.RFC3339Nano, p.ObservedAt)
				if newErr == nil && (oldErr != nil || !newAt.Before(oldAt)) {
					out[i] = r
				}
				found = true
				break
			}
		}
		if !found {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return routeRank(out[i]) < routeRank(out[j]) })
	return out
}

// SelectRoute is pre-dispatch selection. After sending input, transport failure
// has unknown outcome and must not cause a second mutation on another route.
func SelectRoute(row Session, now time.Time) (Route, error) {
	if row.OriginWingID == "" || row.SessionID == "" {
		return Route{}, errors.New("origin identity is unknown; pair and refresh before acting")
	}
	candidates := append([]Route(nil), row.Routes...)
	sort.SliceStable(candidates, func(i, j int) bool { return routeRank(candidates[i]) < routeRank(candidates[j]) })
	for _, r := range candidates {
		t, err := time.Parse(time.RFC3339Nano, r.ObservedAt)
		if r.WingID != "" && (r.Kind == "direct" || r.Kind == "via") && (r.Kind != "direct" || r.WingID == row.OriginWingID) && r.Healthy && err == nil && !t.After(now) && now.Sub(t) <= 30*time.Second {
			return r, nil
		}
	}
	return Route{}, errors.New("no fresh healthy route; refresh before acting")
}
func Resolve(rows []Session, id Identity) (Session, error) {
	if id.OriginWingID == "" || id.SessionID == "" {
		return Session{}, errors.New("origin_wing_id and full session_id are required")
	}
	var result Session
	count := 0
	for _, r := range rows {
		if r.Identity == id {
			result = r
			count++
		}
	}
	if count != 1 {
		return Session{}, errors.New("exact session identity is missing or ambiguous")
	}
	return result, nil
}
