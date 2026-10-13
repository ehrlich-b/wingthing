package fleet

import (
	"testing"
	"time"
)

func TestMergeCollisionTwoRoutesAndFallback(t *testing.T) {
	now := time.Now().UTC()
	at := now.Format(time.RFC3339Nano)
	mac := Session{Identity: Identity{"mac-wing", "same-short-id"}, MachineLabel: "EhrlichMac", Name: "local run", Routes: []Route{{WingID: "mac-wing", Kind: "direct", Healthy: true, ObservedAt: at}}}
	forge := Session{Identity: Identity{"forge-wing", "same-short-id"}, MachineLabel: "forge", Name: "wing", Pinned: true, Role: "coordinator", Routes: []Route{{WingID: "forge-wing", Kind: "direct", Healthy: true, ObservedAt: at}}}
	via := forge
	via.Routes = []Route{{WingID: "mac-wing", MachineLabel: "EhrlichMac", Kind: "via", Remote: "forge", Healthy: true, ObservedAt: at}}
	inv := Merge([]Inventory{{Complete: true, Sessions: []Session{mac, via}}, {Complete: true, Sessions: []Session{forge}}})
	if len(inv.Sessions) != 2 || inv.Sessions[0].OriginWingID != "forge-wing" || len(inv.Sessions[0].Routes) != 2 {
		t.Fatalf("collision or pin lost: %+v", inv)
	}
	row, e := Resolve(inv.Sessions, forge.Identity)
	if e != nil {
		t.Fatal(e)
	}
	route, e := SelectRoute(row, now)
	if e != nil || route.Kind != "direct" {
		t.Fatalf("healthy direct not preferred: %+v %v", route, e)
	}
	row.Routes[0].Healthy = false
	route, e = SelectRoute(row, now)
	if e != nil || route.Kind != "via" || route.WingID != "mac-wing" {
		t.Fatalf("fallback: %+v %v", route, e)
	}
	if _, e = Resolve(inv.Sessions, Identity{SessionID: "same-short-id"}); e == nil {
		t.Fatal("ambiguous short-only action allowed")
	}
}
func TestMergeUnprovenOriginsStaySeparateAndRoutesExpire(t *testing.T) {
	now := time.Now().UTC()
	at := now.Format(time.RFC3339Nano)
	a := Session{Identity: Identity{"", "abc"}, Name: "same", MachineLabel: "forge", Routes: []Route{{WingID: "gateway", Kind: "via", Healthy: true, ObservedAt: at}}}
	b := a
	b.OriginWingID = "origin"
	b.Routes = []Route{{WingID: "origin", Kind: "direct", Healthy: true, ObservedAt: at}}
	inv := Merge([]Inventory{{Complete: true, Sessions: []Session{a, b}}})
	if len(inv.Sessions) != 2 || !inv.Sessions[0].PossibleDuplicate || !inv.Sessions[1].PossibleDuplicate {
		t.Fatalf("guessed equality: %+v", inv)
	}
	if _, e := SelectRoute(a, now); e == nil {
		t.Fatal("unknown origin acted")
	}
	b.Routes[0].ObservedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	if _, e := SelectRoute(b, now); e == nil {
		t.Fatal("stale action")
	}
	b.Routes[0].ObservedAt = now.Add(time.Minute).Format(time.RFC3339Nano)
	if _, e := SelectRoute(b, now); e == nil {
		t.Fatal("future freshness accepted")
	}
}
func TestMergeLatestRouteRevocationAndHealthyMetadata(t *testing.T) {
	now := time.Now().UTC()
	id := Identity{"origin", "run"}
	stale := Session{Identity: id, Name: "old", Routes: []Route{{WingID: "origin", Kind: "direct", Healthy: false, ObservedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)}}}
	fresh := stale
	fresh.Name = "new"
	fresh.Routes = []Route{{WingID: "gateway", Kind: "via", Healthy: true, ObservedAt: now.Format(time.RFC3339Nano)}}
	inv := Merge([]Inventory{{Complete: false, Sessions: []Session{stale}}, {Complete: true, Sessions: []Session{fresh}}})
	if inv.Complete || inv.Sessions[0].Name != "new" {
		t.Fatalf("stale metadata won: %+v", inv)
	}
	newer := fresh
	newer.Routes = append([]Route(nil), fresh.Routes...)
	newer.Routes[0].Healthy = false
	newer.Routes[0].ObservedAt = now.Add(time.Second).Format(time.RFC3339Nano)
	inv = Merge([]Inventory{inv, {Complete: true, Sessions: []Session{newer}}})
	if _, e := SelectRoute(inv.Sessions[0], now.Add(2*time.Second)); e == nil {
		t.Fatal("revoked route survived")
	}
}

func TestMergeFreshUnpinAndInvalidDirectRoute(t *testing.T) {
	now := time.Now().UTC()
	old := Session{Identity: Identity{"origin", "run"}, Pinned: true, Role: "coordinator", Routes: []Route{{WingID: "origin", Kind: "direct", Healthy: true, ObservedAt: now.Add(-time.Second).Format(time.RFC3339Nano)}}}
	new := old
	new.Pinned = false
	new.Routes = append([]Route(nil), old.Routes...)
	new.Routes[0].ObservedAt = now.Format(time.RFC3339Nano)
	inv := Merge([]Inventory{{Complete: true, Sessions: []Session{old}}, {Complete: true, Sessions: []Session{new}}})
	if inv.Sessions[0].Pinned {
		t.Fatal("stale pin overrode a fresh unpin")
	}
	new.Routes[0].WingID = "other"
	if _, e := SelectRoute(new, now); e == nil {
		t.Fatal("direct route to another origin")
	}
}

func TestMergeCurrentGatewayMetadataWinsOverOlderHealthyDirect(t *testing.T) {
	now := time.Now().UTC()
	old := Session{Identity: Identity{"origin", "run"}, Name: "old", Pinned: true, Role: "coordinator", Live: true, Status: "working", NeedsAttention: true, Routes: []Route{{WingID: "origin", Kind: "direct", Healthy: true, ObservedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)}}}
	current := old
	current.Name, current.Pinned, current.Live, current.Status, current.NeedsAttention = "current", false, false, "exited", false
	current.Routes = []Route{{WingID: "gateway", Kind: "via", Healthy: true, ObservedAt: now.Format(time.RFC3339Nano)}}
	for _, inventories := range [][]Inventory{{{Complete: true, Sessions: []Session{old}}, {Complete: true, Sessions: []Session{current}}}, {{Complete: true, Sessions: []Session{current}}, {Complete: true, Sessions: []Session{old}}}} {
		merged := Merge(inventories).Sessions[0]
		if merged.Pinned || merged.Live || merged.NeedsAttention || merged.Name != "current" || merged.Status != "exited" {
			t.Fatalf("stale metadata=%+v", merged)
		}
		route, err := SelectRoute(merged, now)
		if err != nil || route.Kind != "via" {
			t.Fatalf("expired direct selected: %+v %v", route, err)
		}
	}
	// Fresh direct routes still win dispatch even when via metadata is newer.
	old.Routes[0].ObservedAt = now.Add(-time.Second).Format(time.RFC3339Nano)
	merged := Merge([]Inventory{{Complete: true, Sessions: []Session{old}}, {Complete: true, Sessions: []Session{current}}}).Sessions[0]
	route, err := SelectRoute(merged, now)
	if err != nil || route.Kind != "direct" || merged.Name != "current" {
		t.Fatalf("metadata and route conflated: %+v %+v %v", merged, route, err)
	}
}
