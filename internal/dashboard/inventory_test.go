package dashboard

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
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
	if s.selected != "local:live" || !s.machines["work"].loading || !strings.Contains(render(s, 80, 24, time.Now()), "> local") {
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

func TestDashboardInventoryBackoffAndExplicitRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	inv := newInventory(func(context.Context, string) ([]eggclient.LocalSession, error) { return nil, errors.New("offline") })
	defer func() { cancel(); inv.workers.Wait() }()
	now := time.Unix(100, 0)
	// Workers report real completion time; only the event-loop clock is fake.
	s := &state{machines: map[string]machine{"work": {}}}
	for _, delay := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		inv.forceRefresh(ctx, s)
		result := receive(t, inv.results)
		inv.workers.Wait()
		inv.now = func() time.Time { return now }
		inv.apply(s, result)
		if got := inv.nextQuery["work"].Sub(now); got != delay {
			t.Fatalf("backoff = %s, want %s", got, delay)
		}
		inv.refresh(ctx, s)
		if inv.inFlight["work"] {
			t.Fatal("automatic query bypassed failure backoff")
		}
		now = now.Add(delay)
	}
	inv.apply(s, inventoryResult{name: "work", sessions: []eggclient.LocalSession{session("ok", "ok", "/src", "unknown")}, updated: now})
	if inv.failures["work"] != 0 || !inv.nextQuery["work"].IsZero() {
		t.Fatal("recovery did not reset backoff")
	}
	inv.refresh(ctx, s)
	if !inv.inFlight["work"] {
		t.Fatal("healthy machine remained backed off")
	}
}

func TestDashboardInventoryCoalescesBusyRefreshAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var calls atomic.Int32
	inv := newInventory(func(ctx context.Context, _ string) ([]eggclient.LocalSession, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	defer func() { cancel(); inv.workers.Wait() }()
	s := &state{machines: map[string]machine{"local": {}}}
	inv.refresh(ctx, s)
	receive(t, started)
	for range 20 {
		inv.forceRefresh(ctx, s)
	}
	if calls.Load() != 1 {
		t.Fatal("busy refresh overlapped queries")
	}
	release <- struct{}{}
	if !inv.apply(s, receive(t, inv.results)) {
		t.Fatal("busy R was lost")
	}
	inv.start(ctx, s, "local")
	receive(t, started)
	if calls.Load() != 2 || inv.pending["local"] {
		t.Fatal("explicit refreshes were not coalesced")
	}
	cancel()
	joined := make(chan struct{})
	go func() { inv.workers.Wait(); close(joined) }()
	receive(t, joined)
	inv.forceRefresh(ctx, s)
	if calls.Load() != 2 {
		t.Fatal("refresh launched after cancellation")
	}
}

func TestDashboardInventoryQuitWaitsForQueryReaders(t *testing.T) {
	input, writer := pipeInput(t)
	started, canceled, reaped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	inv := newInventory(func(ctx context.Context, _ string) ([]eggclient.LocalSession, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-reaped
		return nil, ctx.Err()
	})
	var restored atomic.Int32
	finished := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer close(reaped)
	go func() {
		finished <- run(ctx, &state{machines: map[string]machine{"local": {}}}, inv, nil, fakeTerminal(io.Discard, &restored), remotepkg.IO{In: input, Out: io.Discard}, nil, nil)
	}()
	receive(t, started)
	if _, err := writer.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	receive(t, canceled)
	select {
	case <-finished:
		t.Fatal("quit returned before inventory reader joined")
	default:
	}
	reaped <- struct{}{}
	if err := receive(t, finished); err != nil || restored.Load() != 1 {
		t.Fatalf("quit cleanup: %v, restores=%d", err, restored.Load())
	}
}

func TestDashboardRegistryReplacementCancelsAndRejectsLateResults(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	inv := newInventory(nil)
	started := make(chan config.Remote, 8)
	canceled := make(chan struct{}, 8)
	release := make(chan struct{})
	defer func() { cancel(); close(release); inv.workers.Wait() }()
	inv.queryProfile = func(ctx context.Context, _ string, profile config.Remote) ([]eggclient.LocalSession, error) {
		started <- profile
		<-ctx.Done()
		canceled <- struct{}{}
		<-release // A transport can still return success after cancellation.
		return []eggclient.LocalSession{session("late", "old-host", "/old", "working")}, nil
	}
	s := &state{machines: map[string]machine{"local": {}}, selected: "local:healthy"}
	s.machines["local"] = machine{sessions: []eggclient.LocalSession{session("healthy", "local", "/src", "idle")}}
	first := config.Remote{SSHTarget: "old", WTBinary: "/old/wt", WingthingDir: "/old"}
	inv.reload(s, map[string]config.Remote{"work": first}, nil)
	old := s.machines["work"].generation
	inv.start(ctx, s, "work")
	if receive(t, started) != first {
		t.Fatal("query did not capture selected profile")
	}
	s.target = row{machine: "work", generation: old}
	s.mode = confirmStop
	next := config.Remote{SSHTarget: "new", WTBinary: "/new/wt", WingthingDir: "/new"}
	inv.reload(s, map[string]config.Remote{"work": next, "empty": {SSHTarget: "empty"}}, nil)
	receive(t, canceled)
	if s.mode != navigate || !strings.Contains(s.message, "Cancelled") || s.selected != "local:healthy" {
		t.Fatal("replacement lost healthy selection or old confirmation")
	}
	inv.start(ctx, s, "work")
	if receive(t, started) != next {
		t.Fatal("replacement reused old route")
	}
	current := s.machines["work"].generation
	if current == old || inv.apply(s, inventoryResult{name: "work", generation: old, sessions: []eggclient.LocalSession{session("late", "wrong", "/old", "working")}}) || !inv.inFlight["work"] || len(s.machines["work"].sessions) != 0 {
		t.Fatal("late result consumed replacement query")
	}
	inv.reload(s, nil, errors.New("malformed registry"))
	if s.registryError == "" || s.remotes["work"] != next || !strings.Contains(render(s, 120, 30, time.Now()), "Saved machines could not be loaded") {
		t.Fatal("malformed registry discarded last valid route")
	}
	inv.reload(s, map[string]config.Remote{}, nil)
	receive(t, canceled)
	inv.apply(s, inventoryResult{name: "work", generation: current})
	if _, exists := s.machines["work"]; exists {
		t.Fatal("removed machine resurrected")
	}
	inv.reload(s, map[string]config.Remote{"work": first}, nil)
	if s.registryError != "" || s.machines["work"].generation == old {
		t.Fatal("re-added identical profile reused retired generation")
	}
	inv.apply(s, inventoryResult{name: "work", generation: old})
	if len(s.machines["work"].sessions) != 0 {
		t.Fatal("old profile ABA result accepted")
	}
}

func TestDashboardRegistryCancelsPendingTargetsAndPreservesPicker(t *testing.T) {
	for _, prompt := range []mode{rename, confirmStop, confirmTakeover, newAgent, newCWD} {
		s := &state{machines: map[string]machine{"local": {}}}
		inv := newInventory(nil)
		inv.reload(s, map[string]config.Remote{"work": {SSHTarget: "host"}}, nil)
		s.mode, s.target = prompt, row{machine: "work", generation: s.machines["work"].generation}
		inv.reload(s, map[string]config.Remote{}, nil)
		if s.mode != navigate || !strings.Contains(s.message, "changed or was removed") {
			t.Fatalf("prompt %v survived removal", prompt)
		}
	}
	s := &state{machines: map[string]machine{"local": {}}}
	inv := newInventory(nil)
	inv.reload(s, map[string]config.Remote{"z": {SSHTarget: "host"}}, nil)
	s.mode, s.picker = machinePicker, 2
	inv.reload(s, map[string]config.Remote{"a": {SSHTarget: "empty"}, "z": {SSHTarget: "host"}}, nil)
	if s.picker != 3 {
		t.Fatal("new machine moved selected picker route")
	}
	inv.reload(s, map[string]config.Remote{}, nil)
	if s.picker != 0 {
		t.Fatal("removed picker selection not reset")
	}
}
