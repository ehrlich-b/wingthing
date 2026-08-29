package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/ws"
)

func TestEdgeRegistrationAndSnapshotCarryOrderingMetadata(t *testing.T) {
	type capturedWing struct {
		WingID        string `json:"wing_id"`
		ConnectionID  string `json:"connection_id"`
		ConnectedAtNS int64  `json:"connected_at_unix_nano"`
		Revision      uint64 `json:"revision"`
	}
	type capturedRequest struct {
		Path         string
		SnapshotAtNS int64          `json:"snapshot_at_unix_nano"`
		Wing         capturedWing   `json:"-"`
		Wings        []capturedWing `json:"wings"`
	}
	captured := make(chan capturedRequest, 2)
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body capturedRequest
		if r.URL.Path == "/internal/wing-register" {
			if err := json.NewDecoder(r.Body).Decode(&body.Wing); err != nil {
				t.Errorf("decode registration: %v", err)
			}
		} else {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode snapshot: %v", err)
			}
		}
		body.Path = r.URL.Path
		captured <- body
		writeJSON(w, http.StatusOK, map[string]any{"banned_users": []string{}})
	}))
	defer login.Close()

	generation := time.Now().Add(-time.Second).Round(0)
	s := NewServer(nil, ServerConfig{FlyMachineID: "edge-a", LoginNodeAddr: login.URL})
	wing := s.Wings.Add(&ConnectedWing{
		ID: "connection-7", WingID: "wing-7", UserID: "user-7",
		ConnectedAt: generation, Revision: 7,
	})
	s.registerWingWithLogin(wing)
	s.edgeSync(context.Background(), login.URL)

	seen := make(map[string]capturedRequest, 2)
	for range 2 {
		item := <-captured
		seen[item.Path] = item
	}
	registration := seen["/internal/wing-register"].Wing
	if registration.WingID != "wing-7" || registration.ConnectionID != "connection-7" ||
		registration.ConnectedAtNS != generation.UnixNano() || registration.Revision != 7 {
		t.Fatalf("registration ordering metadata = %#v", registration)
	}
	snapshot := seen["/internal/wing-sync"]
	if snapshot.SnapshotAtNS == 0 || len(snapshot.Wings) != 1 {
		t.Fatalf("snapshot ordering envelope = %#v", snapshot)
	}
	if got := snapshot.Wings[0]; got.WingID != "wing-7" || got.ConnectionID != "connection-7" ||
		got.ConnectedAtNS != generation.UnixNano() || got.Revision != 7 {
		t.Fatalf("snapshot ordering metadata = %#v", got)
	}
}

func TestWingMapCapabilitiesPropagationAndCompatibility(t *testing.T) {
	server := &Server{WingMap: NewWingMap()}

	register := httptest.NewRequest("POST", "/internal/wing-register", strings.NewReader(`{
        "wing_id":"current-wing","machine_id":"edge-a","user_id":"alice",
		"purpose_binding":true,"direct_mcp":true,"hosted_relay":"deny"
    }`))
	registerResult := httptest.NewRecorder()
	server.handleWingRegister(registerResult, register)
	if registerResult.Code != 200 {
		t.Fatalf("register status = %d body=%s", registerResult.Code, registerResult.Body.String())
	}
	current, ok := server.WingMap.Locate("current-wing")
	if !ok || current.HostedRelay != ws.HostedRelayDeny || !current.PurposeBinding || !current.DirectMCP {
		t.Fatalf("current wing location = %#v, found=%v", current, ok)
	}

	// N-1 edge snapshots omit both new fields. The N login node preserves the
	// deployed hosted-relay behavior, but must not claim that an old wing
	// supports purpose-bound direct control.
	syncRequest := httptest.NewRequest("POST", "/internal/wing-sync", strings.NewReader(`{
        "machine_id":"edge-b","snapshot_at":1,
        "wings":[{"wing_id":"legacy-wing","user_id":"bob"}]
    }`))
	syncResult := httptest.NewRecorder()
	server.handleWingSync(syncResult, syncRequest)
	if syncResult.Code != 200 {
		t.Fatalf("sync status = %d body=%s", syncResult.Code, syncResult.Body.String())
	}
	legacy, ok := server.WingMap.Locate("legacy-wing")
	if !ok || legacy.HostedRelay != "" || !ws.HostedRelayAllowed(legacy.HostedRelay) || legacy.PurposeBinding || legacy.DirectMCP {
		t.Fatalf("legacy wing location = %#v, found=%v", legacy, ok)
	}
}

func TestWingMapSnapshotCannotOverwriteNewerGeneration(t *testing.T) {
	m := NewWingMap()
	snapshotAt := time.Now()
	m.Register("wing", WingLocation{
		MachineID: "edge-a", ConnectionID: "new",
		GenerationAt: snapshotAt.Add(time.Second), Revision: 1,
	})
	m.ReconcileSnapshot("edge-a", map[string]WingLocation{
		"wing": {
			MachineID: "edge-a", ConnectionID: "old", HostedRelay: "allow",
			GenerationAt: snapshotAt.Add(-time.Second), Revision: 1,
		},
	}, snapshotAt)
	if loc, ok := m.Locate("wing"); !ok || loc.ConnectionID != "new" {
		t.Fatalf("stale snapshot replaced newer generation: %#v, found=%v", loc, ok)
	}
}

func TestWingMapRegisterCannotOverwriteNewerGenerationOnSameEdge(t *testing.T) {
	m := NewWingMap()
	now := time.Now()
	m.Register("wing", WingLocation{
		MachineID: "edge-a", ConnectionID: "new", GenerationAt: now, Revision: 1,
		HostedRelay: ws.HostedRelayDeny,
	})
	if applied := m.Register("wing", WingLocation{
		MachineID: "edge-a", ConnectionID: "old", GenerationAt: now.Add(-time.Second), Revision: 9,
		HostedRelay: ws.HostedRelayAllow,
	}); applied {
		t.Fatal("stale connection generation was applied")
	}
	loc, ok := m.Locate("wing")
	if !ok || loc.ConnectionID != "new" || loc.HostedRelay != ws.HostedRelayDeny {
		t.Fatalf("stale generation replaced current registration: %#v, found=%v", loc, ok)
	}
}

func TestWingMapRegisterCannotEvictLiveOwnerThroughAnotherEdge(t *testing.T) {
	m := NewWingMap()
	m.Register("shared-id", WingLocation{
		MachineID: "edge-a", ConnectionID: "alice-current", UserID: "alice",
	})
	if applied := m.Register("shared-id", WingLocation{
		MachineID: "edge-b", ConnectionID: "mallory-new", UserID: "mallory",
	}); applied {
		t.Fatal("different user evicted a live wing through another edge")
	}
	loc, ok := m.Locate("shared-id")
	if !ok || loc.MachineID != "edge-a" || loc.UserID != "alice" {
		t.Fatalf("live owner registration changed: %#v, found=%v", loc, ok)
	}

	// The same owner may reconnect through a new edge during an ordinary
	// anycast/rolling-deploy handoff.
	if applied := m.Register("shared-id", WingLocation{
		MachineID: "edge-b", ConnectionID: "alice-new", UserID: "alice",
	}); !applied {
		t.Fatal("same owner could not move a wing to another edge")
	}
}

func TestWingMapRegisterCanHealExpiredCrossOwnerCollision(t *testing.T) {
	m := NewWingMap()
	m.Register("shared-id", WingLocation{
		MachineID: "dead-edge", ConnectionID: "old", UserID: "old-user",
	})
	m.mu.Lock()
	m.edges["dead-edge"] = time.Now().Add(-time.Minute)
	m.mu.Unlock()

	if applied := m.Register("shared-id", WingLocation{
		MachineID: "edge-b", ConnectionID: "current", UserID: "new-user",
	}); !applied {
		t.Fatal("expired owner mapping prevented edge recovery")
	}
	loc, ok := m.Locate("shared-id")
	if !ok || loc.MachineID != "edge-b" || loc.UserID != "new-user" {
		t.Fatalf("expired mapping was not healed: %#v, found=%v", loc, ok)
	}
}

func TestWingMapRegisterCannotOverwriteNewerRevisionOnSameConnection(t *testing.T) {
	m := NewWingMap()
	generation := time.Now()
	m.Register("wing", WingLocation{
		MachineID: "edge-a", ConnectionID: "current", GenerationAt: generation, Revision: 2,
		Locked: true, HostedRelay: ws.HostedRelayDeny,
	})
	if applied := m.Register("wing", WingLocation{
		MachineID: "edge-a", ConnectionID: "current", GenerationAt: generation, Revision: 1,
		Locked: false, HostedRelay: ws.HostedRelayAllow,
	}); applied {
		t.Fatal("stale config revision was applied")
	}
	loc, ok := m.Locate("wing")
	if !ok || !loc.Locked || loc.Revision != 2 || loc.HostedRelay != ws.HostedRelayDeny {
		t.Fatalf("stale revision replaced current registration: %#v, found=%v", loc, ok)
	}
}

func TestWingMapRegisterCannotDowngradeCurrentMetadataToUnknown(t *testing.T) {
	t.Run("missing revision", func(t *testing.T) {
		m := NewWingMap()
		generation := time.Now()
		m.Register("wing", WingLocation{
			MachineID: "edge-a", ConnectionID: "current", GenerationAt: generation, Revision: 2,
			HostedRelay: ws.HostedRelayDeny,
		})
		if applied := m.Register("wing", WingLocation{
			MachineID: "edge-a", ConnectionID: "current", GenerationAt: generation,
			HostedRelay: ws.HostedRelayAllow,
		}); applied {
			t.Fatal("registration without revision downgraded current metadata")
		}
	})

	t.Run("missing generation", func(t *testing.T) {
		m := NewWingMap()
		m.Register("wing", WingLocation{
			MachineID: "edge-a", ConnectionID: "current", GenerationAt: time.Now(), Revision: 1,
			HostedRelay: ws.HostedRelayDeny,
		})
		if applied := m.Register("wing", WingLocation{
			MachineID: "edge-a", ConnectionID: "unknown", Revision: 1,
			HostedRelay: ws.HostedRelayAllow,
		}); applied {
			t.Fatal("registration without generation downgraded current metadata")
		}
	})
}

func TestWingMapSnapshotCannotOverwriteNewerConfigOnSameGeneration(t *testing.T) {
	m := NewWingMap()
	snapshotAt := time.Now()
	m.Register("wing", WingLocation{
		MachineID:    "edge-a",
		ConnectionID: "current",
		Locked:       true,
		DirectMCP:    true,
		HostedRelay:  ws.HostedRelayDeny,
		GenerationAt: snapshotAt.Add(-time.Second),
		Revision:     2,
	})
	m.ReconcileSnapshot("edge-a", map[string]WingLocation{
		"wing": {
			MachineID:    "edge-a",
			ConnectionID: "current",
			Locked:       false,
			DirectMCP:    false,
			HostedRelay:  ws.HostedRelayAllow,
			GenerationAt: snapshotAt.Add(-time.Second),
			Revision:     1,
		},
	}, snapshotAt)

	loc, ok := m.Locate("wing")
	if !ok || !loc.Locked || !loc.DirectMCP || loc.HostedRelay != ws.HostedRelayDeny {
		t.Fatalf("stale snapshot replaced newer config: %#v, found=%v", loc, ok)
	}
}

func TestWingMapSnapshotCannotDowngradeCurrentOrderingMetadata(t *testing.T) {
	t.Run("missing revision", func(t *testing.T) {
		m := NewWingMap()
		generation := time.Now().Add(-time.Minute)
		m.Register("wing", WingLocation{
			MachineID: "edge-a", ConnectionID: "current", GenerationAt: generation, Revision: 2,
			HostedRelay: ws.HostedRelayDeny,
		})
		m.ReconcileSnapshot("edge-a", map[string]WingLocation{
			"wing": {
				MachineID: "edge-a", ConnectionID: "current", GenerationAt: generation,
				HostedRelay: ws.HostedRelayAllow,
			},
		}, time.Now())
		loc, _ := m.Locate("wing")
		if loc.Revision != 2 || loc.HostedRelay != ws.HostedRelayDeny {
			t.Fatalf("snapshot without revision downgraded current metadata: %#v", loc)
		}
	})

	t.Run("missing generation", func(t *testing.T) {
		m := NewWingMap()
		m.Register("wing", WingLocation{
			MachineID: "edge-a", ConnectionID: "current", GenerationAt: time.Now().Add(-time.Minute), Revision: 1,
			HostedRelay: ws.HostedRelayDeny,
		})
		m.ReconcileSnapshot("edge-a", map[string]WingLocation{
			"wing": {
				MachineID: "edge-a", ConnectionID: "unknown", Revision: 1,
				HostedRelay: ws.HostedRelayAllow,
			},
		}, time.Now())
		loc, _ := m.Locate("wing")
		if loc.ConnectionID != "current" || loc.HostedRelay != ws.HostedRelayDeny {
			t.Fatalf("snapshot without generation downgraded current metadata: %#v", loc)
		}
	})
}

func TestWingMapSnapshotHealsNewerConfigOnSameGeneration(t *testing.T) {
	m := NewWingMap()
	generation := time.Now().Add(-time.Minute)
	m.Register("wing", WingLocation{
		MachineID: "edge-a", ConnectionID: "current", GenerationAt: generation,
		Revision: 1, Locked: false, HostedRelay: ws.HostedRelayAllow,
	})
	m.ReconcileSnapshot("edge-a", map[string]WingLocation{
		"wing": {
			MachineID: "edge-a", ConnectionID: "current", GenerationAt: generation,
			Revision: 2, Locked: true, HostedRelay: ws.HostedRelayDeny,
		},
	}, time.Now())

	loc, ok := m.Locate("wing")
	if !ok || !loc.Locked || loc.Revision != 2 || loc.HostedRelay != ws.HostedRelayDeny {
		t.Fatalf("newer snapshot config was not applied: %#v, found=%v", loc, ok)
	}
}

func TestWingMapSnapshotCannotDeleteGenerationCreatedAfterCapture(t *testing.T) {
	m := NewWingMap()
	snapshotAt := time.Now()
	m.Register("wing", WingLocation{
		MachineID: "edge-a", ConnectionID: "new",
		GenerationAt: snapshotAt.Add(time.Second), Revision: 1,
	})
	m.ReconcileSnapshot("edge-a", map[string]WingLocation{}, snapshotAt)
	if loc, ok := m.Locate("wing"); !ok || loc.ConnectionID != "new" {
		t.Fatalf("stale snapshot deleted newer generation: %#v, found=%v", loc, ok)
	}
}

func TestWingMapSnapshotDeletesMissingGenerationPresentAtCapture(t *testing.T) {
	m := NewWingMap()
	snapshotAt := time.Now()
	m.Register("wing", WingLocation{
		MachineID: "edge-a", ConnectionID: "old",
		GenerationAt: snapshotAt.Add(-time.Second), Revision: 1,
	})
	m.ReconcileSnapshot("edge-a", map[string]WingLocation{}, snapshotAt)
	if loc, ok := m.Locate("wing"); ok {
		t.Fatalf("authoritative snapshot retained missing old generation: %#v", loc)
	}
}

func TestWingMapSnapshotCannotStealWingFromAnotherMachine(t *testing.T) {
	m := NewWingMap()
	m.Register("wing", WingLocation{MachineID: "edge-b", ConnectionID: "current"})
	m.ReconcileSnapshot("edge-a", map[string]WingLocation{
		"wing": {MachineID: "edge-a", ConnectionID: "stale"},
	}, time.Now())
	if loc, ok := m.Locate("wing"); !ok || loc.MachineID != "edge-b" || loc.ConnectionID != "current" {
		t.Fatalf("stale machine snapshot stole wing: %#v, found=%v", loc, ok)
	}
}

func TestWingMapSnapshotRecoversWingFromExpiredMachine(t *testing.T) {
	m := NewWingMap()
	m.Register("wing", WingLocation{MachineID: "dead-edge", ConnectionID: "old"})
	m.mu.Lock()
	m.edges["dead-edge"] = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	m.ReconcileSnapshot("edge-b", map[string]WingLocation{
		"wing": {MachineID: "edge-b", ConnectionID: "current"},
	}, time.Now())
	if loc, ok := m.Locate("wing"); !ok || loc.MachineID != "edge-b" || loc.ConnectionID != "current" {
		t.Fatalf("expired machine retained wing: %#v, found=%v", loc, ok)
	}
}
