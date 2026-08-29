package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestForwardWingEventIncludesCapabilityPolicy(t *testing.T) {
	received := make(chan map[string]any, 1)
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(internalSecretHeader); got != "shared-secret" {
			t.Errorf("internal secret = %q, want shared-secret", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode forwarded event: %v", err)
		}
		received <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer login.Close()

	generation := time.Now().Add(-time.Minute).Round(0)
	s := NewServer(nil, ServerConfig{LoginNodeAddr: login.URL, FlyMachineID: "edge-a", InternalSecret: "shared-secret"})
	s.forwardWingEvent("wing.config", &ConnectedWing{
		ID:             "connection-1",
		WingID:         "wing-1",
		UserID:         "user-1",
		PublicKey:      "key-1",
		PurposeBinding: true,
		DirectMCP:      true,
		HostedRelay:    "deny",
		ConnectedAt:    generation,
		Revision:       7,
	})

	body := <-received
	if got := body["purpose_binding"]; got != true {
		t.Errorf("purpose_binding = %#v, want true", got)
	}
	if got := body["direct_mcp"]; got != true {
		t.Errorf("direct_mcp = %#v, want true", got)
	}
	if got := body["hosted_relay"]; got != "deny" {
		t.Errorf("hosted_relay = %#v, want deny", got)
	}
	if got := body["connection_id"]; got != "connection-1" {
		t.Errorf("connection_id = %#v, want connection-1", got)
	}
	if got := body["machine_id"]; got != "edge-a" {
		t.Errorf("machine_id = %#v, want edge-a", got)
	}
	if got := body["connected_at_unix_nano"]; got != float64(generation.UnixNano()) {
		t.Errorf("connected_at_unix_nano = %#v, want %d", got, generation.UnixNano())
	}
	if got := body["revision"]; got != float64(7) {
		t.Errorf("revision = %#v, want 7", got)
	}
}

func TestInternalWingEventIgnoresStaleOfflineGeneration(t *testing.T) {
	s := NewServer(nil, ServerConfig{NodeRole: "login"})
	s.WingMap = NewWingMap()
	s.WingMap.Register("wing-1", WingLocation{MachineID: "edge-a", ConnectionID: "current"})
	events := make(chan WingEvent, 1)
	s.Wings.Subscribe("user-1", nil, events)

	request := httptest.NewRequest(http.MethodPost, "/internal/wing-event", strings.NewReader(
		`{"type":"wing.offline","wing_id":"wing-1","connection_id":"stale","machine_id":"edge-a","user_id":"user-1"}`,
	))
	response := httptest.NewRecorder()
	s.handleInternalWingEvent(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if loc, ok := s.WingMap.Locate("wing-1"); !ok || loc.ConnectionID != "current" {
		t.Fatalf("current registration was removed: %#v, found=%v", loc, ok)
	}
	select {
	case event := <-events:
		t.Fatalf("stale offline event was delivered: %#v", event)
	default:
	}
}

func TestInternalWingEventIgnoresStaleConfigRevision(t *testing.T) {
	s := NewServer(nil, ServerConfig{NodeRole: "login"})
	s.WingMap = NewWingMap()
	s.WingMap.Register("wing-1", WingLocation{
		MachineID: "edge-a", ConnectionID: "current", Revision: 2,
		Locked: true, HostedRelay: "deny",
	})
	events := make(chan WingEvent, 1)
	s.Wings.Subscribe("user-1", nil, events)

	request := httptest.NewRequest(http.MethodPost, "/internal/wing-event", strings.NewReader(
		`{"type":"wing.config","wing_id":"wing-1","connection_id":"current","machine_id":"edge-a","revision":1,"user_id":"user-1","locked":false,"hosted_relay":"allow"}`,
	))
	response := httptest.NewRecorder()
	s.handleInternalWingEvent(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ignored":"stale"`) {
		t.Fatalf("stale config response = %d %s", response.Code, response.Body.String())
	}
	select {
	case event := <-events:
		t.Fatalf("stale config event was delivered: %#v", event)
	default:
	}
}

func TestInternalWingEventIgnoresAttentionFromStaleConnection(t *testing.T) {
	s := NewServer(nil, ServerConfig{NodeRole: "login"})
	s.WingMap = NewWingMap()
	s.WingMap.Register("wing-1", WingLocation{MachineID: "edge-a", ConnectionID: "current"})
	events := make(chan WingEvent, 1)
	s.Wings.Subscribe("user-1", nil, events)

	request := httptest.NewRequest(http.MethodPost, "/internal/wing-event", strings.NewReader(
		`{"type":"session.attention","wing_id":"wing-1","connection_id":"stale","machine_id":"edge-a","user_id":"user-1","session_id":"session-1"}`,
	))
	response := httptest.NewRecorder()
	s.handleInternalWingEvent(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ignored":"stale"`) {
		t.Fatalf("stale attention response = %d %s", response.Code, response.Body.String())
	}
	select {
	case event := <-events:
		t.Fatalf("stale attention event was delivered: %#v", event)
	default:
	}
}

func TestInternalWingEventDeliversCurrentConfigRevision(t *testing.T) {
	s := NewServer(nil, ServerConfig{NodeRole: "login"})
	s.WingMap = NewWingMap()
	s.WingMap.Register("wing-1", WingLocation{
		MachineID: "edge-a", ConnectionID: "current", Revision: 2,
		Locked: true, HostedRelay: "deny",
	})
	events := make(chan WingEvent, 1)
	s.Wings.Subscribe("user-1", nil, events)

	request := httptest.NewRequest(http.MethodPost, "/internal/wing-event", strings.NewReader(
		`{"type":"wing.config","wing_id":"wing-1","connection_id":"current","machine_id":"edge-a","revision":2,"user_id":"user-1","locked":true,"hosted_relay":"deny"}`,
	))
	response := httptest.NewRecorder()
	s.handleInternalWingEvent(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("current config response = %d %s", response.Code, response.Body.String())
	}
	select {
	case event := <-events:
		if event.Locked == nil || !*event.Locked || event.HostedRelay == nil || *event.HostedRelay != "deny" {
			t.Fatalf("current config event = %#v", event)
		}
	default:
		t.Fatal("current config event was not delivered")
	}
}

func TestInternalWingEventRepairsMissedRegistrationBeforeDelivery(t *testing.T) {
	s := NewServer(nil, ServerConfig{NodeRole: "login"})
	s.WingMap = NewWingMap()
	generation := time.Now().Add(-time.Minute).UnixNano()
	events := make(chan WingEvent, 1)
	s.Wings.Subscribe("user-1", nil, events)

	request := httptest.NewRequest(http.MethodPost, "/internal/wing-event", strings.NewReader(
		fmt.Sprintf(`{"type":"wing.online","wing_id":"wing-1","connection_id":"current","machine_id":"edge-a","connected_at_unix_nano":%d,"revision":1,"user_id":"user-1","public_key":"key-1","locked":false,"allowed_count":0,"purpose_binding":true,"direct_mcp":true,"hosted_relay":"deny"}`, generation),
	))
	response := httptest.NewRecorder()
	s.handleInternalWingEvent(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("self-healing event response = %d %s", response.Code, response.Body.String())
	}
	location, ok := s.WingMap.Locate("wing-1")
	if !ok || location.ConnectionID != "current" || location.Revision != 1 ||
		!location.PurposeBinding || !location.DirectMCP || location.HostedRelay != "deny" {
		t.Fatalf("repaired registration = %#v, found=%v", location, ok)
	}
	select {
	case event := <-events:
		if event.Type != "wing.online" || event.DirectMCP == nil || !*event.DirectMCP {
			t.Fatalf("repaired event = %#v", event)
		}
	default:
		t.Fatal("self-healing event was not delivered")
	}
}

func TestInternalWingEventRemovesMatchingOfflineGeneration(t *testing.T) {
	s := NewServer(nil, ServerConfig{NodeRole: "login"})
	s.WingMap = NewWingMap()
	s.WingMap.Register("wing-1", WingLocation{MachineID: "edge-a", ConnectionID: "current"})
	events := make(chan WingEvent, 1)
	s.Wings.Subscribe("user-1", nil, events)

	request := httptest.NewRequest(http.MethodPost, "/internal/wing-event", strings.NewReader(
		`{"type":"wing.offline","wing_id":"wing-1","connection_id":"current","machine_id":"edge-a","user_id":"user-1"}`,
	))
	response := httptest.NewRecorder()
	s.handleInternalWingEvent(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if loc, ok := s.WingMap.Locate("wing-1"); ok {
		t.Fatalf("matching registration remains: %#v", loc)
	}
	select {
	case event := <-events:
		if event.Type != "wing.offline" {
			t.Fatalf("event = %#v", event)
		}
	default:
		t.Fatal("matching offline event was not delivered")
	}
}

func TestInternalWingEventPreservesOptionalCapabilities(t *testing.T) {
	s := NewServer(nil, ServerConfig{})
	events := make(chan WingEvent, 1)
	s.Wings.Subscribe("user-1", nil, events)

	body := `{"type":"wing.config","wing_id":"wing-1","user_id":"user-1","locked":false,"allowed_count":0,"purpose_binding":false,"direct_mcp":true,"hosted_relay":"deny"}`
	request := httptest.NewRequest(http.MethodPost, "/internal/wing-event", strings.NewReader(body))
	response := httptest.NewRecorder()
	s.handleInternalWingEvent(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}

	event := <-events
	if event.Locked == nil || *event.Locked {
		t.Errorf("locked = %#v, want pointer to false", event.Locked)
	}
	if event.AllowedCount == nil || *event.AllowedCount != 0 {
		t.Errorf("allowed_count = %#v, want pointer to zero", event.AllowedCount)
	}
	if event.PurposeBinding == nil || *event.PurposeBinding {
		t.Errorf("purpose_binding = %#v, want pointer to false", event.PurposeBinding)
	}
	if event.DirectMCP == nil || !*event.DirectMCP {
		t.Errorf("direct_mcp = %#v, want pointer to true", event.DirectMCP)
	}
	if event.HostedRelay == nil || *event.HostedRelay != "deny" {
		t.Errorf("hosted_relay = %#v, want pointer to deny", event.HostedRelay)
	}
}

func TestInternalWingEventAcceptsLegacyMissingCapabilities(t *testing.T) {
	s := NewServer(nil, ServerConfig{})
	events := make(chan WingEvent, 1)
	s.Wings.Subscribe("user-1", nil, events)

	body := `{"type":"wing.online","wing_id":"wing-1","user_id":"user-1"}`
	request := httptest.NewRequest(http.MethodPost, "/internal/wing-event", strings.NewReader(body))
	response := httptest.NewRecorder()
	s.handleInternalWingEvent(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}

	event := <-events
	if event.Locked != nil || event.AllowedCount != nil || event.PurposeBinding != nil || event.DirectMCP != nil || event.HostedRelay != nil {
		t.Fatalf("legacy event invented omitted capabilities: %#v", event)
	}
}

func TestInternalWingEventRejectsOversizedBody(t *testing.T) {
	s := NewServer(nil, ServerConfig{})
	body := `{"type":"org.changed","user_id":"user-1"}` + strings.Repeat(" ", 8192)
	request := httptest.NewRequest(http.MethodPost, "/internal/wing-event", strings.NewReader(body))
	response := httptest.NewRecorder()
	s.handleInternalWingEvent(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
}
