package wing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

func TestWingOrgFlagReachesRuntimePolicy(t *testing.T) {
	home := config.CanonicalProviderPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", filepath.Join(home, "state"))
	if err := config.SaveWingConfig(filepath.Join(home, "state"), &config.WingConfig{ConnectionMode: "direct"}); err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fixture", http.StatusServiceUnavailable) }))
	defer relay.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshots := make(chan *config.WingConfig, 1)
	done := make(chan error, 1)
	options := EntryOptions{Version: "test", SetPolicySource: func(source func() (*config.WingConfig, *egg.EggConfig)) { wc, _ := source(); snapshots <- wc }}
	go func() {
		done <- RunWingWithContext(options, ctx, nil, relay.URL, "", "auto", "", "fixture-org", nil, "", false, false, false, false, false, &auth.DeviceToken{Token: "fixture"})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("fixture wing did not stop")
		}
	}()
	select {
	case wc := <-snapshots:
		if wc.Org != "fixture-org" {
			t.Fatalf("--org missing from runtime launch policy: %q", wc.Org)
		}
	case err := <-done:
		t.Fatalf("fixture startup failed: %v", err)
	case <-ctx.Done():
		t.Fatal("runtime policy not captured")
	}
}
