package timeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/agent"
	"github.com/ehrlich-b/wingthing/internal/store"
)

func TestHealthCacheHit(t *testing.T) {
	probeCount := 0
	ag := &mockAgent{
		output: "ok",
		healthErr: nil,
	}
	// Wrap Health to count probes
	countingAgent := &healthCountAgent{Agent: ag, count: &probeCount}

	eng, _ := setupEngine(t, ag)
	eng.Agents = map[string]agent.Agent{"test": countingAgent}

	// First call should probe
	if !eng.CheckHealth("test") {
		t.Fatal("expected healthy")
	}
	if probeCount != 1 {
		t.Fatalf("expected 1 probe, got %d", probeCount)
	}

	// Second call within TTL should use cache
	if !eng.CheckHealth("test") {
		t.Fatal("expected healthy")
	}
	if probeCount != 1 {
		t.Fatalf("expected still 1 probe (cached), got %d", probeCount)
	}
}

func TestHealthCacheExpiry(t *testing.T) {
	probeCount := 0
	ag := &mockAgent{output: "ok"}
	countingAgent := &healthCountAgent{Agent: ag, count: &probeCount}

	eng, _ := setupEngine(t, ag)
	eng.Agents = map[string]agent.Agent{"test": countingAgent}

	// Prime the cache
	eng.CheckHealth("test")
	if probeCount != 1 {
		t.Fatalf("expected 1 probe, got %d", probeCount)
	}

	// Manually expire the cache entry
	eng.healthMu.Lock()
	eng.healthCache["test"] = healthEntry{
		healthy:   true,
		checkedAt: time.Now().Add(-2 * healthCacheTTL),
	}
	eng.healthMu.Unlock()

	// Next call should re-probe
	eng.CheckHealth("test")
	if probeCount != 2 {
		t.Fatalf("expected 2 probes after expiry, got %d", probeCount)
	}
}

func TestHealthCacheUpdatesStore(t *testing.T) {
	ag := &mockAgent{output: "ok"}
	eng, s := setupEngine(t, ag)

	eng.CheckHealth("test")

	stored, err := s.GetAgent("test")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Healthy {
		t.Error("expected stored agent to be healthy")
	}
	if stored.HealthChecked == nil {
		t.Error("expected health_checked to be set")
	}
}

func TestUnhealthyAgentBlocksDispatch(t *testing.T) {
	ag := &mockAgent{
		output:    "won't run",
		healthErr: fmt.Errorf("agent down"),
	}
	eng, s := setupEngine(t, ag)

	task := &store.Task{
		ID:    "t-health-001",
		Type:  "prompt",
		What:  "do something",
		RunAt: time.Now().Add(-time.Second),
		Agent: "test",
	}
	s.CreateTask(task)

	eng.poll(context.Background())

	got, _ := s.GetTask("t-health-001")
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error == nil || !strings.Contains(*got.Error, "unhealthy") {
		t.Errorf("error = %v, want unhealthy message", got.Error)
	}
}

// healthCountAgent wraps an agent to count Health() calls.
type healthCountAgent struct {
	agent.Agent
	count *int
}

func (h *healthCountAgent) Health() error {
	*h.count++
	return h.Agent.Health()
}
