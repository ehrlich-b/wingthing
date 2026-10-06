package dashboard

import (
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

type machine struct {
	sessions []eggclient.LocalSession
	updated  time.Time
	err      string
	loading  bool
}

type row struct {
	eggclient.LocalSession
	machine string
}

func (r row) key() string { return r.machine + ":" + r.ID }

func (r row) name() string {
	if r.Name != "" {
		return r.Name
	}
	return r.ID
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
)

type action struct {
	kind   mode
	row    row
	agent  string
	cwd    string
	name   string
	attach bool
}

type state struct {
	machines     map[string]machine
	selected     string
	filter       string
	mode         mode
	input        string
	previous     string
	target       row
	agent        string
	defaultAgent string
	defaultCWD   string
	message      string
	busy         bool
}

func (s *state) rows() []row {
	var rows []row
	query := strings.ToLower(s.filter)
	blocked := make(map[string]bool)
	for name, machine := range s.machines {
		for _, session := range machine.sessions {
			r := row{LocalSession: session, machine: name}
			if query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{
				name, session.ID, session.Name, session.Agent, session.Command, session.CWD, session.Status,
			}, " ")), query) {
				continue
			}
			rows = append(rows, r)
			if session.Status == "blocked" {
				blocked[name+"\x00"+r.project()] = true
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
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
		if (left.Status == "blocked") != (right.Status == "blocked") {
			return left.Status == "blocked"
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
	if r, ok := s.selection(); ok {
		s.selected = r.key()
	} else {
		s.selected = ""
	}
}

func (s *state) move(delta int) {
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
	if s.mode != navigate {
		if key == "escape" {
			if s.mode == filter {
				s.filter = s.previous
				s.reconcile()
			}
			s.mode = navigate
			return nil, false
		}
		if s.mode == confirmStop {
			s.mode = navigate
			if key == "y" || key == "Y" {
				return &action{kind: confirmStop, row: s.target}, false
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
	case "q":
		return nil, true
	case "up", "left", "k":
		s.move(-1)
	case "down", "right", "j":
		s.move(1)
	case "/":
		s.mode, s.previous, s.input = filter, s.filter, s.filter
	case "enter", "r", "x", "n":
		if s.busy {
			return nil, false
		}
		r, ok := s.selection()
		if key != "n" && !ok {
			return nil, false
		}
		if !ok {
			r.machine = "local"
		}
		s.target, s.message = r, ""
		switch key {
		case "enter":
			return &action{row: r, attach: true}, false
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
