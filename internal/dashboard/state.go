package dashboard

import (
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

type machine struct {
	generation string
	sessions   []eggclient.LocalSession
	updated    time.Time
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
	name := r.Name
	if name == "" {
		name = r.ID
	}
	if r.Role != "" {
		name = "[" + r.Role + "] " + name
	}
	if r.Pinned {
		name = "* " + name
	}
	return name
}

// A remote cwd is a remote path: never inspect or resolve it on this machine.
func (r row) project() string {
	if r.CWD == "" {
		return "(no cwd)"
	}
	return path.Clean(r.CWD)
}

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
	machineDetails
	diagnostics
	diagnosticInput
	diagnosticReport
)

type action struct {
	run      runRow
	args     []string
	kind     mode
	row      row
	agent    string
	cwd      string
	name     string
	attach   bool
	readOnly bool
	takeover bool
}

type state struct {
	recent            bool
	runs              *runHistory
	machines          map[string]machine
	registryError     string
	registrySequence  uint64
	selectionTouched  bool
	selected          string
	filter            string
	mode              mode
	input             string
	previous          string
	target            row
	agent             string
	defaultAgent      string
	defaultCWD        string
	message           string
	busy              bool
	machineFilter     string
	attentionOnly     bool
	picker            int
	overlayOffset     int
	overlayLimit      int
	refresh           bool
	localDir          string
	remotes           map[string]config.Remote
	attention         map[attentionIdentity]attentionRecord
	attentionProfiles map[string]attentionProfile
	attached          *attentionIdentity
	bell              bool
	pendingBells      int
	diagnostic        diagnosticState
}

func needsAttention(attention string) bool {
	return attention == "blocked" || attention == "unread" || attention == "failed"
}

func (s *state) sessionRow(name string, session eggclient.LocalSession) row {
	r := row{LocalSession: session, machine: name, generation: s.machines[name].generation, identity: s.attentionIdentity(name, session.ID)}
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
	blocked := make(map[string]bool)
	for name, machine := range s.machines {
		if s.machineFilter != "" && name != s.machineFilter {
			continue
		}
		for _, session := range machine.sessions {
			r := s.sessionRow(name, session)
			if s.attentionOnly && !needsAttention(r.attention) {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{
				name, session.ID, session.Name, session.Role, session.Agent, session.Command, session.CWD, session.Status, r.attention,
			}, " ")), query) {
				continue
			}
			rows = append(rows, r)
			if needsAttention(r.attention) {
				blocked[name+"\x00"+r.project()] = true
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if left.Pinned != right.Pinned {
			return left.Pinned
		}
		if left.machine != right.machine {
			if left.machine == "local" || right.machine == "local" {
				return left.machine == "local"
			}
			return left.machine < right.machine
		}
		if left.project() != right.project() {
			lb, rb := blocked[left.machine+"\x00"+left.project()], blocked[right.machine+"\x00"+right.project()]
			if lb != rb {
				return lb
			}
			return left.project() < right.project()
		}
		if needsAttention(left.attention) != needsAttention(right.attention) {
			return needsAttention(left.attention)
		}
		if left.name() != right.name() {
			return left.name() < right.name()
		}
		return left.ID < right.ID
	})
	return rows
}

func (s *state) selection() (row, bool) {
	rows := s.rows()
	for _, r := range rows {
		if r.key() == s.selected {
			return r, true
		}
	}
	if len(rows) > 0 {
		return rows[0], true
	}
	return row{}, false
}

func (s *state) reconcile() {
	s.observeAttention()
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
	if s.mode == diagnostics || s.mode == diagnosticInput || s.mode == diagnosticReport {
		return s.diagnosticHandle(key)
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
		case "escape", "q", "enter", "i", "?":
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
			if s.mode == filter {
				s.filter = s.previous
				s.reconcile()
			}
			s.mode, s.message = navigate, ""
			return nil, false
		}
		if s.mode == confirmStop || s.mode == confirmTakeover {
			kind := s.mode
			s.mode = navigate
			if key == "y" || key == "Y" {
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
				s.mode, s.input = newCWD, s.target.CWD
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
			s.reconcile()
		}
		return nil, false
	}
	switch key {
	case "d":
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
		if r, ok := s.selection(); ok {
			s.target, s.mode, s.overlayOffset = r, details, 0
			s.acknowledge(r)
		}
	case "?":
		s.mode, s.overlayOffset = help, 0
	case "!":
		s.attentionOnly = !s.attentionOnly
		s.reconcile()
	case "b":
		s.bell = !s.bell
		s.message = "Bell " + s.bellSetting() + " for this dashboard"
	case "R":
		s.refresh = true
	case "/":
		s.mode, s.previous, s.input = filter, s.filter, s.filter
	case "enter", "v", "t", "r", "x", "n":
		s.selectionTouched = true
		if s.busy {
			return nil, false
		}
		r, ok := s.selection()
		if key != "n" && !ok {
			return nil, false
		}
		if !ok {
			r.machine = s.machineFilter
			if r.machine == "" {
				r.machine = "local"
			}
		}
		r.generation = s.machines[r.machine].generation
		r.identity = s.attentionIdentity(r.machine, r.ID)
		s.target, s.message = r, ""
		switch key {
		case "enter":
			return &action{row: r, attach: true}, false
		case "v":
			return &action{row: r, attach: true, readOnly: true}, false
		case "t":
			if !takeoverAvailable(r) {
				s.message = "Takeover requires preview input ownership reported by this session"
				return nil, false
			}
			s.mode = confirmTakeover
		case "r":
			s.mode, s.input = rename, r.Name
		case "x":
			s.mode = confirmStop
		case "n":
			s.mode, s.input = newAgent, s.defaultAgent
		}
	}
	return nil, false
}

func takeoverAvailable(r row) bool {
	return config.Channel() == "preview" && r.WriterID != "" && r.InputEpoch > 0
}
