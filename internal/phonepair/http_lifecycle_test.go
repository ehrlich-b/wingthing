package phonepair

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Rapid shutdown must join discovery startup instead of racing a WaitGroup
// registration. Exercise both explicit Close and an already cancelled context.
func TestPhoneListenerImmediateShutdown(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "close", true: "cancel-before-listen"}[cancelled], func(t *testing.T) {
			dir := t.TempDir()
			private, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			server, err := Listen(ctx, dir, "fixture-discovery-wing", "Fixture", nil, private, &fixtureHandler{}, "", ModeLocalOnly)
			if err != nil {
				t.Fatal(err)
			}
			if server == nil {
				t.Skip("PAKE unavailable on this build")
			}
			defer server.Close()
			done := make(chan struct{})
			go func() { server.Close(); server.Close(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("immediate phone shutdown did not join")
			}
			if _, err := os.Stat(filepath.Join(dir, "phone-control.json")); !os.IsNotExist(err) {
				t.Fatalf("closed phone control remains: %v", err)
			}
			if err := server.listener.Close(); err == nil {
				t.Fatal("phone HTTP listener remained open")
			}
		})
	}
}
