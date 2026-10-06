package dashboard

import (
	"context"
	"fmt"
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
	query    func(context.Context, string) ([]eggclient.LocalSession, error)
	results  chan inventoryResult
	inFlight map[string]bool
}

func newInventory(query func(context.Context, string) ([]eggclient.LocalSession, error)) *inventory {
	return &inventory{query: query, results: make(chan inventoryResult), inFlight: make(map[string]bool)}
}

// Each machine has at most one outstanding query. Results are delivered as
// soon as that machine answers; no aggregate wait sits on the render path.
func (i *inventory) refresh(ctx context.Context, s *state) {
	for name, machine := range s.machines {
		if i.inFlight[name] {
			continue
		}
		i.inFlight[name] = true
		machine.loading = true
		s.machines[name] = machine
		go func() {
			result := inventoryResult{name: name}
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						result.err = fmt.Errorf("inventory panic: %v", recovered)
					}
				}()
				result.sessions, result.err = i.query(ctx, name)
			}()
			result.updated = time.Now()
			select {
			case i.results <- result:
			case <-ctx.Done():
			}
		}()
	}
}

func (i *inventory) apply(s *state, result inventoryResult) {
	delete(i.inFlight, result.name)
	machine := s.machines[result.name]
	machine.loading = false
	if result.err != nil {
		machine.err = result.err.Error()
	} else {
		machine.sessions, machine.updated, machine.err = result.sessions, result.updated, ""
	}
	s.machines[result.name] = machine
	s.reconcile()
}
