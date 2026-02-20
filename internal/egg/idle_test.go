package egg

import (
	"testing"
	"time"
)

func TestIdleDuration_NoIO(t *testing.T) {
	sess := &Session{
		StartedAt: time.Now().Add(-5 * time.Minute),
	}
	idle := sess.idleDuration()
	if idle < 4*time.Minute || idle > 6*time.Minute {
		t.Errorf("expected ~5m idle (no I/O, using uptime), got %s", idle)
	}
}

func TestIdleDuration_OutputOnly(t *testing.T) {
	sess := &Session{
		StartedAt:  time.Now().Add(-10 * time.Minute),
		lastOutput: time.Now().Add(-30 * time.Second),
	}
	idle := sess.idleDuration()
	if idle < 25*time.Second || idle > 35*time.Second {
		t.Errorf("expected ~30s idle (since last output), got %s", idle)
	}
}

func TestIdleDuration_InputOnly(t *testing.T) {
	sess := &Session{
		StartedAt: time.Now().Add(-10 * time.Minute),
		lastInput: time.Now().Add(-15 * time.Second),
	}
	idle := sess.idleDuration()
	if idle < 10*time.Second || idle > 20*time.Second {
		t.Errorf("expected ~15s idle (since last input), got %s", idle)
	}
}

func TestIdleDuration_BothIO_OutputMoreRecent(t *testing.T) {
	sess := &Session{
		StartedAt:  time.Now().Add(-10 * time.Minute),
		lastInput:  time.Now().Add(-2 * time.Minute),
		lastOutput: time.Now().Add(-10 * time.Second),
	}
	idle := sess.idleDuration()
	// Should use the more recent of the two (output at 10s ago)
	if idle < 5*time.Second || idle > 15*time.Second {
		t.Errorf("expected ~10s idle (output more recent), got %s", idle)
	}
}

func TestIdleDuration_BothIO_InputMoreRecent(t *testing.T) {
	sess := &Session{
		StartedAt:  time.Now().Add(-10 * time.Minute),
		lastInput:  time.Now().Add(-5 * time.Second),
		lastOutput: time.Now().Add(-3 * time.Minute),
	}
	idle := sess.idleDuration()
	// Should use the more recent of the two (input at 5s ago)
	if idle < 2*time.Second || idle > 10*time.Second {
		t.Errorf("expected ~5s idle (input more recent), got %s", idle)
	}
}

func TestIdleDuration_JustStarted(t *testing.T) {
	sess := &Session{
		StartedAt: time.Now(),
	}
	idle := sess.idleDuration()
	if idle > 2*time.Second {
		t.Errorf("expected near-zero idle for just-started session, got %s", idle)
	}
}

func TestIdleDuration_ActiveSession(t *testing.T) {
	sess := &Session{
		StartedAt:  time.Now().Add(-1 * time.Hour),
		lastOutput: time.Now(), // output just now
	}
	idle := sess.idleDuration()
	if idle > 2*time.Second {
		t.Errorf("expected near-zero idle for active session, got %s", idle)
	}
}
