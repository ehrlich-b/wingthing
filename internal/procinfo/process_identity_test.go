package procinfo

import (
	"os"
	"testing"
	"time"
)

func TestProcessIdentityBindsCurrentProcessLifetime(t *testing.T) {
	first, err := ProcessIdentity(os.Getpid())
	if err != nil || first == "" {
		t.Fatalf("current process identity: %q %v", first, err)
	}
	second, err := ProcessIdentity(os.Getpid())
	if err != nil || second != first {
		t.Fatalf("process identity changed during its lifetime: %q %v", second, err)
	}
	if _, err := ProcessIdentity(1 << 30); err == nil {
		t.Fatal("nonexistent process has a start identity")
	}
}

func TestProcessStartTimeBindsCurrentProcessLifetime(t *testing.T) {
	start, err := ProcessStartTime(os.Getpid())
	if err != nil || start.IsZero() || start.After(time.Now().Add(time.Second)) {
		t.Fatalf("current process start time: %v %v", start, err)
	}
	for _, pid := range []int{0, -1, 1 << 30} {
		if _, err := ProcessStartTime(pid); err == nil {
			t.Fatalf("invalid process PID %d has a start time", pid)
		}
	}
}
