package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/pake"
	"github.com/ehrlich-b/wingthing/internal/phonepair"
)

// Pairing administration belongs to the existing main wing, not an egg.
// Start it through the public CLI, using Northstar's isolated HOME and state.
func storyboardPhoneMainWing(t *testing.T, f *northstarFixture, binary string) {
	t.Helper()
	// Register cleanup before startup so even a partial readiness failure is
	// cleaned up while the fixture's state directory and egg are still alive.
	t.Cleanup(func() {
		cmd := exec.Command(binary, "wing", "stop")
		cmd.Env, cmd.Dir = f.env, f.work
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("stop isolated main wing: %v\n%s", err, out)
		}
	})
	out, err := storyboardCommand(t, f, binary, "wing", "start", "--paths", f.work)
	if err != nil {
		t.Fatalf("start isolated main wing: %v\n%s", err, out)
	}
	storyboardContains(t, out, "local control: ready", "Local network only. That's fine; nothing else needed.")
	storyboardPhoneReady(t, f)
}

// General wing readiness is published before phone endpoint discovery. Wait
// for the specific authenticated, read-only endpoint instead of racing pair.
func storyboardPhoneReady(t *testing.T, f *northstarFixture) {
	t.Helper()
	if !pake.Available {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 300 * time.Millisecond, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	ready := func() bool {
		data, err := os.ReadFile(filepath.Join(f.local, "phone-control.json"))
		var control phonepair.Control
		if err != nil || json.Unmarshal(data, &control) != nil || control.Port < 1 || control.Port > 65535 || control.Token == "" {
			return false
		}
		req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://127.0.0.1:%d/admin", control.Port), bytes.NewBufferString(`{"Op":"devices"}`))
		if err != nil {
			return false
		}
		req.Header.Set("Authorization", "Bearer "+control.Token)
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var devices []json.RawMessage
		return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&devices) == nil
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatal("isolated main wing phone control did not become ready", ctx.Err())
		case <-ticker.C:
		}
	}
}
