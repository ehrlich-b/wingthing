package relay

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnrollmentRejectsInvalidFieldsWithoutPersisting(t *testing.T) {
	store := testStore(t)
	server := NewServer(store, ServerConfig{})
	for _, tc := range []struct{ wing, key string }{
		{strings.Repeat("x", 257), ""}, {"bad\nwing", ""}, {"wing", "not-base64"},
		{"wing", base64.StdEncoding.EncodeToString(make([]byte, 31))},
		{"wing", strings.Repeat("A", 10000)},
	} {
		body, _ := json.Marshal(map[string]string{"wing_id": tc.wing, "public_key": tc.key})
		rr := httptest.NewRecorder()
		server.handleAuthDevice(rr, httptest.NewRequest(http.MethodPost, "/auth/device", strings.NewReader(string(body))))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("invalid enrollment accepted: status %d", rr.Code)
		}
	}
	var count int
	mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM device_codes").Scan(&count))
	if count != 0 {
		t.Fatalf("invalid enrollments persisted %d rows", count)
	}
	for _, key := range []string{"", base64.StdEncoding.EncodeToString(make([]byte, 32))} {
		body, _ := json.Marshal(map[string]string{"wing_id": "legacy-host.local", "public_key": key})
		rr := httptest.NewRecorder()
		server.handleAuthDevice(rr, httptest.NewRequest(http.MethodPost, "/auth/device", strings.NewReader(string(body))))
		if rr.Code != http.StatusOK {
			t.Fatalf("compatible enrollment rejected: %s", rr.Body.String())
		}
	}
}

func TestEnrollmentPendingLimitsAndOnInsertCleanup(t *testing.T) {
	store := testStore(t)
	server := NewServer(store, ServerConfig{ResourceLimits: ResourceLimits{PendingGrants: 2, PendingGrantsPerIP: 1}})
	request := func(ip string, status int, message string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/auth/device", strings.NewReader(`{"wing_id":"wing"}`))
		r.RemoteAddr = ip + ":1234"
		rr := httptest.NewRecorder()
		server.handleAuthDevice(rr, r)
		if rr.Code != status || !strings.Contains(rr.Body.String(), message) {
			t.Fatalf("enrollment %s: %d %s", ip, rr.Code, rr.Body.String())
		}
	}
	request("198.51.100.1", 200, "device_code")
	request("198.51.100.1", 429, "this IP")
	request("198.51.100.2", 200, "device_code")
	request("198.51.100.3", 429, "capacity")
	mustTestExec(t, store.DB(), "UPDATE device_codes SET expires_at = datetime('now', '-1 minute')")
	request("198.51.100.1", 200, "device_code")
	var count int
	mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM device_codes").Scan(&count))
	if count != 1 {
		t.Fatalf("expired grants retained: %d", count)
	}
}

func TestEnrollmentLimitIsAtomicAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	first, err := OpenRelay(path)
	mustTest(t, err)
	t.Cleanup(func() { _ = first.Close() })
	second, err := OpenRelay(path)
	mustTest(t, err)
	t.Cleanup(func() { _ = second.Close() })
	admission := deviceGrantAdmission{IP: "198.51.100.1", Limits: ResourceLimits{PendingGrants: 1, PendingGrantsPerIP: 1}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, store := range []*RelayStore{first, second} {
		wg.Go(func() {
			errs <- store.createDeviceCode(fmt.Sprint(i), fmt.Sprint(i), "wing", "", time.Now().Add(time.Minute), admission)
		})
	}
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrDeviceGrantLimit) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d grants with capacity 1", accepted)
	}
}

func TestEnrollmentPeriodicCleanupPreservesClaimedGrants(t *testing.T) {
	t.Setenv("WT_RELAY_SWEEP_INTERVAL", "10ms")
	store := testStore(t)
	mustTest(t, store.CreateDeviceCode("claimed", "DEF", "wing", time.Now().Add(time.Hour)))
	mustTestExec(t, store.DB(), "UPDATE device_codes SET claimed = 1, expires_at = datetime('now', '-1 hour') WHERE code = 'claimed'")
	mustTestExec(t, store.DB(), "INSERT INTO device_codes (code, user_code, device_id, expires_at) VALUES ('unclaimed', 'ABC', 'wing', datetime('now', '-1 hour'))")
	deadline := time.Now().Add(2 * time.Second)
	for {
		var count int
		mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM device_codes WHERE code = 'unclaimed'").Scan(&count))
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("periodic sweep retained expired unclaimed grant")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var count int
	mustTest(t, store.DB().QueryRow("SELECT COUNT(*) FROM device_codes WHERE code = 'claimed'").Scan(&count))
	if count != 1 {
		t.Fatal("sweep removed claimed grant")
	}
}

func TestRelayLimitsEnvironmentAndConfig(t *testing.T) {
	t.Setenv("WT_RELAY_PENDING_GRANTS", "77")
	t.Setenv("WT_RELAY_PENDING_GRANTS_PER_IP", "9")
	limits := (ResourceLimits{}).withDefaults()
	if limits.PendingGrants != 77 || limits.PendingGrantsPerIP != 9 {
		t.Fatalf("environment limits = %#v", limits)
	}
	limits = (ResourceLimits{PendingGrants: 3}).withDefaults()
	if limits.PendingGrants != 3 {
		t.Fatal("config did not override environment")
	}
}
