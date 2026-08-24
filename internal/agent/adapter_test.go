package agent

import (
	"strings"
	"testing"
	"time"
)

func TestRunHealthCheckTimesOut(t *testing.T) {
	start := time.Now()
	err := runHealthCheck(20*time.Millisecond, "sh", "-c", "sleep 5")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("runHealthCheck error = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("health timeout took %s", elapsed)
	}
}
