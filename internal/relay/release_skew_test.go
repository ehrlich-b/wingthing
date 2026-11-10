package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/auth"
)

// TestReleasedCLISkew executes the shipped artifact, never a rebuild of its tag.
// make test-release-skew requires it; ordinary unit tests remain artifact-free.
func TestReleasedCLISkew(t *testing.T) {
	if os.Getenv("WT_RELEASE_SKEW_REQUIRED") != "1" {
		t.Skip("run make test-release-skew with downloads/v0.149.5 staged")
	}
	binary, err := filepath.Abs("../../downloads/v0.149.5/wt-" + runtime.GOOS + "-" + runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("released v0.149.5 binary required: %v", err)
	}
	store := testStore(t)
	token, userID := createTestToken(t, store, "release-skew")
	server := NewServer(store, ServerConfig{})
	// Suppress the release-feed network fetch; the tested HTTP server is real.
	server.latestVersion = "v0.149.5"
	server.latestVersionAt = time.Now()
	var mu sync.Mutex
	var requests []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		server.ServeHTTP(w, r)
	}))
	defer ts.Close()
	state := t.TempDir()
	mustTest(t, os.WriteFile(filepath.Join(state, "config.yaml"), []byte("roost_url: "+ts.URL+"\n"), 0600))
	mustTest(t, auth.NewTokenStore(state).Save(&auth.DeviceToken{Token: token, UserID: userID, DeviceID: "release-skew"}))
	home := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "WINGTHING_DIR=" + state,
			"TMPDIR=" + os.TempDir(), "NO_COLOR=1"}
		output, err := command.CombinedOutput()
		t.Logf("v0.149.5 wt %s against HEAD relay %s: exit=%v\n%s", strings.Join(args, " "), ts.URL, err, output)
		if err != nil {
			t.Fatalf("released CLI failed: %v", err)
		}
		return string(output)
	}
	if version := run("--version"); !strings.Contains(version, "0.149.5") {
		t.Fatalf("wrong released binary: %s", version)
	}
	if identity := run("whoami"); !strings.Contains(identity, userID) {
		t.Fatalf("wrong identity: %s", identity)
	}
	var wings []map[string]any
	mustTest(t, json.Unmarshal([]byte(run("wings", "--json", "--no-probe")), &wings))
	if len(wings) != 0 {
		t.Fatalf("unexpected fixture wings: %v", wings)
	}
	// Native attach uses local/SSH wing control, not the HTTP relay. Keep its
	// noninteractive inventory contract in the same previous-release probe.
	var sessions []map[string]any
	mustTest(t, json.Unmarshal([]byte(run("attach", "--json")), &sessions))
	if len(sessions) != 0 {
		t.Fatalf("unexpected fixture sessions: %v", sessions)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Logf("HEAD relay requests: %v", requests)
	for _, expected := range []string{"GET /auth/check", "GET /api/app/wings"} {
		found := false
		for _, request := range requests {
			if request == expected {
				found = true
			}
		}
		if !found {
			t.Errorf("released CLI did not reach %s", expected)
		}
	}
}
