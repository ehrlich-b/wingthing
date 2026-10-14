package dashboard

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	"github.com/ehrlich-b/wingthing/internal/onboarding"
	"github.com/ehrlich-b/wingthing/internal/permissions"
)

type machine struct {
	generation string
	sessions   []eggclient.LocalSession
	updated    time.Time
	attempted  time.Time
	err        string
	loading    bool
}

type row struct {
	eggclient.LocalSession
	machine    string
	generation string
	identity   attentionIdentity
	attention  string
	completion int64
}

func (r row) key() string { return r.machine + ":" + r.ID }

func (r row) name() string {
	if r.Pinned && r.Role == "coordinator" {
		return "wing"
	}
	if r.Name == "" || strings.Contains(r.Name, "/") || r.Name == r.ID {
		return "Untitled session"
	}
	// Names are stored as slugs; titles read as words.
	return clean(strings.ReplaceAll(r.Name, "-", " "))
}

// A remote cwd is presentation data, never resolved on this machine.
func (r row) project() string { return projectName(r.CWD) }

type mode int

const (
	navigate mode = iota
	filter
	rename
	newAgent
	newCWD
	confirmStop
	confirmTakeover
	machinePicker
	details
	help
	runFilter
	runDetails
	runResult
	runAttach
	agentSetup
	installConsent
	providerReady
	setupFailure
	setupInvoke
	machineDetails
	diagnostics
	diagnosticInput
	diagnosticReport
	actionMenu
	permissionReview
)

type action struct {
	permission *permissions.Response
	run        runRow
	kind       mode
	row        row
	agent      string
	cwd        string
	name       string
	attach     bool
	readOnly   bool
	takeover   bool
	setupStage string
	args       []string
}

type state struct {
	permissionNotice                   string
	permissionAnswered                 string
	permissionAnswers                  map[string]bool
	permissionSelected                 map[string]string
	permissionRetained                 map[string]permissions.Request
	previews                           map[attentionIdentity]sessionPreview
	previewReader                      func(context.Context, row) (sessionPreview, error)
	theme                              terminalTheme
	rowOrder                           []string
	previousSelected                   string
	draftAgent, draftCWD, draftMachine string
	persistAttention                   bool
	readSeen                           map[string]int64
	agents                             []onboarding.Agent
	setupAgent                         onboarding.Agent
	setupStage                         string
	setupCompleted                     string
	setupError                         string
	recent                             bool
	runs                               *runHistory
	machines                           map[string]machine
	registryError                      string
	registrySequence                   uint64
	selectionTouched                   bool
	selected                           string
	filter                             string
	mode                               mode
	input                              string
	previous                           string
	target                             row
	agent                              string
	defaultAgent                       string
	defaultCWD                         string
	message                            string
	busy                               bool
	machineFilter                      string
	attentionOnly                      bool
	picker                             int
	overlayOffset                      int
	overlayLimit                       int
	refresh                            bool
	localDir                           string
	remotes                            map[string]config.Remote
	attention                          map[attentionIdentity]attentionRecord
	attentionProfiles                  map[string]attentionProfile
	attached                           *attentionIdentity
	bell                               bool
	pendingBells                       int
	diagnostic                         diagnosticState
}

func needsAttention(attention string) bool {
	return attention == "blocked" || attention == "unread" || attention == "failed"
}

func (s *state) sessionRow(name string, session eggclient.LocalSession) row {
	r := row{LocalSession: session, machine: name, generation: s.machines[name].generation, identity: s.attentionIdentity(name, session.ID)}
	s.selectPermission(&r)
	r.completion = s.attention[r.identity].completion
	r.attention = s.attentionFor(r)
	return r
}

func (s *state) machineNames() []string {
	names := make([]string, 0, len(s.machines))
	for name := range s.machines {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == "local" || names[j] == "local" {
			return names[i] == "local"
		}
		return names[i] < names[j]
	})
	return names
}

func (s *state) rows() []row {
	var rows []row
	query := strings.ToLower(s.filter)
	order := make(map[string]int)
	for i, key := range s.rowOrder {
		order[key] = i
	}
	for name, machine := range s.machines {
		for _, session := range machine.sessions {
			r := s.sessionRow(name, session)
			if !r.Pinned && s.machineFilter != "" && name != s.machineFilter {
				continue
			}
			if !r.Pinned && s.attentionOnly && !needsAttention(r.attention) {
				continue
			}
			if !r.Pinned && query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{
				name, r.name(), session.ID, session.Name, session.Role, session.Agent, session.Command, session.CWD, session.Status, r.attention,
			}, " ")), query) {
				continue
			}
			rows = append(rows, r)

		}
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if left.Pinned != right.Pinned {
			return left.Pinned
		}
		li, lk := order[left.key()]
		ri, rk := order[right.key()]
		if s.selectionTouched {
			if lk && rk {
				return li < ri
			}
			if lk != rk {
				return lk
			}
		}
		if needsAttention(left.attention) != needsAttention(right.attention) {
			return needsAttention(left.attention)
		}
		if rank := sessionRank(left.Status) - sessionRank(right.Status); rank != 0 {
			return rank < 0
		}
		if left.machine != right.machine {
			if left.machine == "local" || right.machine == "local" {
				return left.machine == "local"
			}
			return left.machine < right.machine
		}
		if left.project() != right.project() {
			return left.project() < right.project()
		}
		if left.name() != right.name() {
			return left.name() < right.name()
		}
		return left.ID < right.ID
	})
	return rows
}

func sessionRank(status string) int {
	switch status {
	case "working":
		return 0
	case "idle":
		return 1
	case "done", "exited":
		return 2
	default:
		return 3
	}
}

func (s *state) selection() (row, bool) {
	rows := s.rows()
	for _, r := range rows {
		if r.key() == s.selected {
			return r, true
		}
	}
	if len(rows) > 0 {
		if s.filter != "" || s.attentionOnly {
			for _, r := range rows {
				if !r.Pinned {
					return r, true
				}
			}
		}
		return rows[0], true
	}
	return row{}, false
}

func (s *state) reconcile() {
	s.observeAttention()
	ordered := s.rows()
	s.rowOrder = nil
	for _, r := range ordered {
		s.rowOrder = append(s.rowOrder, r.key())
	}
	if rows := s.rows(); !s.selectionTouched && len(rows) > 0 && rows[0].Pinned {
		s.selected = rows[0].key()
	}
	if r, ok := s.selection(); ok {
		s.selected = r.key()
	} else {
		s.selected = ""
	}
}

func (s *state) move(delta int) {
	s.selectionTouched = true
	rows := s.rows()
	for i, r := range rows {
		if r.key() == s.selected {
			s.selected = rows[max(0, min(len(rows)-1, i+delta))].key()
			if s.selected != r.key() {
				delete(s.permissionRetained, r.key())
			}
			return
		}
	}
	s.reconcile()
}

// handle is the input state machine. Actions capture their target before a
// refresh can reorder rows or remove the selected session.
func (s *state) handle(key string) (act *action, quit bool) {
	if key == "ctrl-c" {
		return nil, true
	}
	if s.mode == permissionReview {
		switch key {
		case "escape", "q":
			s.mode = navigate
		case "enter":
			current, found := s.selection()
			p, live := s.reviewedPermission(current)
			s.mode = navigate
			if !found || !live || !current.PermissionRespond || s.target.Permission == nil || p.RequestID != s.target.Permission.RequestID || p.Digest != s.target.Permission.Digest || s.overlayOffset < s.overlayLimit {
				s.message = "Review every line of the current request, or Answer in terminal"
				return nil, false
			}
			response := permissionResponse(p, "allow")
			s.markPermissionAnswered(current, p)
			return &action{kind: permissionReview, row: current, permission: &response}, false
		default:
			s.scrollOverlay(key)
		}
		return nil, false
	}
	if (s.mode == navigate || s.mode == details) && !s.recent && (key == "[" || key == "]") {
		if s.stepPermission(key) {
			return nil, false
		}
	}
	if (s.mode == navigate || s.mode == details) && !s.recent && (key == "a" || key == "d") {
		if act, handled := s.permissionAction(key); handled {
			return act, false
		}
		if r, ok := s.selection(); ok && r.Status == "blocked" {
			s.message = "Answer in terminal"
			return nil, false
		}
	}
	if s.isSetupMode() {
		return s.setupHandle(key)
	}
	if s.mode == diagnostics || s.mode == diagnosticInput || s.mode == diagnosticReport {
		return s.diagnosticHandle(key)
	}
	if s.mode == actionMenu {
		switch key {
		case "escape", "q", ".":
			s.mode = navigate
		case "x":
			s.mode, s.overlayOffset = confirmStop, 0
		case "r":
			s.mode, s.input = rename, s.target.Name
		case "t":
			if takeoverAvailable(s.target) {
				s.mode, s.overlayOffset = confirmTakeover, 0
			} else {
				s.message = "Input control is not available for this session."
			}
		}
		return nil, false
	}
	if s.mode == machineDetails {
		if key == "escape" || key == "enter" || key == "q" || key == "M" {
			s.mode = navigate
		} else {
			s.scrollOverlay(key)
		}
		return nil, false
	}
	if s.recent && s.mode != help {
		return s.runHandle(key)
	}
	if s.mode == machinePicker {
		switch key {
		case "escape", "q", "m":
			s.mode = navigate
		case "up", "k":
			s.picker = max(0, s.picker-1)
		case "down", "j":
			s.picker = min(len(s.machineNames()), s.picker+1)
		case "home":
			s.picker = 0
		case "end":
			s.picker = len(s.machineNames())
		case "i", "M":
			if s.picker > 0 {
				s.target = row{machine: s.machineNames()[s.picker-1]}
				s.mode, s.overlayOffset = machineDetails, 0
			}
		case "enter":
			s.selectionTouched = true
			s.machineFilter = ""
			if s.picker > 0 {
				s.machineFilter = s.machineNames()[s.picker-1]
			}
			s.mode, s.message = navigate, ""
			s.reconcile()
		}
		return nil, false
	}
	if s.mode == details || s.mode == help {
		switch key {
		case "enter":
			// Enter on session details opens the session it describes.
			wasDetails := s.mode == details
			s.mode = navigate
			if wasDetails {
				return s.handle("enter")
			}
		case "escape", "q", "i", "?":
			s.mode = navigate
		case "up", "k":
			s.overlayOffset = max(0, s.overlayOffset-1)
		case "down", "j":
			s.overlayOffset = min(s.overlayLimit, s.overlayOffset+1)
		case "pageup":
			s.overlayOffset = max(0, s.overlayOffset-5)
		case "pagedown":
			s.overlayOffset = min(s.overlayLimit, s.overlayOffset+5)
		case "home":
			s.overlayOffset = 0
		}
		return nil, false
	}
	if s.mode != navigate {
		if key == "escape" {
			if s.mode == newCWD {
				s.draftAgent, s.draftCWD, s.draftMachine = s.agent, s.input, s.target.machine
			}
			if s.mode == filter {
				s.filter = s.previous
				s.selected = s.previousSelected
				s.reconcile()
			}
			s.mode, s.message = navigate, ""
			return nil, false
		}
		if (s.mode == confirmStop || s.mode == confirmTakeover) && (key == "j" || key == "k" || key == "up" || key == "down" || key == "pageup" || key == "pagedown" || key == "home") {
			s.scrollOverlay(key)
			return nil, false
		}
		if s.mode == confirmStop || s.mode == confirmTakeover {
			kind := s.mode
			s.mode = navigate
			if (key == "y" || key == "Y") && s.target.ID != "" && !accessDenied(s.machines[s.target.machine].err) {
				return &action{kind: kind, row: s.target, attach: kind == confirmTakeover, takeover: kind == confirmTakeover}, false
			}
			return nil, false
		}
		if key == "enter" {
			switch s.mode {
			case rename:
				if s.input == "" {
					s.message = "Name cannot be empty"
					return nil, false
				}
				if err := eggclient.ValidateSessionName(s.input); err != nil {
					s.message = err.Error()
					return nil, false
				}
				act = &action{kind: rename, row: s.target, name: s.input}
			case newAgent:
				s.agent = strings.TrimSpace(s.input)
				if s.target.machine == "local" && s.agents != nil {
					for _, a := range s.agents {
						if a.ID == s.agent && a.Path == "" {
							s.setupAgent, s.mode = a, installConsent
							return nil, false
						}
					}
				}
				s.mode, s.input = newCWD, s.target.CWD
				if s.draftMachine == s.target.machine && s.draftAgent == s.agent {
					s.input = s.draftCWD
				}
				if s.input == "" && s.target.machine == "local" {
					s.input = s.defaultCWD
				}
				return nil, false
			case newCWD:
				if strings.TrimSpace(s.input) == "" {
					s.message = "Working directory cannot be empty"
					return nil, false
				}
				act = &action{kind: newCWD, row: s.target, agent: s.agent, cwd: s.input}
			}
			s.mode = navigate
			return act, false
		}
		switch key {
		case "backspace":
			runes := []rune(s.input)
			if len(runes) > 0 {
				s.input = string(runes[:len(runes)-1])
			}
		case "ctrl-u":
			s.input = ""
		default:
			if len([]rune(key)) == 1 && !unicode.IsControl([]rune(key)[0]) {
				s.input += key
			}
		}
		s.message = ""
		if s.mode == filter {
			s.filter = s.input
			if r, ok := s.selection(); ok && r.Pinned && s.filter != "" {
				for _, match := range s.rows() {
					if !match.Pinned {
						s.selected = match.key()
						break
					}
				}
			}
			s.reconcile()
		}
		return nil, false
	}
	switch key {
	case ".":
		s.selectionTouched = true
		if r, ok := s.selection(); ok {
			s.target, s.mode, s.overlayOffset = r, actionMenu, 0
		}
	case "a", "A":
		s.openSetup()
	case "d", "D":
		s.mode, s.overlayOffset = diagnostics, 0
	case "M":
		name := s.machineFilter
		if r, ok := s.selection(); ok {
			name = r.machine
		}
		if name == "" {
			name = "local"
		}
		s.target, s.mode, s.overlayOffset = row{machine: name}, machineDetails, 0
	case "h":
		s.recent = true
		h := s.history()
		h.requested, h.next = true, false
	case "q":
		return nil, true
	case "pageup":
		s.move(-8)
	case "pagedown":
		s.move(8)
	case "home":
		s.move(-len(s.rows()))
	case "end":
		s.move(len(s.rows()))
	case "up", "left", "k":
		s.move(-1)
	case "down", "right", "j":
		s.move(1)
	case "m":
		s.mode, s.picker = machinePicker, 0
		for i, name := range s.machineNames() {
			if name == s.machineFilter {
				s.picker = i + 1
			}
		}
	case "i":
		s.selectionTouched = true
		if r, ok := s.selection(); ok {
			s.target, s.mode, s.overlayOffset = r, details, 0
			s.acknowledge(r)
		}
	case "?":
		s.mode, s.overlayOffset = help, 0
	case "!":
		s.selectionTouched = true
		s.attentionOnly = !s.attentionOnly
		s.reconcile()
	case "b":
		s.bell = !s.bell
		s.message = "Bell " + s.bellSetting() + " for this dashboard"
	case "R":
		s.selectionTouched = true
		s.rowOrder = nil
		s.reconcile()
		s.refresh = true
	case "/":
		s.selectionTouched = true
		s.mode, s.previous, s.input = filter, s.filter, s.filter
		s.previousSelected = s.selected
	case "enter", "v", "t", "r", "x", "n":
		s.selectionTouched = true
		if s.busy {
			return nil, false
		}
		r, ok := s.selection()
		if key == "n" && s.machineFilter != "" && (!ok || r.machine != s.machineFilter) {
			r, ok = row{machine: s.machineFilter}, true
		}
		if key != "n" && !ok {
			return nil, false
		}
		if !ok {
			r.machine = s.machineFilter
			if r.machine == "" {
				r.machine = "local"
			}
		}
		if accessDenied(s.machines[r.machine].err) {
			s.message = "Access denied. R refresh."
			return nil, false
		}
		r.generation = s.machines[r.machine].generation
		r.identity = s.attentionIdentity(r.machine, r.ID)
		s.target, s.message = r, ""
		switch key {
		case "enter":
			if s.stale(r.machine) {
				s.message = machineLabel(r.machine) + " unreachable. R retries this machine."
				return nil, false
			}
			return &action{row: r, attach: true}, false
		case "v":
			if s.stale(r.machine) {
				s.message = machineLabel(r.machine) + " unreachable. R retries this machine."
				return nil, false
			}
			return &action{row: r, attach: true, readOnly: true}, false
		case "t":
			if !takeoverAvailable(r) {
				s.message = "Input control is not available for this session"
				return nil, false
			}
			s.mode, s.overlayOffset = confirmTakeover, 0
		case "r":
			s.mode, s.input = rename, r.Name
		case "x":
			s.mode, s.overlayOffset = confirmStop, 0
		case "n":
			s.mode, s.input = newAgent, s.defaultAgent
		}
	}
	return nil, false
}

func takeoverAvailable(r row) bool {
	return config.Channel() == "preview" && r.WriterID != "" && r.InputEpoch > 0
}
