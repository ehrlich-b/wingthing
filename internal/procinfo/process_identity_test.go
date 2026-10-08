package procinfo

import (
	"os"
	"testing"
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
