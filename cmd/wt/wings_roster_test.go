package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/auth"
)

const remediationWingID = "0123456789abcdef01234567"

func remediationProfile(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".wingthing")
	t.Setenv("HOME", home)
	t.Setenv("WINGTHING_DIR", dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("wing_id: "+remediationWingID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := auth.NewTokenStore(dir).Save(&auth.DeviceToken{Token: "same-fixture-credential"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func remediationList(t *testing.T, relay string) ([]wingFinderEntry, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	before := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = before }()
	cmd := wingsCmd()
	cmd.SetArgs([]string{"--roost", relay, "--no-probe", "--json"})
	err = cmd.Execute()
	_ = w.Close()
	data, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("CLI roster: %s; error: %v", data, err)
	if err != nil {
		return nil, err
	}
	var entries []wingFinderEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	return entries, nil
}

func TestRemediationAccessRevocation(t *testing.T) {
	remediationProfile(t)
	var revoked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/app/wings" || r.Header.Get("Authorization") != "Bearer same-fixture-credential" {
			t.Errorf("unexpected roster request: %s", r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if revoked.Load() {
			_, _ = io.WriteString(w, "[]")
			return
		}
		_, _ = io.WriteString(w, `[{"wing_id":"`+remediationWingID+`","org_id":"fixture-org","user_id":"other-owner"}]`)
	}))
	defer server.Close()
	entries, err := remediationList(t, server.URL)
	if err != nil || len(entries) != 1 || !entries[0].Online {
		t.Fatalf("initial roster: %+v, %v", entries, err)
	}
	// The same credential now receives an empty access-filtered roster while
	// the fixture wing remains connected for its owner.
	revoked.Store(true)
	entries, err = remediationList(t, server.URL)
	if err != nil || len(entries) != 0 {
		t.Fatalf("REVOKED_WING_RETAINED: want empty authoritative roster, got %+v, %v", entries, err)
	}
}

func TestRemediationUnwritableHistory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("probe requires a non-root user")
	}
	dir := remediationProfile(t)
	defer func() { _ = os.Chmod(dir, 0700) }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// config.Load normalizes directory permissions. Remove write permission
		// after that read, at the successful roster response, to isolate the new
		// mandatory history write from existing profile initialization.
		if err := os.Chmod(dir, 0500); err != nil {
			t.Error(err)
		}
		_, _ = io.WriteString(w, `[{"wing_id":"`+remediationWingID+`"}]`)
	}))
	defer server.Close()
	entries, err := remediationList(t, server.URL)
	if err != nil || len(entries) != 1 || !entries[0].Online {
		t.Fatalf("ONLINE_ROSTER_REQUIRES_HISTORY_WRITE: got %+v, %v", entries, err)
	}
}
