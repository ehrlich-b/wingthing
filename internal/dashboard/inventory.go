package dashboard

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

const refreshInterval = 2 * time.Second

type inventoryResult struct {
	name     string
	sessions []eggclient.LocalSession
	err      error
	updated  time.Time
}

type inventory struct {
	query     func(context.Context, string) ([]eggclient.LocalSession, error)
	results   chan inventoryResult
	inFlight  map[string]bool
	pending   map[string]bool
	failures  map[string]int
	nextQuery map[string]time.Time
	now       func() time.Time
	workers   sync.WaitGroup
}

func newInventory(query func(context.Context, string) ([]eggclient.LocalSession, error)) *inventory {
	return &inventory{query: query, results: make(chan inventoryResult), inFlight: make(map[string]bool),
		pending: make(map[string]bool), failures: make(map[string]int), nextQuery: make(map[string]time.Time), now: time.Now}
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
	machine := s.machines[name]
	i.inFlight[name] = true
	machine.loading = true
	s.machines[name] = machine
	i.workers.Add(1)
	go func() {
		defer i.workers.Done()
		result := inventoryResult{name: name}
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					result.err = fmt.Errorf("inventory panic: %v", recovered)
				}
			}()
			result.sessions, result.err = i.query(ctx, name)
		}()
		result.updated = i.now()
		select {
		case i.results <- result:
		case <-ctx.Done():
		}
	}()
}

// apply is owned by the event loop, as are all scheduling maps. The returned
// flag consumes one coalesced explicit refresh after the finished query.
func (i *inventory) apply(s *state, result inventoryResult) bool {
	delete(i.inFlight, result.name)
	machine := s.machines[result.name]
	machine.loading = false
	if result.err != nil {
		machine.err = result.err.Error()
		i.failures[result.name] = min(i.failures[result.name]+1, 5)
		delay := min(2*time.Second<<(i.failures[result.name]-1), 30*time.Second)
		i.nextQuery[result.name] = i.now().Add(delay)
	} else {
		machine.sessions, machine.updated, machine.err = result.sessions, result.updated, ""
		delete(i.failures, result.name)
		delete(i.nextQuery, result.name)
	}
	s.machines[result.name] = machine
	s.reconcile()
	pending := i.pending[result.name]
	delete(i.pending, result.name)
	return pending
}
