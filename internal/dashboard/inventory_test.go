package dashboard

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestDashboardInventorySlowRemoteDoesNotBlockLocal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow := make(chan struct{})
	started := make(chan struct{}, 1)
	var remoteQueries atomic.Int32
	inv := newInventory(func(ctx context.Context, name string) ([]eggclient.LocalSession, error) {
		if name == "work" {
			remoteQueries.Add(1)
			started <- struct{}{}
			select {
			case <-slow:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []eggclient.LocalSession{session("live", name, "/src", "blocked")}, nil
	})
	s := &state{machines: map[string]machine{"local": {}, "work": {}}}
	inv.refresh(ctx, s)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("remote query did not start")
	}
	inv.refresh(ctx, s)
	select {
	case result := <-inv.results:
		if result.name != "local" {
			t.Fatal("local update waited for a remote")
		}
		inv.apply(s, result)
	case <-time.After(time.Second):
		t.Fatal("slow remote blocked local inventory")
	}
	if s.selected != "local:live" || !s.machines["work"].loading || !strings.Contains(render(s, 80, 24, time.Now()), "> B local") {
		t.Fatal("local result was not renderable while remote was pending")
	}
	inv.refresh(ctx, s)
	select {
	case result := <-inv.results:
		inv.apply(s, result)
	case <-time.After(time.Second):
		t.Fatal("local refresh waited for remote")
	}
	if remoteQueries.Load() != 1 {
		t.Fatal("refresh overlapped an outstanding remote query")
	}
	close(slow)
	select {
	case result := <-inv.results:
		inv.apply(s, result)
	case <-time.After(time.Second):
		t.Fatal("remote result was not delivered")
	}
	if s.machines["work"].loading || len(s.rows()) != 2 || s.rows()[1].Status != "blocked" {
		t.Fatal("remote status was lost")
	}
	if refreshInterval != 2*time.Second {
		t.Fatal("refresh cadence changed")
	}
}

func TestDashboardInventoryErrorRetainsSnapshotAndRecovers(t *testing.T) {
	s := fixture()
	inv := newInventory(nil)
	before := s.machines["work"]
	inv.apply(s, inventoryResult{name: "work", err: errors.New("remote timed out")})
	if got := s.machines["work"]; len(got.sessions) != 1 || got.updated != before.updated || got.err != "remote timed out" || got.loading {
		t.Fatalf("failed refresh lost cached sessions: %#v", got)
	}
	inv.apply(s, inventoryResult{name: "work", sessions: nil, updated: time.Unix(99, 0)})
	if got := s.machines["work"]; len(got.sessions) != 0 || got.err != "" || got.updated.Unix() != 99 {
		t.Fatalf("successful refresh did not clear stale data: %#v", got)
	}
}

func TestDashboardInventoryPanicBecomesDiagnostic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inv := newInventory(func(context.Context, string) ([]eggclient.LocalSession, error) { panic("broken discovery") })
	s := &state{machines: map[string]machine{"local": {}}}
	inv.refresh(ctx, s)
	select {
	case result := <-inv.results:
		inv.apply(s, result)
		if !strings.Contains(s.machines["local"].err, "broken discovery") {
			t.Fatal("background panic escaped cleanup")
		}
	case <-time.After(time.Second):
		t.Fatal("panic lost the pending inventory result")
	}
}
