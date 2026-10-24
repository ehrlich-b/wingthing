package dashboard

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

const refreshInterval = 2 * time.Second

type inventoryResult struct {
	name       string
	generation string
	sessions   []eggclient.LocalSession
	err        error
	updated    time.Time
}

type inventory struct {
	query        func(context.Context, string) ([]eggclient.LocalSession, error)
	queryProfile func(context.Context, string, config.Remote) ([]eggclient.LocalSession, error)
	cancel       map[string]context.CancelFunc
	results      chan inventoryResult
	inFlight     map[string]bool
	pending      map[string]bool
	failures     map[string]int
	nextQuery    map[string]time.Time
	now          func() time.Time
	workers      sync.WaitGroup
	readGates    machineReads
}

func newInventory(query func(context.Context, string) ([]eggclient.LocalSession, error)) *inventory {
	return &inventory{query: query, results: make(chan inventoryResult), inFlight: make(map[string]bool),
		cancel: make(map[string]context.CancelFunc), pending: make(map[string]bool), failures: make(map[string]int), nextQuery: make(map[string]time.Time), now: time.Now}
}

// Each machine has at most one outstanding query. Results are delivered as
// soon as that machine answers; no aggregate wait sits on the render path.
func (i *inventory) refresh(ctx context.Context, s *state) {
	i.request(ctx, s, false)
}

func (i *inventory) forceRefresh(ctx context.Context, s *state) {
	i.request(ctx, s, true)
}

func (i *inventory) request(ctx context.Context, s *state, force bool) {
	if ctx.Err() != nil {
		return
	}
	for name := range s.machines {
		if name != "local" && s.registryError != "" {
			continue
		}
		if i.inFlight[name] {
			if force {
				i.pending[name] = true
			}
			continue
		}
		if !force && i.now().Before(i.nextQuery[name]) {
			continue
		}
		i.start(ctx, s, name)
	}
}

func (i *inventory) start(ctx context.Context, s *state, name string) {
	if ctx.Err() != nil {
		return
	}
	machine, exists := s.machines[name]
	if !exists {
		return
	}
	queryCtx, cancel := context.WithCancel(ctx)
	i.cancel[name] = cancel
	profile := s.remotes[name]
	gate := i.readGates.gate(name)
	i.inFlight[name] = true
	machine.loading = true
	s.machines[name] = machine
	i.workers.Add(1)
	go func() {
		defer i.workers.Done()
		defer cancel()
		result := inventoryResult{name: name, generation: machine.generation}
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					result.err = fmt.Errorf("inventory panic: %v", recovered)
				}
			}()
			release, err := acquireMachineRead(queryCtx, gate)
			if err != nil {
				result.err = err
				return
			}
			defer release()
			if i.queryProfile != nil {
				result.sessions, result.err = i.queryProfile(queryCtx, name, profile)
			} else {
				result.sessions, result.err = i.query(queryCtx, name)
			}
		}()
		result.updated = i.now()
		select {
		case i.results <- result:
		case <-queryCtx.Done():
		}
	}()
}

// apply is owned by the event loop, as are all scheduling maps. The returned
// flag consumes one coalesced explicit refresh after the finished query.
func (i *inventory) apply(s *state, result inventoryResult) bool {
	machine, exists := s.machines[result.name]
	if !exists || machine.generation != result.generation {
		return false
	}
	delete(i.inFlight, result.name)
	delete(i.cancel, result.name)
	machine.loading = false
	machine.attempted = result.updated
	if result.err != nil {
		machine.err = result.err.Error()
		if accessDenied(machine.err) {
			machine.sessions = nil
			if s.target.machine == result.name {
				switch s.mode {
				case details, confirmStop:
					// Keep the open page and say why in place, but drop the
					// session's private metadata; y then has no target to stop.
					s.target = row{machine: result.name}
					s.input, s.overlayOffset = "", 0
				case rename, confirmTakeover, actionMenu, newAgent, newCWD:
					s.target = row{}
					s.mode, s.input = navigate, ""
					s.message = "Access denied. R refresh."
				}
			}
			if s.draftMachine == result.name {
				s.draftAgent, s.draftCWD, s.draftMachine = "", "", ""
			}

			for identity := range s.previews {
				if identity.machine == result.name {
					delete(s.previews, identity)
				}
			}
		}
		i.failures[result.name] = min(i.failures[result.name]+1, 5)
		delay := min(2*time.Second<<(i.failures[result.name]-1), 30*time.Second)
		i.nextQuery[result.name] = i.now().Add(delay)
	} else {
		machine.sessions, machine.updated, machine.err = result.sessions, result.updated, ""
		delete(i.failures, result.name)
		delete(i.nextQuery, result.name)
	}
	s.machines[result.name] = machine
	if s.selected != "" && strings.HasPrefix(s.selected, result.name+":") {
		found := false
		for _, session := range machine.sessions {
			if result.name+":"+session.ID == s.selected {
				found = true
				break
			}
		}
		if !found {
			s.selectionLost = true
			s.message = "Selected session unavailable. j/k choose another."
		}
	}
	s.reconcile()
	pending := i.pending[result.name]
	delete(i.pending, result.name)
	return pending
}

// Reload is event-loop owned. Workers receive profile values, never a registry
// map that can change while SSH is outstanding.
func (i *inventory) reload(s *state, remotes map[string]config.Remote, err error) {
	if err != nil {
		s.registryError = err.Error()
		return
	}
	s.registryError = ""
	pickerName := ""
	names := s.machineNames()
	if s.mode == machinePicker && s.picker > 0 && s.picker <= len(names) {
		pickerName = names[s.picker-1]
	}
	for name := range s.remotes {
		current, exists := remotes[name]
		if exists && current == s.remotes[name] {
			continue
		}
		if cancel := i.cancel[name]; cancel != nil {
			cancel()
		}
		delete(i.cancel, name)
		delete(i.inFlight, name)
		delete(i.pending, name)
		delete(i.failures, name)
		delete(i.nextQuery, name)
		delete(s.machines, name)
		if s.selected != "" && strings.HasPrefix(s.selected, name+":") {
			s.selectionLost = true
			s.message = "Selected machine changed. j/k choose another."
		}
		if s.target.machine == name && (s.mode == rename || s.mode == confirmStop || s.mode == confirmTakeover || s.mode == newAgent || s.mode == newCWD || s.mode == details || s.mode == actionMenu) {
			s.mode, s.input = navigate, ""
			s.message = "Cancelled: saved profile " + name + " changed or was removed; select the current machine before acting"
		}
	}
	for name, profile := range remotes {
		if _, exists := s.machines[name]; !exists {
			s.registrySequence++
			s.machines[name] = machine{generation: fmt.Sprintf("%x/%d", sha256.Sum256([]byte(fmt.Sprintf("%#v", profile))), s.registrySequence)}
		}
	}
	s.remotes = remotes
	if _, exists := s.machines[s.machineFilter]; s.machineFilter != "" && !exists {
		s.machineFilter = ""
	}
	if s.mode == machinePicker {
		s.picker = 0
		for n, name := range s.machineNames() {
			if name == pickerName {
				s.picker = n + 1
			}
		}
	}
	s.reconcile()
}
