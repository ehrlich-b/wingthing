package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestPreviewRemoteFailureRemainsVisible(t *testing.T) {
	s := fixture()
	m := s.machines["work"]
	m.sessions[0].OriginWingID = "demo-origin"
	s.machines["work"] = m
	r := s.sessionRow("work", m.sessions[0])
	p := newPreviews()
	p.reads[r.identity] = previewRead{serial: 1, origin: r.OriginWingID}
	p.active = 1
	p.apply(s, previewResult{target: r, serial: 1, err: errors.New("permission denied")})
	if !s.preview(r).failed || !strings.Contains(lastLine(s, r), "unavailable") {
		t.Fatalf("remote read failure hidden: preview=%+v line=%q", s.preview(r), lastLine(s, r))
	}
}

func TestPreviewInitialRemoteReadSaysLoading(t *testing.T) {
	s := fixture()
	m := s.machines["work"]
	m.updated = time.Now()
	m.sessions[0].OriginWingID = "demo-origin"
	s.machines["work"] = m
	s.selected = "work:one"
	r, _ := s.selection()
	s.previewReader = func(ctx context.Context, r row) (sessionPreview, error) {
		<-ctx.Done()
		return sessionPreview{}, ctx.Err()
	}
	p := newPreviews()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer p.workers.Wait()
	defer p.stop()
	p.request(ctx, s, true, time.Now())
	if !s.preview(r).loading || !strings.Contains(lastLine(s, r), "Loading") {
		t.Fatalf("initial remote read concealed pending state: %q", lastLine(s, r))
	}
	cancel()
}

func TestPreviewStaleCacheStillBounded(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {err: "connection refused"}}, previews: map[attentionIdentity]sessionPreview{}}
	m := s.machines["local"]
	for i := 0; i < 12; i++ {
		m.sessions = append(m.sessions, session(fmt.Sprint(i), "Task "+fmt.Sprint(i), "/project", "idle"))
	}
	s.machines["local"] = m
	s.reconcile()
	p := newPreviews()
	for _, r := range s.rows() {
		p.reads[r.identity] = previewRead{cursor: 1}
		s.previews[r.identity] = sessionPreview{update: "Saved report"}
	}
	p.request(t.Context(), s, true, time.Now(), s.rows()...)
	if len(p.reads) != 0 {
		t.Fatal("stale reads were not pruned")
	}
	s.machines["local"] = machine{sessions: []eggclient.LocalSession{session("next", "Next task", "/project", "idle")}, updated: time.Now()}
	s.selected = "local:next"
	s.reconcile()
	s.previewReader = func(context.Context, row) (sessionPreview, error) { return sessionPreview{update: "Next report"}, nil }
	p.request(t.Context(), s, true, time.Now(), s.rows()...)
	select {
	case result := <-p.results:
		p.apply(s, result)
	case <-time.After(time.Second):
		t.Fatal("bounded fixture reader stalled")
	}
	p.stop()
	p.workers.Wait()
	if len(s.previews) > 12 {
		t.Fatalf("retained %d caches after stale targets disappeared; max 12", len(s.previews))
	}
}

func TestPreviewTodayNamesUnknownCoverage(t *testing.T) {
	s := fixture()
	m := s.machines["work"]
	m.err = "connection refused"
	s.machines["work"] = m
	r, _ := s.selection()
	text := strings.Join(s.paperPane(r, time.Now(), 48, 40), "\n")
	suffix := strings.SplitN(text, "TODAY", 2)[1]
	if strings.Contains(suffix, "0 done · 0 failed") && !strings.Contains(suffix, "known") && !strings.Contains(suffix, "partial") && !strings.Contains(suffix, "unavailable") && !strings.Contains(suffix, "not reported") {
		t.Fatalf("TODAY claims complete zero with unavailable source: %s", suffix)
	}
}

func TestPreviewCancelAndReplaceNeverExceedsTwoWorkers(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {updated: time.Now()}}}
	m := s.machines["local"]
	for i := 0; i < 13; i++ {
		m.sessions = append(m.sessions, session(fmt.Sprint(i), "Task "+fmt.Sprint(i), "/project", "idle"))
	}
	s.machines["local"] = m
	s.reconcile()
	started := make(chan string, 2)
	var active, peak atomic.Int32
	s.previewReader = func(ctx context.Context, r row) (sessionPreview, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- r.key()
		<-ctx.Done()
		return sessionPreview{}, ctx.Err()
	}
	p := newPreviews()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer p.workers.Wait()
	defer p.stop()
	rows := s.rows()
	p.request(ctx, s, true, time.Now(), rows...)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("initial worker did not start")
		}
	}
	s.selected = rows[12].key()
	p.request(ctx, s, true, time.Now(), rows[12])
	if p.active > 2 || len(p.reads) > 12 {
		t.Fatalf("scheduler over limit: active=%d retained=%d", p.active, len(p.reads))
	}
	for i := 0; i < 2; i++ {
		select {
		case result := <-p.results:
			p.apply(s, result)
		case <-time.After(time.Second):
			t.Fatal("cancelled worker did not finish")
		}
	}
	p.request(ctx, s, true, time.Now(), rows[12])
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("replacement worker did not start")
	}
	if peak.Load() > 2 {
		t.Fatalf("%d concurrent workers", peak.Load())
	}
	cancel()
}

func TestPreviewUnknownAndDeniedMachineCounts(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {updated: time.Now()}, "Studio": {loading: true}}, target: row{machine: "Studio"}}
	if text := strings.Join(s.machineLines(time.Now()), "\n"); strings.Contains(text, "0 sessions") {
		t.Fatalf("never-fetched details claim zero: %s", text)
	}
	s.machines["Studio"] = machine{updated: time.Now().Add(-time.Minute), err: "permission denied"}
	if text := strings.Join(s.paperPane(row{machine: "local"}, time.Now(), 48, 40), "\n"); strings.Contains(text, "Studio · access denied · 0 sessions") {
		t.Fatalf("denied cache claims zero: %s", text)
	}
	if text := strings.Join(s.machineLines(time.Now()), "\n"); strings.Contains(text, "0 sessions") {
		t.Fatalf("denied details claim zero: %s", text)
	}
}

func TestInventoryDenialClearsFrozenTargetAndPreventsConfirmedMutation(t *testing.T) {
	for _, mode := range []mode{details, rename, confirmStop, confirmTakeover, actionMenu, newAgent, newCWD} {
		s := fixture()
		s.selected = "work:one"
		s.target, _ = s.selection()
		s.target.Name = "Private task"
		s.target.CWD = "/private/project"
		s.mode = mode
		s.input = "private draft"
		s.draftMachine, s.draftCWD = "work", "/private/project"
		newInventory(nil).apply(s, inventoryResult{name: "work", err: errors.New("grant revoked"), updated: time.Now()})
		if s.mode != navigate || s.target.ID != "" || s.target.CWD != "" || s.input != "" || s.draftCWD != "" {
			t.Fatalf("denial kept protected target in %v", mode)
		}
		if act, _ := s.handle("y"); act != nil {
			t.Fatal("denied confirmation still dispatched old target")
		}
		text := render(s, 80, 24, time.Now())
		if strings.Contains(text, "Private task") || strings.Contains(text, "private/project") {
			t.Fatal("denied metadata remained visible")
		}
	}
	s := fixture()
	s.target, _ = s.selection()
	s.mode = details
	key := s.target.key()
	newInventory(nil).apply(s, inventoryResult{name: "work", err: errors.New("permission denied"), updated: time.Now()})
	if s.mode != details || s.target.key() != key {
		t.Fatal("remote denial cancelled unrelated local detail")
	}
}

func TestDeniedMachineScopeCannotStartOrReplaceLocalWork(t *testing.T) {
	s := fixture()
	s.machineFilter = "work"
	newInventory(nil).apply(s, inventoryResult{name: "work", err: errors.New("permission denied"), updated: time.Now()})
	if act, _ := s.handle("n"); act != nil || s.mode != navigate {
		t.Fatal("denied scope offered a new launch")
	}
	s.machineFilter = "local"
	s.reconcile()
	s.handle("n")
	if s.mode != newAgent || s.target.machine != "local" {
		t.Fatal("denied remote disabled local creation")
	}
}

func TestPreviewInventoryReadsSerializeAndOtherMachineProceeds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := &state{machines: map[string]machine{"local": {sessions: []eggclient.LocalSession{session("one", "Task", "/project", "idle")}}, "other": {}}}
	s.reconcile()
	started, release := make(chan struct{}), make(chan struct{})
	var nativeReaders atomic.Int32
	var overlap atomic.Bool
	s.previewReader = func(ctx context.Context, _ row) (sessionPreview, error) {
		if nativeReaders.Add(1) != 1 {
			overlap.Store(true)
		}
		defer nativeReaders.Add(-1)
		close(started)
		select {
		case <-release:
			return sessionPreview{}, nil
		case <-ctx.Done():
			return sessionPreview{}, ctx.Err()
		}
	}
	p := newPreviews()
	inv := newInventory(func(_ context.Context, name string) ([]eggclient.LocalSession, error) {
		if name == "local" {
			if nativeReaders.Add(1) != 1 {
				overlap.Store(true)
			}
			defer nativeReaders.Add(-1)
		}
		return nil, nil
	})
	gates := machineReads{}
	p.readGates, inv.readGates = gates, gates
	defer func() { cancel(); p.stop(); p.workers.Wait(); inv.workers.Wait() }()
	p.request(ctx, s, true, time.Now())
	receive(t, started)
	inv.start(ctx, s, "local")
	inv.start(ctx, s, "other")
	if result := receive(t, inv.results); result.name != "other" {
		t.Fatal("inventory overlapped its preview or blocked an unrelated machine")
	}
	close(release)
	if result := receive(t, inv.results); result.name != "local" || result.err != nil {
		t.Fatal("inventory did not resume after preview completion")
	}
	if overlap.Load() {
		t.Fatal("inventory and preview imported the same native journal concurrently")
	}
}

func TestInventoryWaitingForPreviewCancelsWithoutDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := &state{machines: map[string]machine{"local": {sessions: []eggclient.LocalSession{session("one", "Task", "/project", "idle")}}}}
	s.reconcile()
	started := make(chan struct{})
	s.previewReader = func(ctx context.Context, _ row) (sessionPreview, error) {
		close(started)
		<-ctx.Done()
		return sessionPreview{}, ctx.Err()
	}
	var queries atomic.Int32
	inv := newInventory(func(context.Context, string) ([]eggclient.LocalSession, error) { queries.Add(1); return nil, nil })
	p := newPreviews()
	gates := machineReads{}
	p.readGates, inv.readGates = gates, gates
	defer func() { cancel(); p.stop(); p.workers.Wait(); inv.workers.Wait() }()
	p.request(ctx, s, true, time.Now())
	receive(t, started)
	inv.start(ctx, s, "local")
	inv.cancel["local"]()
	joined := make(chan struct{})
	go func() { inv.workers.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("cancelled queued inventory did not join")
	}
	if queries.Load() != 0 {
		t.Fatal("cancelled queued inventory dispatched a native query")
	}
	cancel()
	p.workers.Wait()
}

func TestPreviewQueuedReadDeadlineIncludesWait(t *testing.T) {
	s := &state{machines: map[string]machine{"local": {sessions: []eggclient.LocalSession{session("one", "Task", "/project", "idle")}}}}
	s.reconcile()
	var reads atomic.Int32
	s.previewReader = func(context.Context, row) (sessionPreview, error) { reads.Add(1); return sessionPreview{}, nil }
	p := newPreviews()
	p.readGates = machineReads{}
	release, err := acquireMachineRead(t.Context(), p.readGates.gate("local"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	defer func() { p.stop(); p.workers.Wait() }()
	started := time.Now()
	p.request(t.Context(), s, true, started)
	select {
	case result := <-p.results:
		if !errors.Is(result.err, context.DeadlineExceeded) || reads.Load() != 0 {
			t.Fatal("queued preview dispatched after its read deadline")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("preview queue wait extended its two-second deadline")
	}
}

func TestLoadingInventoryDefersNewPreviewAndRetainsDesiredCache(t *testing.T) {
	s := fixture()
	r, _ := s.selection()
	m := s.machines[r.machine]
	m.loading = true
	s.machines[r.machine] = m
	s.previews = map[attentionIdentity]sessionPreview{r.identity: {origin: r.OriginWingID, update: "Observed reply"}}
	s.previewReader = func(context.Context, row) (sessionPreview, error) {
		t.Error("preview started during inventory")
		return sessionPreview{}, nil
	}
	p := newPreviews()
	p.request(t.Context(), s, true, time.Now())
	p.workers.Wait()
	if p.active != 0 || s.latestUpdate(r) != "Observed reply" {
		t.Fatal("inventory loading started a read or discarded permitted cache")
	}
}
